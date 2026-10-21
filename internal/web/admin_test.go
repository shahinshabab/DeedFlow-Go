package web

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The helpers the admin re-implements from Django are checked against what
// Django answers (testdata/admin_oracle.json, written by tools/admin/gen_oracle.py).

type oracleCase struct {
	In        string   `json:"in"`
	Out       any      `json:"out"`
	Back      string   `json:"back"`
	Ok        bool     `json:"ok"`
	Value     string   `json:"value"`
	Errors    []string `json:"errors"`
	MaxDigits *int     `json:"max_digits"`
	Places    *int     `json:"places"`
	Lists     [][]string
}

func loadOracle(t *testing.T) map[string][]oracleCase {
	t.Helper()
	b, err := os.ReadFile("testdata/admin_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string][]oracleCase
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSmartSplit(t *testing.T) {
	for _, c := range loadOracle(t)["smart_split"] {
		got := smartSplit(c.In)
		want, _ := c.Out.([]any)
		if len(got) != len(want) {
			t.Errorf("smart_split(%q) = %q, Django %v", c.In, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i].(string) {
				t.Errorf("smart_split(%q) = %q, Django %v", c.In, got, want)
			}
		}
	}
}

func TestQuote(t *testing.T) {
	for _, c := range loadOracle(t)["quote"] {
		if got := adminQuote(c.In); got != c.Out.(string) {
			t.Errorf("quote(%q) = %q, Django %q", c.In, got, c.Out)
		}
		if got := adminUnquote(adminQuote(c.In)); got != c.Back {
			t.Errorf("unquote(quote(%q)) = %q, Django %q", c.In, got, c.Back)
		}
	}
}

func TestDateInput(t *testing.T) {
	for _, c := range loadOracle(t)["date"] {
		d, ok := parseDateInput(c.In)
		if strings.TrimSpace(c.In) == "" {
			continue // empty is handled by the field
		}
		if ok != c.Ok {
			t.Errorf("date %q: ok=%v, Django %v (%v)", c.In, ok, c.Ok, c.Errors)
			continue
		}
		if ok && d.Format("2006-01-02") != c.Value {
			t.Errorf("date %q = %s, Django %s", c.In, d.Format("2006-01-02"), c.Value)
		}
	}
}

func TestTimeInput(t *testing.T) {
	for _, c := range loadOracle(t)["time"] {
		d, ok := parseTimeInput(c.In)
		if strings.TrimSpace(c.In) == "" {
			continue
		}
		if ok != c.Ok {
			t.Errorf("time %q: ok=%v, Django %v", c.In, ok, c.Ok)
			continue
		}
		if ok {
			got := time.Time{}.Add(d).Format("15:04:05.000000")
			want := c.Value
			if !strings.Contains(want, ".") {
				want += ".000000"
			}
			if got != want {
				t.Errorf("time %q = %s, Django %s", c.In, got, c.Value)
			}
		}
	}
}

func TestIntegerField(t *testing.T) {
	for _, c := range loadOracle(t)["int"] {
		v, msg := cleanInteger(c.In, nil, nil)
		if len(c.In) > 18 && c.Ok {
			continue // beyond a 64-bit column; Django would fail in the database
		}
		if strings.TrimSpace(c.In) == "" {
			if v != nil || msg != "" {
				t.Errorf("int %q: %v %q", c.In, v, msg)
			}
			continue
		}
		if (msg == "") != c.Ok {
			t.Errorf("int %q: msg=%q, Django ok=%v %v", c.In, msg, c.Ok, c.Errors)
			continue
		}
		if c.Ok && (v == nil || strconv.FormatInt(*v, 10) != c.Value) {
			t.Errorf("int %q = %v, Django %s", c.In, v, c.Value)
		}
		if !c.Ok && msg != c.Errors[0] {
			t.Errorf("int %q: %q, Django %q", c.In, msg, c.Errors[0])
		}
	}
}

func TestDecimalField(t *testing.T) {
	for _, c := range loadOracle(t)["decimal"] {
		d, ok, msg := cleanDecimal(c.In, c.MaxDigits, c.Places)
		if strings.TrimSpace(c.In) == "" {
			if ok || msg != "" {
				t.Errorf("decimal %q: %v %q", c.In, ok, msg)
			}
			continue
		}
		if (msg == "") != c.Ok {
			t.Errorf("decimal(%v,%v) %q: msg=%q, Django ok=%v %v", c.MaxDigits, c.Places, c.In, msg, c.Ok, c.Errors)
			continue
		}
		if c.Ok && !decimalEqual(d, c.Value) {
			t.Errorf("decimal %q = %s, Django %s", c.In, d, c.Value)
		}
		if !c.Ok && msg != c.Errors[0] {
			t.Errorf("decimal(%v,%v) %q: %q, Django %q", c.MaxDigits, c.Places, c.In, msg, c.Errors[0])
		}
	}
}

func TestEmailValidator(t *testing.T) {
	for _, c := range loadOracle(t)["email"] {
		if c.In == "" {
			continue
		}
		if got := validEmail(c.In); got != c.Ok {
			t.Errorf("email %q: %v, Django %v", c.In, got, c.Ok)
		}
	}
}

func TestMediaMerge(t *testing.T) {
	b, _ := os.ReadFile("testdata/admin_oracle.json")
	var raw struct {
		Media []struct {
			Lists [][]string `json:"lists"`
			Out   []string   `json:"out"`
		} `json:"media"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, c := range raw.Media {
		got := mergeMediaLists(c.Lists)
		if strings.Join(got, ",") != strings.Join(c.Out, ",") {
			t.Errorf("merge(%v) = %v, Django %v", c.Lists, got, c.Out)
		}
	}
}

func TestPyJSON(t *testing.T) {
	for _, c := range loadOracle(t)["json_dump"] {
		got := pyJSONFromInput(c.In)
		if c.Out == nil {
			if got != c.In {
				t.Errorf("json %q: %q, Django leaves it", c.In, got)
			}
			continue
		}
		if got != c.Out.(string) {
			t.Errorf("json %q = %q, Django %q", c.In, got, c.Out)
		}
	}
}
