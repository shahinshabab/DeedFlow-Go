package ai

import (
	"os"
	"testing"
)

func TestSystemPromptMatchesPython(t *testing.T) {
	want, err := os.ReadFile("testdata/system_prompt.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := SystemPrompt(); got != string(want) {
		for i := 0; i < len(got) && i < len(want); i++ {
			if got[i] != want[i] {
				lo := max(0, i-60)
				t.Fatalf("differs at byte %d:\n got  %q\n want %q", i, got[lo:min(len(got), i+60)], string(want[lo:min(len(want), i+60)]))
			}
		}
		t.Fatalf("length %d want %d", len(got), len(want))
	}
}

func TestParseLenient(t *testing.T) {
	out, err := ParseExtracted([]byte(`{"giver":[{"name":"A","age":"40","sro":null,"pin":null}],"previous_documents":[{"document_type":"x","sro":null,"book":"1,2","year":"......"}],"other_details":{"document_price":125000,"fair_value":null,"deed_type":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if *out.Giver[0].Age != 40 || out.Giver[0].Pin != "" || *out.PreviousDocuments[0].Book != 12 || out.PreviousDocuments[0].Year != nil {
		t.Fatalf("%+v", out)
	}
	if _, err := ParseExtracted([]byte(`{"giver":[{"house_name":"x"}]}`)); err == nil {
		t.Fatal("missing name should fail")
	}
}
