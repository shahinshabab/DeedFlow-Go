// Package ledger is the Accounts section's background work: the nightly
// collection of payment receipts from the portal into the database, and the
// "receipts still pending" email alert. It is the Go counterpart of
// ledger/tasks.py.
//
// The scheduler queues nightly_receipts at 9 PM, 10 PM and 11 PM India time.
// The 9 PM run collects every registered token that isn't collected yet;
// 10 PM and 11 PM run again only if something is still pending; after the
// 11 PM run anything still pending is emailed to the organization's alert
// address.
package ledger

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/config"
	"github.com/shahinshabab/deedflow-go/internal/jobs"
	"github.com/shahinshabab/deedflow-go/internal/mail"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/pipeline"
	"github.com/shahinshabab/deedflow-go/internal/portal"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

const (
	RunTimeLimit = 58 * time.Minute
	RunDeadline  = 50 * time.Minute
	// While a deed submission holds the browser: try again this often, for at
	// most this many times (the next attempt is an hour later anyway).
	BrowserWaitRetry      = 60 * time.Second
	BrowserWaitMaxRetries = 45
	TokenAttemptsPerRun   = 2
	FinalAttempt          = 3
	// The alert email lists at most this many pending tokens.
	AlertMaxTokens = 50
)

// Session is what a collection run needs from the portal: the real thing
// drives a browser, tests supply a stand-in.
type Session interface {
	RegisteredTokens() ([]portal.RegisteredRow, error)
	TokenReceipts(token string) (*portal.TokenReceipts, error)
	Logout() error
	Close()
}

// Service runs the nightly collection.
type Service struct {
	Cfg    *config.Config
	Store  *store.Store
	Mail   mail.Sender
	Render func(name string, data map[string]any) (string, error)
	// Open starts a portal session; nil means a real browser.
	Open func(ctx context.Context, cred *models.PortalCredential, deadline time.Time) (Session, error)
	Log  *slog.Logger
}

type browserSession struct {
	bot     *portal.Automation
	scraper *portal.ReceiptScraper
}

func (b *browserSession) RegisteredTokens() ([]portal.RegisteredRow, error) {
	return b.scraper.RegisteredTokens()
}
func (b *browserSession) TokenReceipts(token string) (*portal.TokenReceipts, error) {
	return b.scraper.TokenReceipts(token)
}
func (b *browserSession) Logout() error { return b.scraper.Logout() }
func (b *browserSession) Close()        { b.bot.Close() }

func (s *Service) open(ctx context.Context, cred *models.PortalCredential, deadline time.Time) (Session, error) {
	if s.Open != nil {
		return s.Open(ctx, cred, deadline)
	}
	bot, err := pipeline.OpenPortal(ctx, s.Cfg, cred, deadline)
	if err != nil {
		return nil, err
	}
	return &browserSession{bot: bot, scraper: portal.NewReceiptScraper(bot, deadline, s.Cfg.TimeZone)}, nil
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// TonightStart is the start of tonight's collection window (a little before
// the 9 PM run).
func (s *Service) TonightStart() time.Time {
	now := time.Now().In(s.Cfg.TimeZone)
	return time.Date(now.Year(), now.Month(), now.Day(), 20, 30, 0, 0, s.Cfg.TimeZone)
}

// NightlyReceipts queues a collection for every organization that has it on.
func (s *Service) NightlyReceipts(ctx context.Context, q *jobs.Queue, attempt int) error {
	creds, err := s.Store.CredentialsCollectingReceipts(ctx)
	if err != nil {
		return err
	}
	for _, c := range creds {
		if _, err := q.Enqueue(ctx, jobs.KindCollectReceipts, map[string]any{"org_id": c.OrganizationID, "attempt": attempt}, time.Time{}); err != nil {
			return err
		}
	}
	return nil
}

// CollectOrgReceipts is the job for one organization. attempt: 1/2/3 = the
// 9/10/11 PM runs, 0 = started by hand. tries is how many times this job has
// been claimed (a busy browser makes it come back).
func (s *Service) CollectOrgReceipts(ctx context.Context, orgID int64, attempt int, tries int) error {
	ctx, cancel := context.WithTimeout(ctx, RunTimeLimit)
	defer cancel()
	cred, err := s.Store.PortalCredentialFor(ctx, orgID)
	if err != nil {
		return err
	}
	if cred == nil || cred.PortalUsername == "" {
		return nil
	}
	org, err := s.Store.OrganizationByID(ctx, orgID)
	if err != nil || org == nil {
		return err
	}
	if attempt > 1 {
		again, err := s.NeedsAnotherRun(ctx, orgID)
		if err != nil {
			return err
		}
		if !again {
			s.log().Info("Receipts are all collected - skipping this attempt", "org", org.Name, "attempt", attempt)
			return nil
		}
	}
	orgLock, ok, err := s.Store.DB.TryLock(ctx, orgLockKey(orgID))
	if err != nil {
		return err
	}
	if !ok {
		s.log().Info("A receipt run is already going - skipping this attempt", "org", org.Name, "attempt", attempt)
		return nil
	}
	defer orgLock.Release()
	browserLock, ok, err := s.Store.DB.TryLock(ctx, pipeline.BrowserLockKey())
	if err != nil {
		return err
	}
	if !ok {
		if tries <= BrowserWaitMaxRetries {
			return jobs.RetryLater{After: BrowserWaitRetry}
		}
		run := &models.ReceiptSyncRun{OrganizationID: orgID, Attempt: int16(attempt), Status: models.RunFailed,
			Message: "The browser was busy with deed submissions the whole time."}
		now := time.Now()
		run.FinishedAt = &now
		if err := s.Store.CreateReceiptRun(ctx, run); err != nil {
			return err
		}
		s.finish(ctx, run, cred, org)
		return nil
	}
	defer browserLock.Release()
	_, err = s.RunCollection(ctx, cred, org, attempt)
	return err
}

func orgLockKey(orgID int64) int64 { return lockKey(fmt.Sprintf("deedflow:ledger:org:%d", orgID)) }

// NeedsAnotherRun is true if tonight's runs haven't collected everything: no
// successful run since 9 PM, or tokens still pending.
func (s *Service) NeedsAnotherRun(ctx context.Context, orgID int64) (bool, error) {
	pending, err := s.Store.CountNotCollected(ctx, orgID)
	if err != nil {
		return false, err
	}
	if pending > 0 {
		return true, nil
	}
	done, err := s.Store.HasDoneRunSince(ctx, orgID, s.TonightStart())
	return !done, err
}

// RunCollection records one run: the registered tokens My Token shows, then
// the receipts of every token not collected yet.
func (s *Service) RunCollection(ctx context.Context, cred *models.PortalCredential, org *models.Organization, attempt int) (*models.ReceiptSyncRun, error) {
	run := &models.ReceiptSyncRun{OrganizationID: org.ID, Attempt: int16(attempt)}
	if err := s.Store.CreateReceiptRun(ctx, run); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(RunDeadline)
	runErr := func() error {
		session, err := s.open(ctx, cred, deadline)
		if err != nil {
			return err
		}
		defer session.Close()
		registered, err := session.RegisteredTokens()
		if err != nil {
			return err
		}
		run.RegisteredFound = int32(len(registered))
		for _, row := range registered {
			if err := s.Store.RecordRegistered(ctx, org.ID, row.Token, row.Name, row.Date); err != nil {
				return err
			}
		}
		_ = session.Logout()

		todo, err := s.Store.NotCollectedTokens(ctx, org.ID, 0)
		if err != nil {
			return err
		}
		for i := range todo {
			if err := s.collectToken(ctx, session, &todo[i]); err != nil {
				return err
			}
			if todo[i].Status == models.TokenCollected {
				run.Collected++
			}
		}
		_ = session.Logout()
		return nil
	}()
	if runErr != nil {
		s.log().Error("Receipt run failed", "org", org.Name, "err", runErr)
		run.Message = portal.Short(runErr)
		run.Status = models.RunFailed
	}
	// The run's own context may be what ended; settle the record anyway.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	pending, err := s.Store.CountNotCollected(sctx, org.ID)
	if err != nil {
		return run, err
	}
	run.Pending = int32(pending)
	if run.Status != models.RunFailed {
		if pending > 0 {
			run.Status = models.RunPartial
		} else {
			run.Status = models.RunDone
		}
	}
	finished := time.Now()
	run.FinishedAt = &finished
	if err := s.Store.SaveReceiptRun(sctx, run); err != nil {
		return run, err
	}
	s.finish(sctx, run, cred, org)
	return run, nil
}

// collectToken reads one token's receipts, trying twice. A failure that is
// the token's own (not found, nothing to show) is kept on the token; running
// out of time ends the whole run.
func (s *Service) collectToken(ctx context.Context, session Session, item *models.RegisteredToken) error {
	last := ""
	for i := 0; i < TokenAttemptsPerRun; i++ {
		item.Attempts++
		data, err := session.TokenReceipts(item.Token)
		if err != nil {
			if portal.IsReceiptsUnavailable(err) {
				last = err.Error()
				break
			}
			if portal.IsPortalError(err) && strings.Contains(err.Error(), "out of time") {
				_ = s.Store.SaveRegisteredToken(ctx, item, "attempts")
				return err
			}
			last = portal.Short(err)
			s.log().Warn("Receipts of a token could not be read", "token", item.Token, "err", last)
			continue
		}
		return s.saveReceipts(ctx, item, data)
	}
	item.Status = models.TokenFailed
	if r := []rune(last); len(r) > 2000 {
		last = string(r[:2000])
	}
	item.LastError = last
	return s.Store.SaveRegisteredToken(ctx, item, "status", "last_error", "attempts")
}

func (s *Service) saveReceipts(ctx context.Context, item *models.RegisteredToken, data *portal.TokenReceipts) error {
	return s.Store.InTx(ctx, func(tx *store.Store) error {
		p := data.Presenter
		if p.Place != "" {
			item.PresenterPlaceML = p.Place
		}
		if p.Address != "" {
			item.PresenterAddrML = p.Address
		}
		if p.City != "" {
			item.PresenterCityML = p.City
		}
		if item.PresenterNameML == "" {
			item.PresenterNameML = p.Name
		}
		if p.PresentedAt != nil {
			item.PresentedAt = p.PresentedAt
		}
		if p.DocumentAmount != nil {
			item.DocumentAmount = p.DocumentAmount
		}
		if p.SRO != "" {
			item.SRO = p.SRO
		}
		for _, group := range []struct {
			kind     string
			receipts []portal.Receipt
		}{{models.KindStampDuty, data.Stamp}, {models.KindRegistrationFee, data.Fee}} {
			for _, r := range group.receipts {
				fields := r.Fields
				if fields == nil {
					fields = [][2]string{}
				}
				raw, err := marshalFields(fields)
				if err != nil {
					return err
				}
				if err := tx.UpsertReceipt(ctx, &models.PaymentReceipt{
					TokenID: item.ID, Kind: group.kind, TransactionNo: r.TransactionNo, ApplicantName: r.ApplicantName,
					SRO: r.SRO, GRN: r.GRN, Amount: r.Amount, PaymentMode: r.PaymentMode, BankReference: r.BankReference,
					BankCIN: r.BankCIN, PaidAt: r.PaidAt, Status: r.Status, Fields: raw}); err != nil {
					return err
				}
				if item.ApplicantName == "" {
					item.ApplicantName = r.ApplicantName
				}
				if item.SRO == "" {
					item.SRO = r.SRO
				}
			}
		}
		item.Status = models.TokenCollected
		item.LastError = ""
		now := time.Now()
		item.CollectedAt = &now
		return tx.SaveRegisteredToken(ctx, item)
	})
}
