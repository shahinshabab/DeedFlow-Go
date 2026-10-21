package webdriver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Poll is how often Until re-checks (WebDriverWait's default).
const Poll = 500 * time.Millisecond

// Until polls fn until it reports done or timeout passes (ErrTimeout). Like
// WebDriverWait it ignores "no such element" between polls; any other error
// ends the wait. The last ignored error is kept as the timeout's message.
func Until(ctx context.Context, timeout time.Duration, fn func(ctx context.Context) (bool, error)) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		done, err := fn(ctx)
		switch {
		case err == nil && done:
			return nil
		case err != nil && !errors.Is(err, ErrNoSuchElement):
			return err
		case err != nil:
			last = err
		}
		if time.Now().After(deadline) {
			msg := ""
			if last != nil {
				msg = last.Error()
			}
			return &Error{Kind: ErrTimeout, Code: "timeout", Message: msg}
		}
		select {
		case <-time.After(Poll):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// WaitElement polls for an element satisfying cond (presence when cond is nil).
func (d *Driver) WaitElement(ctx context.Context, timeout time.Duration, by By, cond func(context.Context, *Element) (bool, error)) (*Element, error) {
	var found *Element
	err := Until(ctx, timeout, func(ctx context.Context) (bool, error) {
		el, err := d.FindElement(ctx, by)
		if err != nil {
			return false, err
		}
		if cond != nil {
			ok, err := cond(ctx, el)
			if errors.Is(err, ErrStaleElement) {
				return false, nil
			}
			if err != nil || !ok {
				return false, err
			}
		}
		found = el
		return true, nil
	})
	return found, err
}

// Present is expected_conditions.presence_of_element_located.
func (d *Driver) Present(ctx context.Context, timeout time.Duration, by By) (*Element, error) {
	return d.WaitElement(ctx, timeout, by, nil)
}

// Visible is expected_conditions.visibility_of_element_located.
func (d *Driver) Visible(ctx context.Context, timeout time.Duration, by By) (*Element, error) {
	return d.WaitElement(ctx, timeout, by, func(ctx context.Context, e *Element) (bool, error) { return e.IsDisplayed(ctx) })
}

// Clickable is expected_conditions.element_to_be_clickable.
func (d *Driver) Clickable(ctx context.Context, timeout time.Duration, by By) (*Element, error) {
	return d.WaitElement(ctx, timeout, by, func(ctx context.Context, e *Element) (bool, error) {
		shown, err := e.IsDisplayed(ctx)
		if err != nil || !shown {
			return false, err
		}
		return e.IsEnabled(ctx)
	})
}

// IsStale is expected_conditions.staleness_of's check.
func (e *Element) IsStale(ctx context.Context) bool {
	_, err := e.IsEnabled(ctx)
	return errors.Is(err, ErrStaleElement)
}

// Exists reports whether by matches anything now.
func (d *Driver) Exists(ctx context.Context, by By) bool {
	els, err := d.FindElements(ctx, by)
	return err == nil && len(els) > 0
}

// DocumentComplete waits for document.readyState == "complete".
func (d *Driver) DocumentComplete(ctx context.Context, timeout time.Duration) error {
	return Until(ctx, timeout, func(ctx context.Context) (bool, error) {
		v, err := d.ExecuteScript(ctx, "return document.readyState;")
		if err != nil {
			return false, err
		}
		return v == "complete", nil
	})
}

// --- <select> helper (selenium.webdriver.support.ui.Select) ----------------------------

// Select wraps a <select> element.
type Select struct{ El *Element }

// Options lists the option elements.
func (s Select) Options(ctx context.Context) ([]*Element, error) {
	return s.El.FindElements(ctx, ByTag("option"))
}

func xpathString(s string) string {
	if !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	parts := strings.Split(s, `"`)
	return `concat("` + strings.Join(parts, `", '"', "`) + `")`
}

func (s Select) pick(ctx context.Context, opt *Element) error {
	sel, err := opt.IsSelected(ctx)
	if err != nil || sel {
		return err
	}
	en, err := opt.IsEnabled(ctx)
	if err != nil {
		return err
	}
	if !en {
		return &Error{Code: "not implemented", Message: "You may not select a disabled option"}
	}
	return opt.Click(ctx)
}

func longestToken(s string) string {
	best := ""
	for _, t := range strings.Fields(s) {
		if len(t) > len(best) {
			best = t
		}
	}
	return best
}

// SelectByVisibleText picks the option showing exactly text.
func (s Select) SelectByVisibleText(ctx context.Context, text string) error {
	opts, err := s.El.FindElements(ctx, ByXPath(".//option[normalize-space(.) = "+xpathString(text)+"]"))
	if err != nil {
		return err
	}
	if len(opts) > 0 {
		return s.pick(ctx, opts[0])
	}
	if strings.Contains(text, " ") {
		token := longestToken(text)
		var cands []*Element
		if token == "" {
			cands, err = s.Options(ctx)
		} else {
			cands, err = s.El.FindElements(ctx, ByXPath(".//option[contains(.,"+xpathString(token)+")]"))
		}
		if err != nil {
			return err
		}
		for _, c := range cands {
			t, err := c.Text(ctx)
			if err != nil {
				return err
			}
			if t == text {
				return s.pick(ctx, c)
			}
		}
	}
	return &Error{Kind: ErrNoSuchElement, Code: "no such element", Message: fmt.Sprintf("Could not locate element with visible text: %s", text)}
}

// SelectByValue picks the option with that value attribute.
func (s Select) SelectByValue(ctx context.Context, value string) error {
	opts, err := s.El.FindElements(ctx, ByXPath(".//option[@value = "+xpathString(value)+"]"))
	if err != nil {
		return err
	}
	if len(opts) == 0 {
		return &Error{Kind: ErrNoSuchElement, Code: "no such element", Message: fmt.Sprintf("Cannot locate option with value: %s", value)}
	}
	return s.pick(ctx, opts[0])
}
