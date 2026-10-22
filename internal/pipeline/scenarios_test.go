package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/portal"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// Ports of SubmissionTaskTests and RowEditTests (deeds/tests.py), with the
// portal replaced by a scripted stand-in.

type submitCall struct {
	existing string
	sections []string
}

type scriptedPortal struct {
	mu          sync.Mutex
	calls       []submitCall
	plans       []deeds.RowPlan
	hook        func(n int, existing string, sections []string, onToken func(string)) (*portal.SubmitResult, error)
	failRows    map[string]bool // "list/index" whose edit fails
	tokenAbsent bool
}

func allOK(sections []string) *portal.SubmitResult {
	res := &portal.SubmitResult{SectionResults: map[string]portal.StageResult{}, SectionErrors: map[string][]string{},
		SectionsOK: map[string]bool{}, OverallOK: true}
	if sections == nil {
		for _, s := range deeds.PortalSections {
			sections = append(sections, s.Key)
		}
		sections = append(sections, "document_link")
	}
	for _, s := range sections {
		res.SectionsOK[s] = true
	}
	return res
}

func (p *scriptedPortal) SubmitDeed(_ *deeds.Payload, progress portal.Progress, existing string, onToken func(string), sections []string) (*portal.SubmitResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, submitCall{existing, append([]string(nil), sections...)})
	if p.hook != nil {
		return p.hook(len(p.calls), existing, sections, onToken)
	}
	token := existing
	if token == "" {
		token = "T12345678"
		onToken(token)
	}
	progress("login", 10, true, "Logged in.", nil)
	res := allOK(sections)
	res.TokenNumber = token
	return res, nil
}

func (p *scriptedPortal) SubmitRowEdits(_ *deeds.Payload, plan deeds.RowPlan, token string, _ portal.Progress) (*portal.RowEditResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plans = append(p.plans, plan)
	if p.tokenAbsent {
		return nil, &portal.TokenUnavailableError{Msg: fmt.Sprintf("Token %s isn't in My Token on the portal - nothing was changed.", token)}
	}
	res := &portal.RowEditResult{TokenNumber: token, RowsOK: map[string]map[int]bool{}, SectionResults: map[string]portal.StageResult{}, OverallOK: true}
	for _, list := range plan.Lists {
		res.RowsOK[list] = map[int]bool{}
		for _, ch := range plan.Changes[list] {
			ok := !p.failRows[fmt.Sprintf("%s/%d", list, ch.Index)]
			res.RowsOK[list][ch.Index] = ok
			msg := "updated."
			if !ok {
				msg = "Executant 2: portal rejected the update - Age Required"
				res.OverallOK = false
			}
			res.SectionResults[fmt.Sprintf("%s_%d", list, ch.Index+1)] = portal.StageResult{OK: ok, Message: msg}
		}
	}
	return res, nil
}

func (p *scriptedPortal) Close() {}

func (p *scriptedPortal) reset() { p.calls, p.plans = nil, nil }

type scenario struct {
	env  *Env
	deed *models.Deed
	st   *store.Store
	p    *scriptedPortal
}

func newScenario(t *testing.T) *scenario {
	p := &scriptedPortal{failRows: map[string]bool{}}
	env, deed, st := submitFixture(t, p)
	return &scenario{env, deed, st, p}
}

// submit marks the deed submitting and runs the job.
func (s *scenario) submit(t *testing.T) (*models.Deed, error) {
	t.Helper()
	ctx := context.Background()
	d, _ := s.st.DeedByID(ctx, s.deed.ID)
	startSubmit(t, s.st, d)
	err := s.env.SubmitDeedToPortal(ctx, s.deed.ID)
	d, _ = s.st.DeedByID(ctx, s.deed.ID)
	return d, err
}

func (s *scenario) deedNow() *models.Deed {
	d, _ := s.st.DeedByID(context.Background(), s.deed.ID)
	return d
}

func (s *scenario) data(t *testing.T) *deeds.DeedData {
	t.Helper()
	data, err := s.st.LoadDeedData(context.Background(), s.deedNow())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func keys(sections []string) string { return strings.Join(sections, ",") }

func TestContinuedTokenFillsEveryPageButRegistration(t *testing.T) {
	// "Continue with existing token": the deed starts with a token the portal
	// already holds, so no token is created and registration (fixed on that
	// token) is never sent.
	s := newScenario(t)
	d := s.deedNow()
	d.TokenNumber, d.PortalSnapshot = "T10380594", models.Snapshot{}
	if err := s.st.SaveDeed(context.Background(), d, "token_number", "portal_snapshot"); err != nil {
		t.Fatal(err)
	}
	got, err := s.submit(t)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.p.calls) != 1 || s.p.calls[0].existing != "T10380594" ||
		keys(s.p.calls[0].sections) != "presenter,prior_documents,executants,claimants,schedule,witnesses" {
		t.Errorf("calls = %+v", s.p.calls)
	}
	if got.Status != models.StatusCompleted || got.TokenNumber != "T10380594" {
		t.Errorf("deed = %s %q (%s)", got.Status, got.TokenNumber, got.ResultMessage)
	}
}

func TestATokenThePortalWontLetUsUseFailsAtOnce(t *testing.T) {
	s := newScenario(t)
	d := s.deedNow()
	d.TokenNumber = "T10000001"
	if err := s.st.SaveDeed(context.Background(), d, "token_number"); err != nil {
		t.Fatal(err)
	}
	// Mark one section as edited so a pass is attempted.
	data := s.data(t)
	data.Presenter.Name += " x"
	if err := s.st.SavePresenter(context.Background(), data.Presenter); err != nil {
		t.Fatal(err)
	}
	s.p.hook = func(n int, existing string, _ []string, _ func(string)) (*portal.SubmitResult, error) {
		return nil, &portal.TokenUnavailableError{Msg: fmt.Sprintf("Token %s isn't in My Token on the portal - nothing was changed.", existing)}
	}
	got, _ := s.submit(t)
	if len(s.p.calls) != 1 {
		t.Errorf("%d passes, want one", len(s.p.calls))
	}
	if got.Status != models.StatusFailed || got.ResultMessage != "Token T10000001 isn't in My Token on the portal - nothing was changed." {
		t.Errorf("deed = %s %q", got.Status, got.ResultMessage)
	}
}

func TestFailedDocumentLinkKeepsTheDeedUnfinished(t *testing.T) {
	s := newScenario(t)
	s.p.hook = func(n int, existing string, sections []string, onToken func(string)) (*portal.SubmitResult, error) {
		if existing == "" {
			onToken("T12345678")
		}
		res := allOK(sections)
		res.TokenNumber = "T12345678"
		res.SectionsOK["document_link"] = false
		res.SectionErrors["document_link"] = []string{"Property 1: portal rejected the save - X"}
		res.OverallOK = false
		return res, nil
	}
	got, _ := s.submit(t)
	if got.Status != models.StatusFailed {
		t.Errorf("status = %s", got.Status)
	}
	if _, confirmed := got.PortalSnapshot.Sections["schedule"]; confirmed && got.PortalSnapshot.Sections["schedule"] != "" {
		t.Errorf("the schedule was recorded as confirmed")
	}
}

func TestPortalRefusingTheDataIsNotRepeated(t *testing.T) {
	s := newScenario(t)
	s.p.hook = func(n int, existing string, sections []string, onToken func(string)) (*portal.SubmitResult, error) {
		if existing == "" {
			onToken("T12345678")
		}
		res := allOK(sections)
		res.TokenNumber = "T12345678"
		res.SectionsOK["schedule"] = false
		res.SectionErrors["schedule"] = []string{"Property 1: portal rejected the save - X"}
		res.OverallOK = false
		return res, nil
	}
	got, _ := s.submit(t)
	if len(s.p.calls) != 1 || got.Status != models.StatusFailed {
		t.Errorf("passes = %d, status %s", len(s.p.calls), got.Status)
	}
}

func TestAStoppedPassResumesWhenATokenExists(t *testing.T) {
	s := newScenario(t)
	s.p.hook = func(n int, existing string, sections []string, onToken func(string)) (*portal.SubmitResult, error) {
		if n == 1 {
			onToken("T12345678")
			return nil, portal.NewError("Lost the portal session too many times - stopping this run.")
		}
		res := allOK(sections)
		res.TokenNumber = existing
		return res, nil
	}
	got, _ := s.submit(t)
	if got.Status != models.StatusCompleted || len(s.p.calls) != 2 {
		t.Fatalf("status %s after %d passes (%s)", got.Status, len(s.p.calls), got.ResultMessage)
	}
	if s.p.calls[1].existing != "T12345678" || strings.Contains(keys(s.p.calls[1].sections), "registration") {
		t.Errorf("second pass = %+v", s.p.calls[1])
	}
}

func TestSecondRunOfTheSameDeedIsSkipped(t *testing.T) {
	s := newScenario(t)
	ctx := context.Background()
	d := s.deedNow()
	startSubmit(t, s.st, d)
	lock, ok, err := s.st.DB.TryLock(ctx, DeedLockKey(d.ID))
	if err != nil || !ok {
		t.Skip("deed lock is held elsewhere")
	}
	defer lock.Release()
	if err := s.env.SubmitDeedToPortal(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.p.calls) != 0 || s.deedNow().Status != models.StatusSubmitting {
		t.Errorf("a second run touched the portal or the deed")
	}
}

func TestCutOffSubmissionIsReleased(t *testing.T) {
	s := newScenario(t)
	ctx := context.Background()
	age := func(d time.Duration) *models.Deed {
		_, err := s.st.DB.Pool.Exec(ctx, `UPDATE deeds_deed SET status = 'submitting', updated_at = now() - make_interval(secs => $2) WHERE id = $1`, s.deed.ID, d.Seconds())
		if err != nil {
			t.Fatal(err)
		}
		return s.deedNow()
	}
	d := age(StaleSubmission + time.Minute)
	if !ReapStaleSubmission(ctx, s.st, d) || d.Status != models.StatusFailed || !strings.Contains(d.ResultMessage, "interrupted") {
		t.Errorf("a cut-off run was not released: %s %q", d.Status, d.ResultMessage)
	}

	// Still running (its lock is held) or recent: left alone.
	d = age(StaleSubmission + time.Minute)
	lock, ok, err := s.st.DB.TryLock(ctx, DeedLockKey(d.ID))
	if err != nil || !ok {
		t.Skip("deed lock is held elsewhere")
	}
	if ReapStaleSubmission(ctx, s.st, d) {
		t.Errorf("a running submission was released")
	}
	lock.Release()
	d = age(time.Minute)
	if ReapStaleSubmission(ctx, s.st, d) || d.Status != models.StatusSubmitting {
		t.Errorf("a recent submission was released")
	}
}

// --- RowEditTests ----------------------------------------------------------------

// submitted is a scenario whose deed has already been submitted once.
func newSubmitted(t *testing.T) *scenario {
	s := newScenario(t)
	got, err := s.submit(t)
	if err != nil || got.Status != models.StatusCompleted {
		t.Fatalf("first submission: %v %s %q", err, got.Status, got.ResultMessage)
	}
	if pending := deeds.PendingSections(s.data(t)); len(pending) != 0 {
		t.Fatalf("pending after the first submission: %v", pending)
	}
	s.p.reset()
	if len(s.data(t).Executants) < 2 {
		t.Skip("the seed deed has fewer than two executants")
	}
	return s
}

func (s *scenario) setExecutantAge(t *testing.T, i int, age int16) {
	t.Helper()
	data := s.data(t)
	e := data.Executants[i]
	e.Age = &age
	if err := s.st.UpdateRow(context.Background(), store.RowTable["executants"], &e); err != nil {
		t.Fatal(err)
	}
}

func TestRenamedRowIsFoundByItsOldName(t *testing.T) {
	s := newSubmitted(t)
	data := s.data(t)
	e := data.Executants[1]
	oldName := e.Name
	e.Name = "പുതിയ പേര്"
	if err := s.st.UpdateRow(context.Background(), store.RowTable["executants"], &e); err != nil {
		t.Fatal(err)
	}
	if _, err := s.submit(t); err != nil {
		t.Fatal(err)
	}
	if got := s.p.plans[0].Changes["executants"][0].OldKey; got != oldName {
		t.Errorf("old key = %q, want %q", got, oldName)
	}
	// Once landed, the new name is what the next edit looks for.
	s.setExecutantAge(t, 1, 33)
	if _, err := s.submit(t); err != nil {
		t.Fatal(err)
	}
	if got := s.p.plans[1].Changes["executants"][0].OldKey; got != "പുതിയ പേര്" {
		t.Errorf("second old key = %q", got)
	}
}

func TestFailedEditMarksTheDeedFailedAndStaysPending(t *testing.T) {
	s := newSubmitted(t)
	s.p.failRows["executants/1"] = true
	s.setExecutantAge(t, 1, 50)
	got, _ := s.submit(t)
	if got.Status != models.StatusFailed || !strings.Contains(got.ResultMessage, "Edit failed") || !strings.Contains(got.ResultMessage, "Age Required") {
		t.Errorf("deed = %s %q", got.Status, got.ResultMessage)
	}
	if pending := deeds.PendingSections(s.data(t)); keys(pending) != "executants" {
		t.Errorf("pending = %v", pending)
	}
}

func TestTokenNotInMyTokenStops(t *testing.T) {
	s := newSubmitted(t)
	s.p.tokenAbsent = true
	s.setExecutantAge(t, 1, 50)
	got, _ := s.submit(t)
	if got.Status != models.StatusFailed || !strings.Contains(got.ResultMessage, "isn't in My Token") {
		t.Errorf("deed = %s %q", got.Status, got.ResultMessage)
	}
	if pending := deeds.PendingSections(s.data(t)); keys(pending) != "executants" {
		t.Errorf("pending = %v", pending)
	}
}

func TestAddedRowIsSavedAsANewRow(t *testing.T) {
	s := newSubmitted(t)
	data := s.data(t)
	first := data.Executants[0]
	row := &models.Party{DeedID: s.deed.ID, Name: "പുതിയ ആൾ", Capacity: first.Capacity, Job: "കൃഷി", Position: int32(len(data.Executants))}
	if err := s.st.InsertRow(context.Background(), store.RowTable["executants"], row); err != nil {
		t.Fatal(err)
	}
	got, _ := s.submit(t)
	n := len(data.Executants)
	if len(s.p.plans) != 1 || len(s.p.plans[0].Changes["executants"]) != 1 {
		t.Fatalf("plans = %+v", s.p.plans)
	}
	ch := s.p.plans[0].Changes["executants"][0]
	if ch.Index != n || ch.Kind != "add" || ch.OldKey != "" || got.Status != models.StatusCompleted {
		t.Errorf("change = %+v, status %s (%s)", ch, got.Status, got.ResultMessage)
	}
}

func TestRemovedRowRedoesTheWholeTab(t *testing.T) {
	s := newSubmitted(t)
	data := s.data(t)
	if err := s.st.DeleteRows(context.Background(), store.RowTable["executants"], s.deed.ID, data.Executants[1].ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.submit(t)
	if len(s.p.plans) != 0 || len(s.p.calls) != 1 || keys(s.p.calls[0].sections) != "executants" || got.Status != models.StatusCompleted {
		t.Errorf("plans %d, calls %+v, status %s (%s)", len(s.p.plans), s.p.calls, got.Status, got.ResultMessage)
	}
}

func TestReorderedRowsAreEditedInPlace(t *testing.T) {
	s := newSubmitted(t)
	data := s.data(t)
	first, second := data.Executants[0], data.Executants[1]
	first.Position, second.Position = 1, 0
	for _, e := range []models.Party{first, second} {
		e := e
		if err := s.st.UpdateRow(context.Background(), store.RowTable["executants"], &e, "position"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.submit(t); err != nil {
		t.Fatal(err)
	}
	if len(s.p.plans) != 1 || len(s.p.calls) != 0 {
		t.Fatalf("plans %d, passes %d", len(s.p.plans), len(s.p.calls))
	}
	changes := s.p.plans[0].Changes["executants"]
	if len(changes) != 2 || changes[0] != (deeds.RowChange{Index: 0, Kind: "edit", OldKey: first.Name}) ||
		changes[1] != (deeds.RowChange{Index: 1, Kind: "edit", OldKey: second.Name}) {
		t.Errorf("changes = %+v", changes)
	}
	if pending := deeds.PendingSections(s.data(t)); len(pending) != 0 {
		t.Errorf("pending = %v", pending)
	}
}

func TestDeedSubmittedBeforeRowTrackingGetsABaseline(t *testing.T) {
	s := newSubmitted(t)
	ctx := context.Background()
	d := s.deedNow()
	d.PortalSnapshot.Rows = nil
	if err := s.st.SaveDeed(ctx, d, "portal_snapshot"); err != nil {
		t.Fatal(err)
	}
	d = s.deedNow()
	if err := s.env.EnsureBaseline(ctx, d); err != nil { // what opening the deed / the wizard does
		t.Fatal(err)
	}
	s.setExecutantAge(t, 1, 50)
	if _, err := s.submit(t); err != nil {
		t.Fatal(err)
	}
	if len(s.p.plans) != 1 || len(s.p.calls) != 0 {
		t.Errorf("plans %d, passes %d", len(s.p.plans), len(s.p.calls))
	}
}
