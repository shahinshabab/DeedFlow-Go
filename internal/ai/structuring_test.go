package ai

import (
	"os"
	"testing"

	"context"
	"encoding/json"
	"github.com/shahinshabab/deedflow-go/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Ports of RealDeedExtractionTests.test_ai_nulls_for_missing_values_do_not_fail_extraction
// and AiProviderTests (deeds/tests.py).
func TestAINullsForMissingValuesDoNotFailExtraction(t *testing.T) {
	data, err := ParseExtracted([]byte(`{
	  "giver": [{"name": "വാസു", "age": "......", "pin": null}],
	  "recipient": null,
	  "previous_documents": [{"document_type": "രജിസ്റ്റർ/ഫയൽ ചെയ്ത", "sro": null, "year": "1,987"}],
	  "other_details": {"document_price": null, "deed_type": null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if data.Giver[0].Age != nil || data.Giver[0].Pin != "" {
		t.Errorf("giver = %+v", data.Giver[0])
	}
	if len(data.Recipient) != 0 {
		t.Errorf("recipient = %+v", data.Recipient)
	}
	doc := data.PreviousDocuments[0]
	if doc.SRO != "" || doc.Year == nil || *doc.Year != 1987 {
		t.Errorf("previous document = %+v", doc)
	}
	if data.OtherDetails.DeedType != "" {
		t.Errorf("deed type = %q", data.OtherDetails.DeedType)
	}
}

func TestOpenAIOnlyUnlessTheClaudeFallbackIsOn(t *testing.T) {
	var openAI, claude int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "openai") {
			openAI++
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "not json"}}}})
			return
		}
		claude++
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []map[string]any{{"type": "tool_use", "input": map[string]any{
			"giver": []map[string]any{{"name": "Claude's answer"}}}}}})
	}))
	defer srv.Close()
	cfg := &config.Config{OpenAIAPIKey: "k", OpenAIModel: "m", AnthropicAPIKey: "k", AnthropicModel: "m"}
	c := New(cfg)
	c.OpenAIURL, c.AnthropicURL = srv.URL+"/openai", srv.URL+"/claude"

	if _, err := c.StructureDeedText(context.Background(), "text"); err == nil {
		t.Errorf("a bad OpenAI answer should fail without the fallback")
	}
	if claude != 0 {
		t.Errorf("Claude was called with the fallback off")
	}
	cfg.AIClaudeFallback = true
	out, err := c.StructureDeedText(context.Background(), "text")
	if err != nil || len(out.Giver) != 1 || out.Giver[0].Name != "Claude's answer" {
		t.Errorf("fallback = %+v, %v", out, err)
	}
	if openAI != 2 || claude != 1 {
		t.Errorf("calls: OpenAI %d, Claude %d", openAI, claude)
	}
}
