package webdriver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const page = `<!doctype html><html><body>
<h1 id="title">Hello</h1>
<input id="name" name="name" value="x">
<select id="sel"><option value="">--</option><option value="a">Alpha one</option><option value="b">Beta</option></select>
<button id="alert" onclick="alert('Saved Successfully')">alert</button>
<button id="grow" onclick="var d=document.createElement('div');d.id='made';d.textContent='made';document.body.appendChild(d)">grow</button>
<div id="hidden" style="display:none">h</div>
</body></html>`

func startDriver(t *testing.T) string {
	bin, err := chromedriverPath()
	if err != nil {
		t.Skip("no chromedriver")
	}
	if _, err := os.Stat(chromeBinary()); err != nil {
		t.Skip("no chromium")
	}
	cmd := exec.Command(bin, "--port=9631")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	for i := 0; i < 50; i++ {
		if resp, err := http.Get("http://127.0.0.1:9631/status"); err == nil {
			resp.Body.Close()
			return "http://127.0.0.1:9631"
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Skip("chromedriver did not start")
	return ""
}

func chromeBinary() string {
	if v := os.Getenv("CHROME_BIN"); v != "" {
		return v
	}
	return "/opt/pw-browsers/chromium-1194/chrome-linux/chrome"
}

func TestSession(t *testing.T) {
	base := startDriver(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, err := NewRemote(ctx, base, Options{Args: []string{"--headless=new", "--no-sandbox", "--disable-dev-shm-usage"},
		Binary: chromeBinary(), UnhandledPrompt: "ignore", PageLoadTimeout: 30 * time.Second})
	if err != nil {
		if strings.Contains(err.Error(), "session not created") {
			t.Skip("chromedriver and chromium don't match: ", err)
		}
		t.Fatal(err)
	}
	defer d.Quit(ctx)
	if err := d.Get(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	h1, err := d.FindElement(ctx, ByID("title"))
	if err != nil {
		t.Fatal(err)
	}
	if txt, _ := h1.Text(ctx); txt != "Hello" {
		t.Fatalf("text %q", txt)
	}
	if _, err := d.FindElement(ctx, ByID("nope")); !errors.Is(err, ErrNoSuchElement) {
		t.Fatalf("want no such element, got %v", err)
	}
	in, _ := d.FindElement(ctx, ByName("name"))
	if err := in.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if err := in.SendKeys(ctx, "മുസ്തഫ 123"); err != nil {
		t.Fatal(err)
	}
	if v, _ := in.Attribute(ctx, "value"); v != "മുസ്തഫ 123" {
		t.Fatalf("value %q", v)
	}
	sel, _ := d.FindElement(ctx, ByID("sel"))
	if err := (Select{sel}).SelectByVisibleText(ctx, "Alpha one"); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.ExecuteScript(ctx, "return arguments[0].value;", sel); v != "a" {
		t.Fatalf("selected %v", v)
	}
	if err := (Select{sel}).SelectByValue(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if err := (Select{sel}).SelectByVisibleText(ctx, "Nothing here"); !errors.Is(err, ErrNoSuchElement) {
		t.Fatalf("want no such element, got %v", err)
	}
	// native dialogs stay open (unhandledPromptBehavior=ignore) for us to read
	btn, _ := d.FindElement(ctx, ByID("alert"))
	_ = btn.Click(ctx)
	text, err := d.AlertText(ctx)
	if err != nil || !strings.Contains(text, "Saved Successfully") {
		t.Fatalf("alert %q %v", text, err)
	}
	if _, err := d.FindElement(ctx, ByID("title")); !errors.Is(err, ErrUnexpectedAlert) {
		t.Fatalf("want unexpected alert, got %v", err)
	}
	if err := d.AlertAccept(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AlertText(ctx); !errors.Is(err, ErrNoAlert) {
		t.Fatalf("want no alert, got %v", err)
	}
	// waits
	grow, _ := d.FindElement(ctx, ByID("grow"))
	_ = grow.Click(ctx)
	if _, err := d.Visible(ctx, 3*time.Second, ByID("made")); err != nil {
		t.Fatal(err)
	}
	hidden, _ := d.FindElement(ctx, ByID("hidden"))
	if shown, _ := hidden.IsDisplayed(ctx); shown {
		t.Fatal("hidden shown")
	}
	if _, err := d.Visible(ctx, time.Second, ByID("hidden")); !errors.Is(err, ErrTimeout) {
		t.Fatalf("want timeout, got %v", err)
	}
	png, err := d.Screenshot(ctx)
	if err != nil || len(png) < 100 || string(png[1:4]) != "PNG" {
		t.Fatalf("screenshot %d %v", len(png), err)
	}
	v, err := d.ExecuteAsyncScript(ctx, "var cb = arguments[arguments.length-1]; setTimeout(function(){cb('done')}, 50);")
	if err != nil || v != "done" {
		t.Fatalf("async %v %v", v, err)
	}
	els, err := d.ExecuteScript(ctx, "return [document.getElementById('title')];")
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := els.([]any); len(l) != 1 {
		t.Fatalf("elements %v", els)
	} else if e, ok := l[0].(*Element); !ok || e.ID() != h1.ID() {
		t.Fatalf("element result %v", l[0])
	}
	if u, _ := d.CurrentURL(ctx); !strings.HasPrefix(u, srv.URL) {
		t.Fatalf("url %q", u)
	}
	if old, err := d.FindElement(ctx, ByID("title")); err == nil {
		_, _ = d.ExecuteScript(ctx, "location.reload();")
		time.Sleep(500 * time.Millisecond)
		if !old.IsStale(ctx) {
			t.Fatal("expected a stale element after reload")
		}
	}
}

func chromedriverPath() (string, error) {
	if v := os.Getenv("CHROMEDRIVER"); v != "" {
		return v, nil
	}
	return exec.LookPath("chromedriver")
}
