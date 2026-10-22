package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/store"
)

// Ports of deeds/tests.py: SeedTestDeedsTests, PreflightTests and the rule
// tests of DateAndValueRuleTests that don't need a form or a browser.

func today() time.Time { return deeds.Today(time.UTC) }

func payloadOf(t *testing.T, st *store.Store, id int64) (*deeds.DeedData, *deeds.Payload) {
	t.Helper()
	ctx := context.Background()
	deed, err := st.DeedByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	data, err := st.LoadDeedData(ctx, deed)
	if err != nil {
		t.Fatal(err)
	}
	p, err := deeds.BuildPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	return data, p
}

// seedAll creates all the test deeds for the fixture's user (the fixture
// already made number 3).
func seedAll(t *testing.T, st *store.Store, ownerID int64) []int64 {
	t.Helper()
	var ids []int64
	for i := 0; i < SeedSpecCount(); i++ {
		if i == 2 {
			continue // the fixture's own
		}
		d, err := SeedTestDeed(context.Background(), st, ownerID, i, today())
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}
	return ids
}

func TestSeedCreatesFiveCompleteTestDeeds(t *testing.T) {
	_, fixture, st := submitFixture(t, &fakePortal{})
	ids := append(seedAll(t, st, fixture.OwnerID), fixture.ID)
	if len(ids) != 5 {
		t.Fatalf("%d deeds", len(ids))
	}
	for _, id := range ids {
		_, p := payloadOf(t, st, id)
		if len(p.Executants) == 0 || len(p.Claimants) == 0 || len(p.Schedule) == 0 {
			t.Errorf("deed %d: parties/schedule missing", id)
		}
		for _, it := range p.Schedule {
			if it.FairValue == "" {
				t.Errorf("deed %d: a property has no fair value", id)
			}
		}
	}
}

func TestSeededDeedsPassPreflight(t *testing.T) {
	_, fixture, st := submitFixture(t, &fakePortal{})
	for _, id := range append(seedAll(t, st, fixture.OwnerID), fixture.ID) {
		_, p := payloadOf(t, st, id)
		if problems := deeds.PreflightProblems(p, nil, today()); len(problems) != 0 {
			t.Errorf("deed %d: %v", id, problems)
		}
	}
}

func problemsMention(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestPreflightPriceMustMatchDocumentValue(t *testing.T) {
	_, deed, st := submitFixture(t, &fakePortal{})
	ctx := context.Background()
	data, _ := payloadOf(t, st, deed.ID)
	item := data.Schedule[0]
	one := int32(1)
	item.Price = &one
	if err := st.UpdateRow(ctx, store.RowTable["schedule"], &item); err != nil {
		t.Fatal(err)
	}
	_, p := payloadOf(t, st, deed.ID)
	if problems := deeds.PreflightProblems(p, nil, today()); !problemsMention(problems, "add up to") {
		t.Errorf("problems = %v", problems)
	}
	if problems := deeds.PreflightProblems(p, []string{"witnesses"}, today()); len(problems) != 0 {
		t.Errorf("a check of the witnesses page alone found %v", problems)
	}
}

func TestPreflightCatchesPastPresentationAndFutureStampDates(t *testing.T) {
	_, deed, st := submitFixture(t, &fakePortal{})
	data, _ := payloadOf(t, st, deed.ID)
	past := today().AddDate(0, 0, -1)
	data.Presenter.PresenterDate = &past
	if err := st.SavePresenter(context.Background(), data.Presenter); err != nil {
		t.Fatal(err)
	}
	_, p := payloadOf(t, st, deed.ID)
	if problems := deeds.PreflightProblems(p, nil, today()); !problemsMention(problems, "presentation date") {
		t.Errorf("problems = %v", problems)
	}

	data, _ = payloadOf(t, st, deed.ID)
	now, future := today(), today().AddDate(0, 0, 1)
	data.Presenter.PresenterDate, data.Presenter.StampDate = &now, &future
	if err := st.SavePresenter(context.Background(), data.Presenter); err != nil {
		t.Fatal(err)
	}
	_, p = payloadOf(t, st, deed.ID)
	if problems := deeds.PreflightProblems(p, nil, today()); !problemsMention(problems, "stamp date") {
		t.Errorf("problems = %v", problems)
	}
}

func TestMinimumDocumentValueIsFairValueTimesArea(t *testing.T) {
	// The example from the request: 25,000 per are, 2 are -> 50,000.
	if v, ok := deeds.MinimumPrice("25000", deeds.Area{Hectare: "0", Are: "2", Sqm: "0"}, deeds.Area{}); !ok || v != 50000 {
		t.Errorf("25,000 x 2 are = %d, %v", v, ok)
	}
	// 1 hectare 50 sq.m = 100.5 are; a blank transfer falls back to the whole area.
	if v, ok := deeds.MinimumPrice("1000", deeds.Area{}, deeds.Area{Hectare: "1", Are: "0", Sqm: "50"}); !ok || v != 100500 {
		t.Errorf("1,000 x 100.5 are = %d, %v", v, ok)
	}

	_, deed, st := submitFixture(t, &fakePortal{})
	ctx := context.Background()
	// Seeded deed 3 is the first that has a priced property; its minimum is what the seed says.
	data, _ := payloadOf(t, st, deed.ID)
	min, ok := deeds.MinimumDocumentValue(data.Schedule)
	if !ok || min == 0 {
		t.Fatalf("minimum = %d, %v", min, ok)
	}
	// A price and document value below it are caught, whichever page is sent first.
	low := int32(min - 1)
	item := data.Schedule[0]
	item.Price = &low
	if err := st.UpdateRow(ctx, store.RowTable["schedule"], &item); err != nil {
		t.Fatal(err)
	}
	data.Presenter.DocumentValue = &low
	if err := st.SavePresenter(ctx, data.Presenter); err != nil {
		t.Fatal(err)
	}
	_, p := payloadOf(t, st, deed.ID)
	if problems := deeds.PreflightProblems(p, nil, today()); len(problems) == 0 {
		t.Errorf("a document value below the minimum passed preflight")
	}
	if problems := deeds.PreflightProblems(p, []string{"presenter"}, today()); len(problems) == 0 {
		t.Errorf("the Presentation page alone should be refused")
	}
}

func TestPresenterDefaultsFillTodayAndFiveSheets(t *testing.T) {
	_, deed, st := submitFixture(t, &fakePortal{})
	data, _ := payloadOf(t, st, deed.ID)
	p := data.Presenter
	old := today().AddDate(0, 0, -3)
	p.PresenterDate, p.StampDate, p.DocumentSheetCount = &old, nil, nil
	changed := deeds.ApplyPresenterDefaults(p, today())
	if !p.PresenterDate.Equal(today()) || !p.StampDate.Equal(today()) || p.DocumentSheetCount == nil || *p.DocumentSheetCount != 5 || len(changed) != 3 {
		t.Errorf("defaults: %v %v %v %v", p.PresenterDate, p.StampDate, p.DocumentSheetCount, changed)
	}
	// What the user set is kept.
	keep, sheets := today().AddDate(0, 0, -10), int16(8)
	p.StampDate, p.DocumentSheetCount = &keep, &sheets
	deeds.ApplyPresenterDefaults(p, today())
	if !p.StampDate.Equal(keep) || *p.DocumentSheetCount != 8 {
		t.Errorf("the user's stamp date and sheets were overwritten")
	}
}
