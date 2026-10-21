package cli

import (
	"context"

	"github.com/shahinshabab/deedflow-go/internal/ai"
	"github.com/shahinshabab/deedflow-go/internal/jobs"
	"github.com/shahinshabab/deedflow-go/internal/ledger"
	"github.com/shahinshabab/deedflow-go/internal/mail"
	"github.com/shahinshabab/deedflow-go/internal/pipeline"
	"github.com/shahinshabab/deedflow-go/internal/web"
)

func (e *Env) pipelineEnv() *pipeline.Env {
	return &pipeline.Env{Cfg: e.Cfg, Store: e.Store, Hub: e.Hub, AI: ai.New(e.Cfg), Loc: e.Cfg.TimeZone}
}

func (e *Env) ledgerService() (*ledger.Service, error) {
	render, err := web.NewRenderer(e.Cfg)
	if err != nil {
		return nil, err
	}
	return &ledger.Service{Cfg: e.Cfg, Store: e.Store, Mail: mail.New(e.Cfg), Render: render, Log: e.Log}, nil
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
		svc, err := e.ledgerService()
		if err != nil {
			e.Log.Error("receipts collection is unavailable", "err", err)
			return
		}
		w.Handlers[jobs.KindCollectReceipts] = func(ctx context.Context, j jobs.Job) error {
			var args struct {
				OrgID   int64 `json:"org_id"`
				Attempt int   `json:"attempt"`
			}
			if err := j.Decode(&args); err != nil {
				return err
			}
			return svc.CollectOrgReceipts(ctx, args.OrgID, args.Attempt, j.Attempts)
		}
		w.Handlers[jobs.KindNightlyReceipts] = func(ctx context.Context, j jobs.Job) error {
			var args struct {
				Attempt int `json:"attempt"`
			}
			if err := j.Decode(&args); err != nil {
				return err
			}
			return svc.NightlyReceipts(ctx, e.Queue, args.Attempt)
		}
		w.Schedulers = append(w.Schedulers, func(ctx context.Context) { svc.RunScheduler(ctx, e.Queue) })
	}
}
