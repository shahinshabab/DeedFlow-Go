package textconv

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
	c := New()
	for _, tc := range cases {
		if got := c.ConvertASCIIToUnicode(tc.In); got != tc.Out {
			bad++
			if bad < 6 {
				t.Errorf("convert(%q) = %q, python %q", tc.In, got, tc.Out)
			}
		}
	}
	t.Logf("%d cases, %d mismatches", len(cases), bad)
}

func TestKnownWord(t *testing.T) {
	// "വസ്തു" typed in keystroke order, from the Python source's own comments.
	if got := New().ConvertASCIIToUnicode("hkXv p"); got != "വസ്തു" {
		t.Errorf("got %q", got)
	}
}
