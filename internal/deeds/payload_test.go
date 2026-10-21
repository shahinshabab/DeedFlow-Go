package deeds

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/pyjson"
)

// fillFromJSON copies a JSON object onto a struct by its `db` tags.
func fillFromJSON(dst any, src map[string]any) error {
	rv := reflect.ValueOf(dst).Elem()
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("db")
		if tag == "" {
			continue
		}
		raw, ok := src[tag]
		if !ok {
			continue
		}
		if err := setField(rv.Field(i), raw); err != nil {
			return fmt.Errorf("%s: %w", tag, err)
		}
	}
	return nil
}

func setField(f reflect.Value, raw any) error {
	if raw == nil {
		f.Set(reflect.Zero(f.Type()))
		return nil
	}
	switch f.Type() {
	case reflect.TypeOf((*time.Time)(nil)):
		t, err := time.Parse("2006-01-02", raw.(string))
		if err != nil {
			return err
		}
		f.Set(reflect.ValueOf(&t))
		return nil
	case reflect.TypeOf((*db.Decimal)(nil)):
		d, err := db.ParseDecimal(raw.(string))
		if err != nil {
			return err
		}
		f.Set(reflect.ValueOf(&d))
		return nil
	}
	switch f.Kind() {
	case reflect.String:
		f.SetString(raw.(string))
	case reflect.Int16, reflect.Int32, reflect.Int64, reflect.Int:
		f.SetInt(int64(raw.(float64)))
	case reflect.Bool:
		f.SetBool(raw.(bool))
	case reflect.Ptr:
		elem := reflect.New(f.Type().Elem())
		if err := setField(elem.Elem(), raw); err != nil {
			return err
		}
		f.Set(elem)
	default:
		return fmt.Errorf("unsupported kind %s", f.Kind())
	}
	return nil
}

func list[T any](v any) []T {
	var out []T
	for _, it := range v.([]any) {
		var x T
		if err := fillFromJSON(&x, it.(map[string]any)); err != nil {
			panic(err)
		}
		out = append(out, x)
	}
	return out
}

func deedFromCase(c map[string]any) *DeedData {
	d := c["deed"].(map[string]any)
	deed := &models.Deed{TokenNumber: d["token_number"].(string), Status: d["status"].(string)}
	if snap, ok := c["snapshot"]; ok {
		b, _ := json.Marshal(snap)
		_ = json.Unmarshal(b, &deed.PortalSnapshot)
	} else {
		deed.PortalSnapshot = models.NewSnapshot()
	}
	dd := &DeedData{Deed: deed}
	reg := &models.RegistrationDetail{}
	if err := fillFromJSON(reg, d["registration"].(map[string]any)); err != nil {
		panic(err)
	}
	dd.Registration = reg
	pr := &models.Presenter{}
	if err := fillFromJSON(pr, d["presenter"].(map[string]any)); err != nil {
		panic(err)
	}
	dd.Presenter = pr
	dd.PriorDocuments = list[models.PriorDocument](d["prior_documents"])
	dd.Executants = list[models.Party](d["executants"])
	dd.Claimants = list[models.Party](d["claimants"])
	dd.Schedule = list[models.ScheduleItem](d["schedule_items"])
	dd.Witnesses = list[models.Witness](d["witnesses"])
	return dd
}

func canonical(v any) string { return pyjson.MustDumps(v) }

func TestPayloadAgainstPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/payload_cases.json")
	if err != nil {
		t.Skip("no golden file")
	}
	var cases []map[string]any
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	today := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	checked := 0
	for i, c := range cases {
		dd := deedFromCase(c)
		if c["error"] != nil {
			if _, err := BuildPayload(dd); err == nil {
				t.Errorf("case %d: python failed to build a payload, go did not", i)
			}
			continue
		}
		p, err := BuildPayload(dd)
		if err != nil {
			t.Errorf("case %d: %v", i, err)
			continue
		}
		checked++
		if got, want := canonical(p), canonical(c["payload"]); got != want {
			t.Errorf("case %d payload differs:\n go: %s\n py: %s", i, got, want)
			continue
		}
		fps := SectionFingerprints(p)
		if got, want := canonical(fps), canonical(c["fingerprints"]); got != want {
			t.Errorf("case %d fingerprints differ:\n go: %s\n py: %s", i, got, want)
		}
		if got, want := canonical(RowFingerprints(p)), canonical(c["rows"]); got != want {
			t.Errorf("case %d row fingerprints differ", i)
		}
		if got, want := canonical(PreflightProblems(p, nil, today)), canonical(c["problems_all"]); got != want {
			t.Errorf("case %d preflight (all) differs:\n go: %s\n py: %s", i, got, want)
		}
		if got, want := canonical(PreflightProblems(p, []string{"presenter", "schedule"}, today)), canonical(c["problems_some"]); got != want {
			t.Errorf("case %d preflight (some) differs:\n go: %s\n py: %s", i, got, want)
		}
		// pending sections and the row-edit plan
		var wantSections []string
		for _, s := range c["sections"].([]any) {
			wantSections = append(wantSections, s.(string))
		}
		gotSections := PendingSections(dd)
		if strings.Join(gotSections, ",") != strings.Join(wantSections, ",") {
			t.Errorf("case %d pending sections %v, python %v", i, gotSections, wantSections)
			continue
		}
		plan, whole := PlanRowEdits(dd.Deed.PortalSnapshot, p, gotSections)
		gotPlan := map[string]any{}
		var gotDescribed []string
		for _, name := range plan.Lists {
			var chs []any
			for _, ch := range plan.Changes[name] {
				chs = append(chs, map[string]any{"index": ch.Index, "kind": ch.Kind, "old_key": ch.OldKey})
				gotDescribed = append(gotDescribed, DescribeRowChange(name, ch, p))
			}
			gotPlan[name] = chs
		}
		if got, want := canonical(gotPlan), canonical(c["plan"]); got != want {
			t.Errorf("case %d plan differs:\n go: %s\n py: %s", i, got, want)
		}
		if whole == nil {
			whole = []string{}
		}
		if got, want := canonical(whole), canonical(c["whole"]); got != want {
			t.Errorf("case %d whole sections %s, python %s", i, got, want)
		}
		if got, want := canonical(append([]string{}, gotDescribed...)), canonical(c["described"]); got != want {
			t.Errorf("case %d descriptions differ:\n go: %s\n py: %s", i, got, want)
		}
		wantMin, _ := c["minimum_document_value"].(float64)
		if gotMin, _ := MinimumDocumentValue(dd.Schedule); float64(gotMin) != wantMin {
			t.Errorf("case %d minimum document value %d, python %v", i, gotMin, wantMin)
		}
	}
	t.Logf("checked %d cases", checked)
}
