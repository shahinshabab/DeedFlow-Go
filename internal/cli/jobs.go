package cli

import (
	"context"

	"github.com/shahinshabab/deedflow-go/internal/ai"
	"github.com/shahinshabab/deedflow-go/internal/jobs"
	"github.com/shahinshabab/deedflow-go/internal/pipeline"
)

func (e *Env) pipelineEnv() *pipeline.Env {
	return &pipeline.Env{Cfg: e.Cfg, Store: e.Store, Hub: e.Hub, AI: ai.New(e.Cfg), Loc: e.Cfg.TimeZone}
}

func init() {
	prev := registerJobs
	registerJobs = func(e *Env, w *jobs.Worker) {
		prev(e, w)
		p := e.pipelineEnv()
		w.Handlers[jobs.KindProcessPDF] = func(ctx context.Context, j jobs.Job) error {
			var args struct {
				DeedID int64 `json:"deed_id"`
			}
			if err := j.Decode(&args); err != nil {
				return err
			}
			return p.ProcessDeedPDF(ctx, args.DeedID)
		}
		w.Handlers[jobs.KindSubmitDeed] = func(ctx context.Context, j jobs.Job) error {
			var args struct {
				DeedID int64 `json:"deed_id"`
			}
			if err := j.Decode(&args); err != nil {
				return err
			}
			return p.SubmitDeedToPortal(ctx, args.DeedID)
		}
	}
}
