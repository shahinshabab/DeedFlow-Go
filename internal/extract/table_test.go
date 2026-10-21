package extract

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprint(x)
	}
	return fmt.Sprint(v)
}

func TestScheduleAgainstPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/tables.json")
	if err != nil {
		t.Skip("no golden data")
	}
	var cases []struct {
		Table   [][]*string
		Entries []map[string]any
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	ex := NewTableExtractor()
	bad := 0
	for ci, c := range cases {
		got := ex.ExtractSchedule([]Table{Table(c.Table)})
		if len(got) != len(c.Entries) {
			t.Errorf("case %d: %d entries want %d", ci, len(got), len(c.Entries))
			bad++
			continue
		}
		for i, e := range got {
			w := c.Entries[i]
			checks := map[string]string{
				"schedule_number": e.Number, "district": e.District, "sro": e.SRO, "taluk": e.Taluk,
				"village": e.Village, "desam": e.Desam, "local_body_type": e.LocalBodyType, "local_body_name": e.LocalBodyName,
				"resurvey_block": e.ResurveyBlock, "resurvey_number": e.ResurveyNumber, "resurvey_sub": e.ResurveySub,
				"old_survey": e.OldSurvey, "old_sub": e.OldSub, "tp_number": e.TPNumber, "details": e.Details,
				"boundary_east": e.BoundaryEast, "boundary_north": e.BoundaryNorth, "boundary_west": e.BoundaryWest,
				"boundary_south": e.BoundarySouth,
				"total_hectare":  e.TotalHectare.String(), "total_are": e.TotalAre.String(), "total_sqm": e.TotalSqm.String(),
				"transfer_hectare": e.TransferHectare.String(), "transfer_are": e.TransferAre.String(), "transfer_sqm": e.TransferSqm.String(),
			}
			for k, v := range checks {
				wv, ok := w[k]
				if !ok {
					continue
				}
				ws := pyStr(wv)
				if k == "schedule_number" && wv == nil {
					ws = ""
				}
				if v != ws {
					if bad < 15 {
						t.Errorf("case %d entry %d %s: got %q want %q", ci, i, k, v, ws)
					}
					bad++
				}
			}
			_, hasPrice := w["price"]
			if hasPrice != e.PriceSet {
				t.Errorf("case %d entry %d price flag %v want %v", ci, i, e.PriceSet, hasPrice)
				bad++
			}
			wm, hasMap := w["survey_mapping"]
			if hasMap != (e.SurveyMapping != nil) {
				t.Errorf("case %d entry %d survey_mapping %v want %v", ci, i, e.SurveyMapping != nil, hasMap)
				bad++
			} else if hasMap {
				m := wm.(map[string]any)
				if int(m["count"].(float64)) != e.SurveyMapping.Count {
					t.Errorf("case %d entry %d mapping count", ci, i)
					bad++
				}
				gj, _ := json.Marshal(e.SurveyMapping.Areas)
				wj, _ := json.Marshal(m["areas"])
				if string(gj) != string(wj) {
					t.Errorf("case %d entry %d areas %s want %s", ci, i, gj, wj)
					bad++
				}
			}
		}
	}
	t.Logf("%d cases, %d mismatches", len(cases), bad)
}
