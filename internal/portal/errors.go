// Package portal drives the Registration Department portal
// (pearl.registration.kerala.gov.in) with a WebDriver browser to submit a
// deed: logging in, solving the captcha and filling every section of the
// online form. It is the Go counterpart of deeds/utils/selenium_automation.py.
//
// The automation is written the way the Python was: a step that fails raises,
// and the layer that knows what to do (retry, recover the session, report)
// catches. Go has no exceptions, so failures are panics carrying an error
// value, and try() is the except clause.
package portal

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shahinshabab/deedflow-go/internal/webdriver"
)

// Error is PortalAutomationError: the automation can't proceed at all (login
// failure, token couldn't be created/opened, too many lost sessions).
// Section-level failures don't raise - they're recorded and the run moves on.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func errorf(format string, args ...any) *Error { return &Error{Msg: fmt.Sprintf(format, args...)} }

// TokenUnavailableError: the token isn't in this login's My Token, or can't
// be edited any more - nothing was touched, and trying again won't change that.
type TokenUnavailableError struct{ Msg string }

func (e *TokenUnavailableError) Error() string { return e.Msg }

// SessionLostError: the browser died or the portal logged us out mid-run.
type SessionLostError struct{ Msg string }

func (e *SessionLostError) Error() string { return e.Msg }

// ReceiptsUnavailable: the portal answered, but the token's payment details
// can't be shown (not found, a message instead of the tabs...). Not worth
// retrying tonight with a fresh login; reported on the token.
type ReceiptsUnavailable struct{ Msg string }

func (e *ReceiptsUnavailable) Error() string { return e.Msg }

// fillError is raised inside a step on any failure. retryable=false marks a
// failure that repeating with the same data can't fix (an explicit portal
// validation rejection, a value that isn't one of a dropdown's options).
type fillError struct {
	msg       string
	retryable bool
}

func (e *fillError) Error() string { return e.msg }

// newFillError builds a fillError; the portal's own hiccups ("Error while
// saving. Please try again.") are never a verdict on the data.
func newFillError(msg string, retryable bool) *fillError {
	return &fillError{msg: msg, retryable: retryable || transient([]string{msg})}
}

func fillErrorf(retryable bool, format string, args ...any) *fillError {
	return newFillError(fmt.Sprintf(format, args...), retryable)
}

// try runs fn and returns the error it panicked with, as Python's except does.
// Runtime errors (bugs) keep propagating.
func try(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok && isRaised(e) {
				err = e
				return
			}
			panic(r)
		}
	}()
	fn()
	return nil
}

// tryv is try for a function with a result.
func tryv[T any](fn func() T) (v T, err error) {
	err = try(func() { v = fn() })
	return v, err
}

// isRaised reports whether an error is one the automation raises (as opposed
// to a Go runtime error, which is a bug and must not be swallowed).
func isRaised(e error) bool {
	var (
		wd *webdriver.Error
		pe *Error
		tu *TokenUnavailableError
		sl *SessionLostError
		fe *fillError
		ru *ReceiptsUnavailable
	)
	return errors.As(e, &wd) || errors.As(e, &pe) || errors.As(e, &tu) || errors.As(e, &sl) || errors.As(e, &fe) || errors.As(e, &ru) ||
		errors.Is(e, errCanceled)
}

var errCanceled = errors.New("the submission was cancelled")

// must raises err if there is one.
func must(err error) {
	if err != nil {
		raise(err)
	}
}

// mustv raises err or returns v.
func mustv[T any](v T, err error) T {
	if err != nil {
		raise(err)
	}
	return v
}

// raise panics with err; a cancelled context becomes errCanceled so it is
// never mistaken for a portal failure.
func raise(err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		panic(errCanceled)
	}
	panic(err)
}

func isWD(err error) bool {
	var wd *webdriver.Error
	return errors.As(err, &wd)
}

func is(err error, target error) bool { return errors.Is(err, target) }

var (
	isNoSuch     = func(err error) bool { return errors.Is(err, webdriver.ErrNoSuchElement) }
	isStale      = func(err error) bool { return errors.Is(err, webdriver.ErrStaleElement) }
	isUnexpAlert = func(err error) bool { return errors.Is(err, webdriver.ErrUnexpectedAlert) }
	isTimeout    = func(err error) bool { return errors.Is(err, webdriver.ErrTimeout) }
	isNotInter   = func(err error) bool { return errors.Is(err, webdriver.ErrNotInteractable) }
)

// short is one readable line for an error that ends up in the deed's result
// message: Selenium's own text carries a multi-line native stack trace, and a
// TimeoutException's is empty.
func short(err error) string {
	if err == nil {
		return ""
	}
	var wd *webdriver.Error
	if errors.As(err, &wd) {
		if errors.Is(err, webdriver.ErrTimeout) && strings.TrimSpace(wd.Message) == "" {
			return "timed out waiting for the portal page"
		}
		name := exceptionName(wd)
		first := strings.Split(strings.TrimSpace(wd.Message), "\n")[0]
		if strings.TrimSpace(wd.Message) == "" {
			return name
		}
		if len(first) > 200 {
			first = first[:200]
		}
		return name + ": " + first
	}
	return err.Error()
}

func exceptionName(e *webdriver.Error) string {
	switch {
	case errors.Is(e, webdriver.ErrNoSuchElement):
		return "NoSuchElementException"
	case errors.Is(e, webdriver.ErrStaleElement):
		return "StaleElementReferenceException"
	case errors.Is(e, webdriver.ErrTimeout):
		return "TimeoutException"
	case errors.Is(e, webdriver.ErrUnexpectedAlert):
		return "UnexpectedAlertPresentException"
	case errors.Is(e, webdriver.ErrNotInteractable):
		return "ElementNotInteractableException"
	case errors.Is(e, webdriver.ErrNoAlert):
		return "NoAlertPresentException"
	case errors.Is(e, webdriver.ErrInvalidSession):
		return "InvalidSessionIdException"
	}
	return "WebDriverException"
}

func asErr[T error](err error, target *T) bool { return errors.As(err, target) }

func errors_Is(err, target error) bool { return errors.Is(err, target) }

// IsPortalError is `except PortalAutomationError` (TokenUnavailableError included).
func IsPortalError(err error) bool { return isPortalErr(err) }

// IsTokenUnavailable reports a token that isn't there or can't be edited.
func IsTokenUnavailable(err error) bool {
	var tu *TokenUnavailableError
	return errors.As(err, &tu)
}

// IsSessionLost reports a dropped browser session / portal logout.
func IsSessionLost(err error) bool {
	var sl *SessionLostError
	return errors.As(err, &sl)
}

// NewError builds a PortalAutomationError.
func NewError(format string, args ...any) error { return errorf(format, args...) }

// Short is one readable line for an error that ends up in a deed's result message.
func Short(err error) string { return short(err) }
