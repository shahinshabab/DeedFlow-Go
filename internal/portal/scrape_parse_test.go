package portal

import (
	"reflect"
	"testing"
)

// Port of PortalDropdownTests.test_response_parsing (deeds/tests.py).
func TestResponseParsing(t *testing.T) {
	html := "<option value=''>-- Select --</option><option value='0101'>അമരവിള</option>" +
		`<option value="0102" >ആറ്റിങ്ങൽ</option>`
	target := []Option{{"", "Select"}, {"0101", "അമരവിള"}, {"0102", "ആറ്റിങ്ങൽ"}}
	spec := findExtraction(html, target)
	if spec == nil || spec.Kind != "html" {
		t.Fatalf("spec = %+v", spec)
	}
	if got := realOptions(extractOptions(html, *spec)); !reflect.DeepEqual(got, target[1:]) {
		t.Errorf("options = %v", got)
	}

	jsonText := `{"status": 1, "data": [{"id": "0700", "name": "വിലയാധാരം"}, {"id": "1200", "name": "ദാനം"}]}`
	spec = findExtraction(jsonText, []Option{{"0700", "വിലയാധാരം"}, {"1200", "ദാനം"}})
	if spec == nil || spec.Kind != "dicts" || len(spec.Path) != 1 || spec.Path[0] != "data" || spec.ValueKey != "id" || spec.TextKey != "name" {
		t.Errorf("json spec = %+v", spec)
	}
	if findExtraction(jsonText, []Option{{"0700", "വിലയാധാരം"}}) != nil {
		t.Errorf("a partial match must not be accepted")
	}
	if !isPlaceholder("0", "-ബുക്ക് നമ്പര്‍-") || isPlaceholder("1", "1 - Immovable Property") {
		t.Errorf("placeholder detection is wrong")
	}
}
