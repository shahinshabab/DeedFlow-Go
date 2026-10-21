package translit

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAgainstPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ In, Out string }
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		if got := MalayalamToITRANS(c.In); got != c.Out {
			if bad < 10 {
				t.Errorf("%q: got %q want %q", c.In, got, c.Out)
			}
			bad++
		}
	}
	t.Logf("%d cases, %d mismatches", len(cases), bad)
}
