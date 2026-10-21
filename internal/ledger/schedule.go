package ledger

import (
	"context"
	"fmt"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/jobs"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// The nightly runs, in UTC (15:30/16:30/17:30 UTC = 9/10/11 PM India time,
// which has no daylight saving).
var nightlySlots = []struct {
	Attempt int
	Hour    int
}{{1, 15}, {2, 16}, {3, 17}}

// Grace is how late a slot may still be queued (a worker that was down at the
// exact minute catches up, but a stale slot is not run hours later).
const Grace = 20 * time.Minute

// RunScheduler queues the nightly receipt collection at its times until ctx
// ends. Several workers may run it; a slot is queued once.
func (s *Service) RunScheduler(ctx context.Context, q *jobs.Queue) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		s.fireDueSlots(ctx, q, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Service) fireDueSlots(ctx context.Context, q *jobs.Queue, now time.Time) {
	now = now.UTC()
	for _, slot := range nightlySlots {
		at := time.Date(now.Year(), now.Month(), now.Day(), slot.Hour, 30, 0, 0, time.UTC)
		if now.Before(at) || now.After(at.Add(Grace)) {
			continue
		}
		key := fmt.Sprintf("nightly_receipts:%s:%d", at.Format("2006-01-02"), slot.Attempt)
		fired, err := firstToFire(ctx, s.Store, key)
		if err != nil {
			s.log().Error("scheduler", "err", err)
			continue
		}
		if !fired {
			continue
		}
		if _, err := q.Enqueue(ctx, jobs.KindNightlyReceipts, map[string]any{"attempt": slot.Attempt}, time.Time{}); err != nil {
			s.log().Error("could not queue the nightly receipts", "err", err)
		}
	}
}

// firstToFire records the slot; it is true for exactly one caller.
func firstToFire(ctx context.Context, st *store.Store, slot string) (bool, error) {
	tag, err := st.DB.Pool.Exec(ctx, `INSERT INTO df_schedule_fire (slot) VALUES ($1) ON CONFLICT DO NOTHING`, slot)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
