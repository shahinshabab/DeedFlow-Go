package filereader

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestLegacyAgainstDjango(t *testing.T) {
	raw, err := os.ReadFile("testdata/oracle_legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	var o struct {
		ToUnicode   [][2]string
		FromUnicode []struct {
			In  string
			Out [][2]any
		}
		IsLegacy [][3]any
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(generic["to_unicode"], &o.ToUnicode)
	var from [][]json.RawMessage
	json.Unmarshal(generic["from_unicode"], &from)
	json.Unmarshal(generic["is_legacy"], &o.IsLegacy)

	for _, c := range o.ToUnicode {
		if got := ToUnicode(c[0]); got != c[1] {
			t.Errorf("ToUnicode(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	for _, c := range from {
		var in string
		var want [][2]any
		json.Unmarshal(c[0], &in)
		json.Unmarshal(c[1], &want)
		var got [][2]any
		for _, p := range FromUnicode(in) {
			got = append(got, [2]any{p.Text, p.Legacy})
		}
		if len(want) == 0 && len(got) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("FromUnicode(%q) = %q, want %q", in, got, want)
		}
	}
	t.Logf("%d to_unicode, %d from_unicode, %d is_legacy cases", len(o.ToUnicode), len(from), len(o.IsLegacy))
	for _, c := range o.IsLegacy {
		if got := IsLegacyFont(c[0].(string), c[1].(string)); got != c[2].(bool) {
			t.Errorf("IsLegacyFont(%q, %q) = %v, want %v", c[0], c[1], got, c[2])
		}
	}
}
