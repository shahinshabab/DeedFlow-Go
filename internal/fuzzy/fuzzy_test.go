package fuzzy

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

type goldenCase struct {
	A, B      string
	Ratio     int      `json:"ratio"`
	Partial   int      `json:"partial"`
	SeqRatio  float64  `json:"seq_ratio"`
	SeqQuick  float64  `json:"seq_quick"`
	SeqBlocks [][3]int `json:"seq_blocks"`
}

func TestAgainstPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	var badRatio, badPartial, badSeq int
	for _, c := range cases {
		if got := Ratio(c.A, c.B); got != c.Ratio {
			badRatio++
			if badRatio < 5 {
				t.Errorf("Ratio(%q,%q) = %d, python %d", c.A, c.B, got, c.Ratio)
			}
		}
		if got := PartialRatio(c.A, c.B); got != c.Partial {
			badPartial++
			if badPartial < 8 {
				t.Errorf("PartialRatio(%q,%q) = %d, python %d", c.A, c.B, got, c.Partial)
			}
		}
		m := NewSequenceMatcher(c.A, c.B)
		if math.Abs(m.Ratio()-c.SeqRatio) > 1e-12 || math.Abs(m.QuickRatio()-c.SeqQuick) > 1e-12 {
			badSeq++
			if badSeq < 5 {
				t.Errorf("SequenceMatcher(%q,%q) ratio %v quick %v, python %v %v", c.A, c.B, m.Ratio(), m.QuickRatio(), c.SeqRatio, c.SeqQuick)
			}
		}
		blocks := m.GetMatchingBlocks()
		if len(blocks) != len(c.SeqBlocks) {
			t.Errorf("blocks(%q,%q) = %v, python %v", c.A, c.B, blocks, c.SeqBlocks)
			continue
		}
		for i, b := range blocks {
			if [3]int{b.A, b.B, b.Size} != c.SeqBlocks[i] {
				t.Errorf("blocks(%q,%q) = %v, python %v", c.A, c.B, blocks, c.SeqBlocks)
				break
			}
		}
	}
	t.Logf("%d cases; ratio mismatches %d, partial mismatches %d, difflib mismatches %d", len(cases), badRatio, badPartial, badSeq)
}
