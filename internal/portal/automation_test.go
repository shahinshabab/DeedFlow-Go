package portal

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/webdriver"
)

type fixedSolver struct{}

func (fixedSolver) SolveCaptcha(context.Context, []byte, int) (string, []string) {
	return "1234", nil
}

func startChromedriver(t *testing.T) string {
	bin := os.Getenv("CHROMEDRIVER")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("chromedriver"); err != nil {
			t.Skip("no chromedriver")
		}
	}
	chrome := os.Getenv("CHROME_BIN")
	if chrome == "" {
		chrome = "/opt/pw-browsers/chromium-1194/chrome-linux/chrome"
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Skip("no chromium")
	}
	cmd := exec.Command(bin, "--port=9632")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	for i := 0; i < 50; i++ {
		if resp, err := http.Get("http://127.0.0.1:9632/status"); err == nil {
			resp.Body.Close()
			return "http://127.0.0.1:9632"
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Skip("chromedriver did not start")
	return ""
}

func newTestAutomation(t *testing.T) (*Automation, *mockPortal) {
	base := startChromedriver(t)
	m := newMockPortal()
	t.Cleanup(m.srv.Close)
	chrome := os.Getenv("CHROME_BIN")
	if chrome == "" {
		chrome = "/opt/pw-browsers/chromium-1194/chrome-linux/chrome"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	a, err := New(ctx, Config{
		SeleniumURL: base, PortalURL: m.srv.URL + "/index.php", Headless: true, ChromeBinary: chrome,
		Solver: fixedSolver{},
	}, Credentials{Username: "demo", Password: "secret", NewDeedMenuIndex: 1, MyDeedsMenuIndex: 2}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	a.log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	t.Cleanup(a.Close)
	return a, m
}

func TestLoginAndRows(t *testing.T) {
	a, m := newTestAutomation(t)
	if err := try(func() { a.EnsureLoggedIn(3) }); err != nil {
		t.Fatalf("login: %v", err)
	}
	tokens := mustv(tryv(a.listMyTokens))
	if len(tokens) != 2 || tokens[0].Token != "T123456" {
		t.Fatalf("tokens = %+v", tokens)
	}
	if err := try(func() { a.FindToken("T123456") }); err != nil {
		t.Fatalf("find token: %v", err)
	}
	if err := try(func() { a.FindToken("T123457") }); !IsTokenUnavailable(err) {
		t.Fatalf("a sent-on token must not be editable, got %v", err)
	}
	if err := try(func() { a.FindToken("T999999") }); !IsTokenUnavailable(err) {
		t.Fatalf("a missing token must not be editable, got %v", err)
	}
	if err := try(func() { a.OpenToken("T123456") }); err != nil {
		t.Fatalf("open token: %v", err)
	}
	a.token = "T123456"

	witness := func(name string) deeds.WitnessPayload {
		return deeds.WitnessPayload{Name: name, Gender: "Male", Relation: "Father of", RName: "Father " + name,
			HouseName: "House", Desam: "Desam", Place: "Place", Job: "Farmer", WType: "WB"}
	}
	insert := func(i int, w deeds.WitnessPayload) StepResult {
		return a.insertRow(tabAttestWitness, "Attesting witness "+w.Name, func() { a.fillWitnessForm(w, true) },
			webdriver.ByID("saveForm2"), i, "T123456")
	}
	if r := insert(1, witness("Alpha")); !r.OK {
		t.Fatalf("first insert: %+v", r)
	}
	if r := insert(2, witness("Beta")); !r.OK {
		t.Fatalf("second insert: %+v", r)
	}
	// A retry of an insert whose save did land must not save twice.
	if r := insert(2, witness("Beta")); !r.OK {
		t.Fatalf("repeat insert: %+v", r)
	}
	if got := m.rowNames(); !reflect.DeepEqual(got, []string{"Alpha", "Beta"}) {
		t.Fatalf("portal rows = %v", got)
	}
	if len(a.savedRows()) != 2 {
		t.Fatalf("saved rows = %d", len(a.savedRows()))
	}

	// The portal's own refusal is reported and not retried.
	r := insert(3, witness("REJECT"))
	if r.OK || r.Retryable || !strings.Contains(r.Message, "Name Invalid") {
		t.Fatalf("rejection = %+v", r)
	}

	// Edit the second row in place.
	changed := witness("Beta Edited")
	res := a.editRow(tabAttestWitness, "Attesting witness 2", func() { a.fillWitnessForm(changed, true) }, 1, "Beta", "Beta Edited", "T123456")
	if !res.OK {
		t.Fatalf("edit: %+v", res)
	}
	if got := m.rowNames(); !reflect.DeepEqual(got, []string{"Alpha", "Beta Edited"}) {
		t.Fatalf("portal rows after edit = %v", got)
	}
	// Editing a row that is not the one expected is refused, not guessed.
	res = a.editRow(tabAttestWitness, "Attesting witness 1", func() { a.fillWitnessForm(changed, true) }, 0, "Nobody", "x", "T123456")
	if res.OK || res.Retryable {
		t.Fatalf("a wrong row must not be edited: %+v", res)
	}

	// Clear the tab: every row goes, one confirm() each.
	cleared := mustv(tryv(func() StepResult { return a.clearTab(tabAttestWitness, "Attesting witnesses") }))
	if !cleared.OK || len(m.rowNames()) != 0 {
		t.Fatalf("clear: %+v rows=%v", cleared, m.rowNames())
	}
}

func TestRecoverAfterLogout(t *testing.T) {
	a, m := newTestAutomation(t)
	if err := try(func() { a.EnsureLoggedIn(3) }); err != nil {
		t.Fatal(err)
	}
	if err := try(func() { a.OpenToken("T123456") }); err != nil {
		t.Fatal(err)
	}
	m.logout()
	// The open page is still the old one; the next request finds the login page.
	must(a.d.Get(a.ctx, m.srv.URL+"/wizard?tab=prevDoc"))
	if err := try(func() { a.raiseIfSessionLost() }); !IsSessionLost(err) {
		t.Fatalf("a logout must read as a lost session, got %v", err)
	}
	if err := try(func() { a.recoverSession("T123456", "test") }); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !a.loggedInQuick() {
		t.Fatal("not logged in after recovering")
	}
}

func TestHelpers(t *testing.T) {
	if !transient([]string{"Error while saving. Please try again."}) || transient([]string{"Name Required"}) {
		t.Fatal("transient")
	}
	if got := rejections([]string{"Saved Successfully", "Name Required", "Do you want to add another?"}); !reflect.DeepEqual(got, []string{"Name Required"}) {
		t.Fatalf("rejections = %v", got)
	}
	if digitsOnly("2,50,000.00") != "250000" || digitsOnly("000") != "" {
		t.Fatal("digitsOnly")
	}
	if !sameValue("52.00", "52") || sameValue("a", "b") || !sameValue("x", "x") {
		t.Fatal("sameValue")
	}
	if pyList([]string{"a", "it's"}) != `['a', "it's"]` {
		t.Fatalf("pyList = %s", pyList([]string{"a", "it's"}))
	}
	if got := ExpandSections([]string{"prior_documents"}); !got["executants"] || !got["document_link"] || got["claimants"] {
		t.Fatalf("expand = %v", got)
	}
}
