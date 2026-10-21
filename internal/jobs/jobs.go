// Package jobs is the background job queue: PDF processing, portal
// submissions and the nightly receipt collection. It lives in Postgres
// (SELECT ... FOR UPDATE SKIP LOCKED) instead of Celery + Redis.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/shahinshabab/deedflow-go/internal/db"
)

// WakeChannel is notified when a job is enqueued, so workers needn't wait for their poll.
const WakeChannel = "deedflow_jobs"

// Job kinds.
const (
	KindProcessPDF      = "process_deed_pdf"
	KindSubmitDeed      = "submit_deed_to_portal"
	KindCollectReceipts = "collect_org_receipts"
	KindNightlyReceipts = "nightly_receipts"
	KindScrapeDropdowns = "scrape_portal_dropdowns"
)

// Job is a claimed unit of work.
type Job struct {
	ID       int64
	Kind     string
	Args     json.RawMessage
	Attempts int
}

// Decode reads the job's arguments.
func (j Job) Decode(v any) error { return json.Unmarshal(j.Args, v) }

// Queue enqueues and claims jobs.
type Queue struct {
	DB  *db.DB
	Log *slog.Logger
}

// New returns a queue.
func New(d *db.DB, log *slog.Logger) *Queue { return &Queue{DB: d, Log: log} }

// Enqueue adds a job to run now (or at runAt if non-zero).
func (q *Queue) Enqueue(ctx context.Context, kind string, args any, runAt time.Time) (int64, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return 0, err
	}
	if runAt.IsZero() {
		runAt = time.Now()
	}
	var id int64
	if err := q.DB.Pool.QueryRow(ctx, `INSERT INTO df_job (kind, args, run_at) VALUES ($1, $2, $3) RETURNING id`,
		kind, raw, runAt).Scan(&id); err != nil {
		return 0, err
	}
	_ = q.DB.Notify(ctx, WakeChannel, kind)
	return id, nil
}

// Claim takes the oldest runnable job of one of kinds, or returns nil.
func (q *Queue) Claim(ctx context.Context, worker string, kinds []string) (*Job, error) {
	var j Job
	err := q.DB.Pool.QueryRow(ctx, `UPDATE df_job SET status = 'running', locked_by = $1, heartbeat_at = now(), attempts = attempts + 1
		WHERE id = (SELECT id FROM df_job WHERE status = 'queued' AND run_at <= now() AND kind = ANY($2)
		            ORDER BY run_at, id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id, kind, args, attempts`, worker, kinds).Scan(&j.ID, &j.Kind, &j.Args, &j.Attempts)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// Heartbeat marks a running job alive.
func (q *Queue) Heartbeat(ctx context.Context, id int64) {
	_, _ = q.DB.Pool.Exec(ctx, `UPDATE df_job SET heartbeat_at = now() WHERE id = $1 AND status = 'running'`, id)
}

// Done marks a job finished.
func (q *Queue) Done(ctx context.Context, id int64) error {
	_, err := q.DB.Pool.Exec(ctx, `UPDATE df_job SET status = 'done', finished_at = now() WHERE id = $1`, id)
	return err
}

// Fail marks a job failed, keeping the reason.
func (q *Queue) Fail(ctx context.Context, id int64, reason string) error {
	_, err := q.DB.Pool.Exec(ctx, `UPDATE df_job SET status = 'failed', finished_at = now(), last_error = $2 WHERE id = $1`, id, reason)
	return err
}

// Requeue puts a running job back to run again at runAt (Celery's self.retry).
func (q *Queue) Requeue(ctx context.Context, id int64, runAt time.Time) error {
	_, err := q.DB.Pool.Exec(ctx, `UPDATE df_job SET status = 'queued', run_at = $2, locked_by = '' WHERE id = $1`, id, runAt)
	return err
}

// ReapStale requeues running jobs whose worker stopped heartbeating - the
// job's worker was killed (a deploy, out of memory) - so they run again
// (acks_late + reject_on_worker_lost in the Celery version).
func (q *Queue) ReapStale(ctx context.Context, silentFor time.Duration) (int64, error) {
	tag, err := q.DB.Pool.Exec(ctx, `UPDATE df_job SET status = 'queued', locked_by = '', run_at = now()
		WHERE status = 'running' AND heartbeat_at < now() - make_interval(secs => $1)`, silentFor.Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Purge deletes finished jobs older than age.
func (q *Queue) Purge(ctx context.Context, age time.Duration) {
	_, _ = q.DB.Pool.Exec(ctx, `DELETE FROM df_job WHERE status IN ('done','failed') AND finished_at < now() - make_interval(secs => $1)`, age.Seconds())
}

// HandlerFunc runs one job. Returning ErrRetryLater(runAt) requeues it.
type HandlerFunc func(ctx context.Context, j Job) error

// RetryLater asks the worker to run the job again after delay.
type RetryLater struct{ After time.Duration }

func (r RetryLater) Error() string { return fmt.Sprintf("retry in %s", r.After) }

// Worker runs jobs from a Queue.
type Worker struct {
	Q        *Queue
	ID       string
	Handlers map[string]HandlerFunc
	// Concurrency is how many jobs of a kind may run at once; kinds missing
	// from the map use 1.
	Concurrency map[string]int
	Log         *slog.Logger

	mu      sync.Mutex
	running map[string]int
}

// Run claims and runs jobs until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	w.running = map[string]int{}
	wake := make(chan struct{}, 1)
	go func() {
		for ctx.Err() == nil {
			l, err := w.Q.DB.Listen(ctx, WakeChannel)
			if err != nil {
				select {
				case <-time.After(2 * time.Second):
				case <-ctx.Done():
				}
				continue
			}
			for {
				if _, err := l.Wait(ctx); err != nil {
					break
				}
				select {
				case wake <- struct{}{}:
				default:
				}
			}
			l.Close()
		}
	}()
	reaper := time.NewTicker(30 * time.Second)
	defer reaper.Stop()
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	var wg sync.WaitGroup
	for {
		for w.claimOne(ctx, &wg) {
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-wake:
		case <-poll.C:
		case <-reaper.C:
			if n, err := w.Q.ReapStale(ctx, 3*time.Minute); err == nil && n > 0 {
				w.Log.Warn("requeued jobs from a dead worker", "count", n)
			}
			w.Q.Purge(ctx, 7*24*time.Hour)
		}
	}
}

func (w *Worker) availableKinds() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var kinds []string
	for kind := range w.Handlers {
		limit := w.Concurrency[kind]
		if limit < 1 {
			limit = 1
		}
		if w.running[kind] < limit {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func (w *Worker) claimOne(ctx context.Context, wg *sync.WaitGroup) bool {
	if ctx.Err() != nil {
		return false
	}
	kinds := w.availableKinds()
	if len(kinds) == 0 {
		return false
	}
	job, err := w.Q.Claim(ctx, w.ID, kinds)
	if err != nil {
		if ctx.Err() == nil {
			w.Log.Error("claiming a job", "err", err)
		}
		return false
	}
	if job == nil {
		return false
	}
	w.mu.Lock()
	w.running[job.Kind]++
	w.mu.Unlock()
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			w.mu.Lock()
			w.running[job.Kind]--
			w.mu.Unlock()
		}()
		w.run(ctx, *job)
	}()
	return true
}

func (w *Worker) run(ctx context.Context, job Job) {
	hbCtx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				w.Q.Heartbeat(hbCtx, job.ID)
			}
		}
	}()
	defer func() {
		if rec := recover(); rec != nil {
			w.Log.Error("job panicked", "kind", job.Kind, "id", job.ID, "panic", rec)
			_ = w.Q.Fail(context.Background(), job.ID, fmt.Sprint("panic: ", rec))
		}
	}()
	h := w.Handlers[job.Kind]
	err := h(ctx, job)
	switch e := err.(type) {
	case nil:
		_ = w.Q.Done(context.Background(), job.ID)
	case RetryLater:
		_ = w.Q.Requeue(context.Background(), job.ID, time.Now().Add(e.After))
	default:
		if ctx.Err() != nil {
			// Shutting down mid-job: leave it running; the reaper requeues it.
			return
		}
		w.Log.Error("job failed", "kind", job.Kind, "id", job.ID, "err", err)
		_ = w.Q.Fail(context.Background(), job.ID, err.Error())
	}
}
