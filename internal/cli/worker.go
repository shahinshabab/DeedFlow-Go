package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/shahinshabab/deedflow-go/internal/jobs"
)

func (e *Env) worker(ctx context.Context) error {
	host, _ := os.Hostname()
	w := &jobs.Worker{
		Q:           e.Queue,
		ID:          fmt.Sprintf("%s-%d", host, os.Getpid()),
		Handlers:    map[string]jobs.HandlerFunc{},
		Concurrency: map[string]int{jobs.KindProcessPDF: e.Cfg.ProcessingWorkers},
		Log:         e.Log,
	}
	registerJobs(e, w)
	e.Log.Info("worker started", "id", w.ID)
	w.Run(ctx)
	return nil
}

// registerJobs is filled in by the packages that own the jobs.
var registerJobs = func(e *Env, w *jobs.Worker) {}
