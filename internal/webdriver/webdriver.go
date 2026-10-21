// Package webdriver is a small W3C WebDriver client: just what the portal
// automation uses of Selenium (remote sessions, element lookups, scripts,
// alerts, screenshots), speaking to a Selenium Grid or a chromedriver.
package webdriver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// elementKey is the W3C identifier of a web element in JSON.
const elementKey = "element-6066-11e4-a52e-4f735466cecf"

// Error kinds, as Selenium's exception classes.
var (
	ErrNoSuchElement         = errors.New("no such element")
	ErrStaleElement          = errors.New("stale element reference")
	ErrNoAlert               = errors.New("no such alert")
	ErrNotInteractable       = errors.New("element not interactable")
	ErrTimeout               = errors.New("timeout")
	ErrInvalidSession        = errors.New("invalid session id")
	ErrUnexpectedAlert       = errors.New("unexpected alert open")
	ErrScriptTimeout         = errors.New("script timeout")
	ErrJavascript            = errors.New("javascript error")
	ErrElementClickIntercept = errors.New("element click intercepted")
	ErrNoSuchWindow          = errors.New("no such window")
)

// Error is a failure the driver reported.
type Error struct {
	Kind    error  // one of the Err* values, or nil for other failures
	Code    string // the W3C error code
	Message string
	// AlertText is the open dialog's text for an unexpected-alert error.
	AlertText string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Unwrap lets errors.Is match the Err* kinds.
func (e *Error) Unwrap() error { return e.Kind }

var kinds = map[string]error{
	"no such element":           ErrNoSuchElement,
	"stale element reference":   ErrStaleElement,
	"no such alert":             ErrNoAlert,
	"element not interactable":  ErrNotInteractable,
	"invalid element state":     ErrNotInteractable,
	"timeout":                   ErrTimeout,
	"script timeout":            ErrScriptTimeout,
	"invalid session id":        ErrInvalidSession,
	"unexpected alert open":     ErrUnexpectedAlert,
	"javascript error":          ErrJavascript,
	"element click intercepted": ErrElementClickIntercept,
	"no such window":            ErrNoSuchWindow,
	"no such frame":             ErrNoSuchWindow,
}

// By locates elements.
type By struct{ using, value string }

// ByID finds by id attribute.
func ByID(id string) By { return By{"css selector", `[id="` + id + `"]`} }

// ByName finds by name attribute.
func ByName(name string) By { return By{"css selector", `[name="` + name + `"]`} }

// ByCSS finds by CSS selector.
func ByCSS(sel string) By { return By{"css selector", sel} }

// ByXPath finds by XPath.
func ByXPath(x string) By { return By{"xpath", x} }

// ByTag finds by tag name.
func ByTag(tag string) By { return By{"css selector", tag} }

// Driver is a browser session.
type Driver struct {
	base    string
	session string
	client  *http.Client
}

// Options start a session.
type Options struct {
	Browser string   // "chrome"
	Args    []string // browser command line flags
	Binary  string   // browser binary path (local chromedriver only)
	// UnhandledPrompt is the W3C unhandledPromptBehavior ("ignore" keeps
	// dialogs open for the caller to read and answer).
	UnhandledPrompt string
	PageLoadTimeout time.Duration
}

// NewRemote starts a session on the WebDriver server at baseURL (a Selenium
// Grid's .../wd/hub or a chromedriver's root).
func NewRemote(ctx context.Context, baseURL string, o Options) (*Driver, error) {
	d := &Driver{base: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: 10 * time.Minute}}
	if o.Browser == "" {
		o.Browser = "chrome"
	}
	chrome := map[string]any{"args": o.Args}
	if o.Binary != "" {
		chrome["binary"] = o.Binary
	}
	always := map[string]any{"browserName": o.Browser, "goog:chromeOptions": chrome}
	if o.UnhandledPrompt != "" {
		always["unhandledPromptBehavior"] = o.UnhandledPrompt
	}
	var resp struct {
		SessionID string `json:"sessionId"`
	}
	if err := d.call(ctx, http.MethodPost, "/session", map[string]any{"capabilities": map[string]any{"alwaysMatch": always}}, &resp); err != nil {
		return nil, err
	}
	d.session = resp.SessionID
	if o.PageLoadTimeout > 0 {
		if err := d.SetPageLoadTimeout(ctx, o.PageLoadTimeout); err != nil {
			d.Quit(ctx)
			return nil, err
		}
	}
	return d, nil
}

// call sends one command. out, if non-nil, receives the response "value".
func (d *Driver) call(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	} else if method == http.MethodPost {
		rdr = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, d.base+path, rdr)
	if err != nil {
		return err
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return &Error{Code: "connection", Message: err.Error()}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return &Error{Code: "connection", Message: err.Error()}
	}
	var envelope struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return &Error{Code: "bad response", Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, trunc(string(data), 200))}
	}
	if resp.StatusCode >= 400 {
		var ev struct {
			Error      string                `json:"error"`
			Message    string                `json:"message"`
			Data       struct{ Text string } `json:"data"`
			Stacktrace string                `json:"stacktrace"`
		}
		_ = json.Unmarshal(envelope.Value, &ev)
		e := &Error{Code: ev.Error, Message: ev.Message, Kind: kinds[ev.Error], AlertText: ev.Data.Text}
		if e.Code == "" {
			e.Code = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		// Older drivers report the dialog's text inside the message.
		if e.Kind == ErrUnexpectedAlert && e.AlertText == "" {
			if i := strings.Index(e.Message, "Alert text :"); i >= 0 {
				e.AlertText = strings.TrimSpace(strings.SplitN(e.Message[i+len("Alert text :"):], "\n", 2)[0])
			}
		}
		return e
	}
	if out != nil {
		if len(envelope.Value) > 0 && string(envelope.Value) != "null" {
			// New-session responses nest the value under sessionId.
			if err := json.Unmarshal(envelope.Value, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (d *Driver) path(p string) string { return "/session/" + d.session + p }

// Quit ends the session.
func (d *Driver) Quit(ctx context.Context) error {
	if d == nil || d.session == "" {
		return nil
	}
	err := d.call(ctx, http.MethodDelete, "/session/"+d.session, nil, nil)
	d.session = ""
	return err
}

// SetPageLoadTimeout limits how long a navigation may take.
func (d *Driver) SetPageLoadTimeout(ctx context.Context, t time.Duration) error {
	return d.call(ctx, http.MethodPost, d.path("/timeouts"), map[string]any{"pageLoad": t.Milliseconds()}, nil)
}

// SetScriptTimeout limits how long an async script may run.
func (d *Driver) SetScriptTimeout(ctx context.Context, t time.Duration) error {
	return d.call(ctx, http.MethodPost, d.path("/timeouts"), map[string]any{"script": t.Milliseconds()}, nil)
}

// Get navigates to url.
func (d *Driver) Get(ctx context.Context, url string) error {
	return d.call(ctx, http.MethodPost, d.path("/url"), map[string]any{"url": url}, nil)
}

// CurrentURL is the page's address.
func (d *Driver) CurrentURL(ctx context.Context) (string, error) {
	var s string
	err := d.call(ctx, http.MethodGet, d.path("/url"), nil, &s)
	return s, err
}

// Screenshot is the page as PNG.
func (d *Driver) Screenshot(ctx context.Context) ([]byte, error) {
	var s string
	if err := d.call(ctx, http.MethodGet, d.path("/screenshot"), nil, &s); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(s)
}

// AlertText reads the open dialog's text (ErrNoAlert if none).
func (d *Driver) AlertText(ctx context.Context) (string, error) {
	var s string
	err := d.call(ctx, http.MethodGet, d.path("/alert/text"), nil, &s)
	return s, err
}

// AlertAccept presses OK on the open dialog.
func (d *Driver) AlertAccept(ctx context.Context) error {
	return d.call(ctx, http.MethodPost, d.path("/alert/accept"), nil, nil)
}

// AlertDismiss presses Cancel on the open dialog.
func (d *Driver) AlertDismiss(ctx context.Context) error {
	return d.call(ctx, http.MethodPost, d.path("/alert/dismiss"), nil, nil)
}

// Element is a located page element.
type Element struct {
	d  *Driver
	id string
}

func encodeArgs(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		switch x := a.(type) {
		case *Element:
			out[i] = map[string]string{elementKey: x.id}
		case []string:
			list := make([]any, len(x))
			for j, v := range x {
				list[j] = v
			}
			out[i] = list
		default:
			out[i] = a
		}
	}
	return out
}

// decodeValue turns element references in a script result into *Element.
func (d *Driver) decodeValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if id, ok := x[elementKey].(string); ok {
			return &Element{d: d, id: id}
		}
		for k, e := range x {
			x[k] = d.decodeValue(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = d.decodeValue(e)
		}
		return x
	}
	return v
}

// ExecuteScript runs JavaScript in the page; *Element arguments are passed as
// elements, and element results come back as *Element.
func (d *Driver) ExecuteScript(ctx context.Context, script string, args ...any) (any, error) {
	var out any
	err := d.call(ctx, http.MethodPost, d.path("/execute/sync"), map[string]any{"script": script, "args": encodeArgs(args)}, &out)
	return d.decodeValue(out), err
}

// ExecuteAsyncScript runs JavaScript that reports through its last argument,
// a callback.
func (d *Driver) ExecuteAsyncScript(ctx context.Context, script string, args ...any) (any, error) {
	var out any
	err := d.call(ctx, http.MethodPost, d.path("/execute/async"), map[string]any{"script": script, "args": encodeArgs(args)}, &out)
	return d.decodeValue(out), err
}

// FindElement locates one element (ErrNoSuchElement if absent).
func (d *Driver) FindElement(ctx context.Context, by By) (*Element, error) {
	return d.find(ctx, d.path("/element"), by)
}

// FindElements locates every match (an empty list if none).
func (d *Driver) FindElements(ctx context.Context, by By) ([]*Element, error) {
	return d.findAll(ctx, d.path("/elements"), by)
}

func (d *Driver) find(ctx context.Context, p string, by By) (*Element, error) {
	var m map[string]string
	if err := d.call(ctx, http.MethodPost, p, map[string]any{"using": by.using, "value": by.value}, &m); err != nil {
		return nil, err
	}
	return &Element{d: d, id: m[elementKey]}, nil
}

func (d *Driver) findAll(ctx context.Context, p string, by By) ([]*Element, error) {
	var list []map[string]string
	if err := d.call(ctx, http.MethodPost, p, map[string]any{"using": by.using, "value": by.value}, &list); err != nil {
		return nil, err
	}
	out := make([]*Element, len(list))
	for i, m := range list {
		out[i] = &Element{d: d, id: m[elementKey]}
	}
	return out, nil
}

func (e *Element) path(p string) string { return e.d.path("/element/" + e.id + p) }

// FindElement looks inside the element.
func (e *Element) FindElement(ctx context.Context, by By) (*Element, error) {
	return e.d.find(ctx, e.path("/element"), by)
}

// FindElements looks inside the element.
func (e *Element) FindElements(ctx context.Context, by By) ([]*Element, error) {
	return e.d.findAll(ctx, e.path("/elements"), by)
}

// Click clicks the element.
func (e *Element) Click(ctx context.Context) error {
	return e.d.call(ctx, http.MethodPost, e.path("/click"), nil, nil)
}

// Clear empties an input.
func (e *Element) Clear(ctx context.Context) error {
	return e.d.call(ctx, http.MethodPost, e.path("/clear"), nil, nil)
}

// SendKeys types text into the element.
func (e *Element) SendKeys(ctx context.Context, text string) error {
	return e.d.call(ctx, http.MethodPost, e.path("/value"), map[string]any{"text": text, "value": strings.Split(text, "")}, nil)
}

// Text is the element's visible text.
func (e *Element) Text(ctx context.Context) (string, error) {
	var s string
	err := e.d.call(ctx, http.MethodGet, e.path("/text"), nil, &s)
	return s, err
}

// TagName is the element's tag.
func (e *Element) TagName(ctx context.Context) (string, error) {
	var s string
	err := e.d.call(ctx, http.MethodGet, e.path("/name"), nil, &s)
	return s, err
}

var booleanAttrs = map[string]bool{
	"disabled": true, "readonly": true, "checked": true, "selected": true, "multiple": true, "required": true,
	"hidden": true, "autofocus": true, "draggable": false, "async": true, "defer": true, "ismap": true,
	"noresize": true, "nowrap": true, "novalidate": true, "open": true, "paused": true, "reversed": true,
}

// Attribute reads an attribute (or, failing that, the property), "" if unset,
// like Selenium's get_attribute.
func (e *Element) Attribute(ctx context.Context, name string) (string, error) {
	var v any
	if err := e.d.call(ctx, http.MethodGet, e.path("/property/"+name), nil, &v); err == nil && v != nil {
		switch x := v.(type) {
		case string:
			return x, nil
		case bool:
			if x {
				return "true", nil
			}
			// Selenium reports a false boolean attribute (disabled, readonly,
			// checked ...) as None, not "false".
			if booleanAttrs[strings.ToLower(name)] {
				return "", nil
			}
			return "false", nil
		case float64:
			if x == float64(int64(x)) {
				return fmt.Sprint(int64(x)), nil
			}
			return fmt.Sprint(x), nil
		}
	}
	var a any
	if err := e.d.call(ctx, http.MethodGet, e.path("/attribute/"+name), nil, &a); err != nil {
		return "", err
	}
	if s, ok := a.(string); ok {
		return s, nil
	}
	return "", nil
}

// IsDisplayed reports whether the element is visible.
func (e *Element) IsDisplayed(ctx context.Context) (bool, error) {
	var b bool
	err := e.d.call(ctx, http.MethodGet, e.path("/displayed"), nil, &b)
	return b, err
}

// IsEnabled reports whether the element is enabled.
func (e *Element) IsEnabled(ctx context.Context) (bool, error) {
	var b bool
	err := e.d.call(ctx, http.MethodGet, e.path("/enabled"), nil, &b)
	return b, err
}

// IsSelected reports whether a checkbox, radio or option is selected.
func (e *Element) IsSelected(ctx context.Context) (bool, error) {
	var b bool
	err := e.d.call(ctx, http.MethodGet, e.path("/selected"), nil, &b)
	return b, err
}

// ID is the driver's reference to the element (equality in scripts).
func (e *Element) ID() string { return e.id }
