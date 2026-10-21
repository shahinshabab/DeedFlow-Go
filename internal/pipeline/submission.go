package pipeline

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// Submission time budget. A portal run on a slow day needs longer than PDF
// processing; the run stops itself at the deadline, inside both limits, so it
// always records how it ended instead of being killed mid-save.
const (
	SubmissionTimeLimit     = 60 * time.Minute
	SubmissionSoftTimeLimit = 55 * time.Minute
	SubmissionDeadline      = 50 * time.Minute
	StaleSubmission         = SubmissionTimeLimit + 5*time.Minute
	BrowserWaitRetry        = 20 * time.Second
	browserLockName         = "deedflow:submit:browser"
)

func lockKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64() & 0x7FFFFFFFFFFFFFFF)
}

// BrowserLockKey serialises the portal browser: one Chrome session at a time.
func BrowserLockKey() int64 { return lockKey(browserLockName) }

// DeedLockKey is held while a deed is being submitted, so a redelivered job
// can't write to the same token twice.
func DeedLockKey(id int64) int64 { return lockKey(fmt.Sprintf("deedflow:submit:deed:%d", id)) }

// WithResumeHint adds the "submitting again updates the same token" note.
func WithResumeHint(message string, d *models.Deed) string {
	if d.TokenNumber == "" {
		return message
	}
	return fmt.Sprintf("%s Token %s was already created - submitting again will update that same token "+
		"(the unfinished sections are deleted there and entered fresh), not start a new one.", message, d.TokenNumber)
}

// SubmissionIsRunning reports whether some worker holds the deed's lock. When
// it can't tell, it says yes: never declare a live run dead.
func SubmissionIsRunning(ctx context.Context, d *db.DB, deedID int64) bool {
	locked, err := d.IsLocked(ctx, DeedLockKey(deedID))
	return err != nil || locked
}

// ReapStaleSubmission marks a deed whose submission was cut off as failed, so
// it can be submitted again (which resumes its token) instead of showing
// "Submitting" forever. It reports whether it did.
func ReapStaleSubmission(ctx context.Context, st *store.Store, d *models.Deed) bool {
	if d.Status != models.StatusSubmitting {
		return false
	}
	if time.Since(d.UpdatedAt) < StaleSubmission {
		return false
	}
	if SubmissionIsRunning(ctx, st.DB, d.ID) {
		return false
	}
	message := WithResumeHint("The submission was interrupted before it finished (the server restarted "+
		"or the run was cut off).", d)
	changed, err := st.MarkStaleSubmission(ctx, d.ID, message)
	if err != nil || !changed {
		return false
	}
	slog.Warn("Marked stale submission of deed as failed", "deed", d.PublicID)
	if fresh, err := st.DeedByID(ctx, d.ID); err == nil && fresh != nil {
		*d = *fresh
	}
	return true
}
