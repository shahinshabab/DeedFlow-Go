package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/mail"
	"github.com/shahinshabab/deedflow-go/internal/models"
)

func lockKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64() & 0x7FFFFFFFFFFFFFFF)
}

func marshalFields(fields [][2]string) (json.RawMessage, error) { return json.Marshal(fields) }

// AlertRecipient is who hears about pending receipts: the alert address, else
// the organization admin's email.
func (s *Service) AlertRecipient(ctx context.Context, cred *models.PortalCredential, org *models.Organization) string {
	if cred.AlertEmail != "" {
		return cred.AlertEmail
	}
	admin, err := s.Store.OrganizationAdmin(ctx, org.ID)
	if err == nil && admin != nil && admin.Email != "" {
		return admin.Email
	}
	return ""
}

// finish emails the alert after the last nightly attempt, if anything is still
// pending (or the run failed outright).
func (s *Service) finish(ctx context.Context, run *models.ReceiptSyncRun, cred *models.PortalCredential, org *models.Organization) {
	if int(run.Attempt) != FinalAttempt || run.Status == models.RunDone {
		return
	}
	recipient := s.AlertRecipient(ctx, cred, org)
	if recipient == "" {
		s.log().Warn("Receipts are still pending but there's no alert email to tell.", "org", org.Name)
		return
	}
	// A broken mail setup mustn't fail the run.
	msg, err := s.ReceiptsAlert(ctx, run, org)
	if err == nil {
		msg.To = []string{recipient}
		err = s.Mail.Send(ctx, msg)
	}
	if err != nil {
		s.log().Error("Could not email the receipts alert", "to", recipient, "err", err)
		return
	}
	run.AlertSentTo = recipient
	if err := s.Store.SaveReceiptRun(ctx, run, "alert_sent_to"); err != nil {
		s.log().Error("could not record the alert", "err", err)
	}
}

// ReceiptsAlert builds the "receipts still pending" email for a run, as plain
// text plus HTML.
func (s *Service) ReceiptsAlert(ctx context.Context, run *models.ReceiptSyncRun, org *models.Organization) (*mail.Message, error) {
	pendingCount, err := s.Store.CountNotCollected(ctx, org.ID)
	if err != nil {
		return nil, err
	}
	pending, err := s.Store.NotCollectedTokens(ctx, org.ID, AlertMaxTokens)
	if err != nil {
		return nil, err
	}
	failed := run.Status == models.RunFailed
	since := s.TonightStart()
	collected, err := s.Store.CountCollectedSince(ctx, org.ID, since)
	if err != nil {
		return nil, err
	}
	var subject string
	if failed {
		subject = fmt.Sprintf("DeedFlow: receipt collection stopped for %s", org.Name)
	} else {
		plural := "s"
		if pendingCount == 1 {
			plural = ""
		}
		subject = fmt.Sprintf("DeedFlow: %d payment receipt%s pending for %s", pendingCount, plural, org.Name)
	}
	rows := make([]map[string]any, 0, len(pending))
	for _, t := range pending {
		rows = append(rows, map[string]any{"token": t.Token, "presentation_date": t.PresentationDate,
			"name": t.Name(), "last_error": t.LastError})
	}
	night := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, time.UTC)
	accountsURL := ""
	if s.Cfg.SiteURL != "" {
		accountsURL = s.Cfg.SiteURL + "/accounts/"
	}
	data := map[string]any{
		"subject": subject, "organization": org.Name,
		"run":    map[string]any{"message": run.Message, "registered_found": run.RegisteredFound},
		"failed": failed, "night": night, "pending": rows, "pending_count": pendingCount,
		"more": max(pendingCount-AlertMaxTokens, 0), "collected_tonight": collected, "accounts_url": accountsURL,
	}
	text, err := s.Render("ledger/email/receipts_alert.txt", data)
	if err != nil {
		return nil, err
	}
	html, err := s.Render("ledger/email/receipts_alert.html", data)
	if err != nil {
		return nil, err
	}
	return &mail.Message{From: s.Cfg.DefaultFromEmail, Subject: subject, Text: text, HTML: html}, nil
}
