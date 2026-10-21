// Package ai turns the converted Malayalam deed text into structured data
// with the office's fine-tuned OpenAI model, optionally falling back to
// Claude: the Go counterpart of deeds/utils/ai_structuring.py.
package ai

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/shahinshabab/deedflow-go/internal/config"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
)

// ExtractedParty is one giver or recipient.
type ExtractedParty struct {
	Name         string `json:"name"`
	HouseName    string `json:"house_name"`
	RelativeName string `json:"relative_name"`
	Relation     string `json:"relation"`
	Gender       string `json:"gender"`
	Age          *int   `json:"age"`
	Job          string `json:"job"`
	IDType       string `json:"id_type"`
	IDNumber     string `json:"id_number"`
	Post         string `json:"post"`
	Pin          string `json:"pin"`
	District     string `json:"district"`
	Taluk        string `json:"taluk"`
	Village      string `json:"village"`
	Place        string `json:"place"`
}

// ExtractedPriorDocument is a document the deed refers to.
type ExtractedPriorDocument struct {
	DocumentType string `json:"document_type"`
	Number       string `json:"number"`
	District     string `json:"district"`
	SRO          string `json:"sro"`
	Book         *int   `json:"book"`
	Volume       *int   `json:"volume"`
	PageFrom     *int   `json:"page_from"`
	PageTo       *int   `json:"page_to"`
	Year         *int   `json:"year"`
	Remarks      string `json:"remarks"`
}

// ExtractedOtherDetails are the deed's own figures and classification.
type ExtractedOtherDetails struct {
	DocumentPrice *int   `json:"document_price"`
	FairValue     *int   `json:"fair_value"`
	DeedType      string `json:"deed_type"`
}

// ExtractedDeedData is the structured result.
type ExtractedDeedData struct {
	Giver             []ExtractedParty         `json:"giver"`
	Recipient         []ExtractedParty         `json:"recipient"`
	PreviousDocuments []ExtractedPriorDocument `json:"previous_documents"`
	OtherDetails      ExtractedOtherDetails    `json:"other_details"`
}

// --- lenient validation (pydantic's _LenientModel) ---------------------------

func asObject(v any, path string) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: Input should be a valid dictionary", path)
	}
	return m, nil
}

// strField reads a str field: null is "", other non-strings are errors.
func strField(m map[string]any, key, path string, required bool) (string, error) {
	v, present := m[key]
	if !present {
		if required {
			return "", fmt.Errorf("%s.%s: Field required", path, key)
		}
		return "", nil
	}
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	}
	return "", fmt.Errorf("%s.%s: Input should be a valid string", path, key)
}

// intField reads an int|None field; digit strings become ints and any other
// string becomes None.
func intField(m map[string]any, key, path string) (*int, error) {
	v, present := m[key]
	if !present || v == nil {
		return nil, nil
	}
	switch x := v.(type) {
	case string:
		digits := strings.ReplaceAll(strings.TrimFunc(x, unicode.IsSpace), ",", "")
		if digits != "" && strings.IndexFunc(digits, func(r rune) bool { return !unicode.IsDigit(r) }) < 0 {
			if n, err := strconv.Atoi(digits); err == nil {
				return &n, nil
			}
		}
		return nil, nil
	case json.Number:
		if i, err := x.Int64(); err == nil {
			n := int(i)
			return &n, nil
		}
		if f, err := x.Float64(); err == nil && f == math.Trunc(f) && math.Abs(f) < 1e15 {
			n := int(f)
			return &n, nil
		}
	}
	return nil, fmt.Errorf("%s.%s: Input should be a valid integer", path, key)
}

type strSpec struct {
	dst      *string
	key      string
	required bool
}

func readStrings(m map[string]any, path string, specs []strSpec) error {
	for _, s := range specs {
		v, err := strField(m, s.key, path, s.required)
		if err != nil {
			return err
		}
		*s.dst = v
	}
	return nil
}

func parseParty(v any, path string) (ExtractedParty, error) {
	var p ExtractedParty
	m, err := asObject(v, path)
	if err != nil {
		return p, err
	}
	err = readStrings(m, path, []strSpec{
		{&p.Name, "name", true}, {&p.HouseName, "house_name", false}, {&p.RelativeName, "relative_name", false},
		{&p.Relation, "relation", false}, {&p.Gender, "gender", false}, {&p.Job, "job", false},
		{&p.IDType, "id_type", false}, {&p.IDNumber, "id_number", false}, {&p.Post, "post", false},
		{&p.Pin, "pin", false}, {&p.District, "district", false}, {&p.Taluk, "taluk", false},
		{&p.Village, "village", false}, {&p.Place, "place", false},
	})
	if err != nil {
		return p, err
	}
	p.Age, err = intField(m, "age", path)
	return p, err
}

func parseList[T any](m map[string]any, key string, parse func(any, string) (T, error)) ([]T, error) {
	v, present := m[key]
	if !present || v == nil {
		return []T{}, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: Input should be a valid list", key)
	}
	out := make([]T, 0, len(arr))
	for i, e := range arr {
		item, err := parse(e, fmt.Sprintf("%s.%d", key, i))
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func parsePrior(v any, path string) (ExtractedPriorDocument, error) {
	var d ExtractedPriorDocument
	m, err := asObject(v, path)
	if err != nil {
		return d, err
	}
	err = readStrings(m, path, []strSpec{
		{&d.DocumentType, "document_type", true}, {&d.Number, "number", false}, {&d.District, "district", false},
		{&d.SRO, "sro", false}, {&d.Remarks, "remarks", false},
	})
	if err != nil {
		return d, err
	}
	for _, f := range []struct {
		dst **int
		key string
	}{{&d.Book, "book"}, {&d.Volume, "volume"}, {&d.PageFrom, "page_from"}, {&d.PageTo, "page_to"}, {&d.Year, "year"}} {
		if *f.dst, err = intField(m, f.key, path); err != nil {
			return d, err
		}
	}
	return d, nil
}

// ParseExtracted validates the model's JSON object the way the Pydantic
// schema does.
func ParseExtracted(data []byte) (*ExtractedDeedData, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	m, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("Input should be a valid dictionary")
	}
	var out ExtractedDeedData
	var err error
	if out.Giver, err = parseList(m, "giver", parseParty); err != nil {
		return nil, err
	}
	if out.Recipient, err = parseList(m, "recipient", parseParty); err != nil {
		return nil, err
	}
	if out.PreviousDocuments, err = parseList(m, "previous_documents", parsePrior); err != nil {
		return nil, err
	}
	od, err := asObject(m["other_details"], "other_details")
	if err != nil {
		return nil, err
	}
	if out.OtherDetails.DocumentPrice, err = intField(od, "document_price", "other_details"); err != nil {
		return nil, err
	}
	if out.OtherDetails.FairValue, err = intField(od, "fair_value", "other_details"); err != nil {
		return nil, err
	}
	if out.OtherDetails.DeedType, err = strField(od, "deed_type", "other_details", false); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- prompt --------------------------------------------------------------

func options(choices []deeds.Choice) string { return strings.Join(deeds.Values(choices), "/") }

func documentTypeMeanings() string {
	var lines []string
	for _, c := range deeds.PriorDocumentTypeChoices {
		lines = append(lines, fmt.Sprintf("  - \"%s\" (%s): %s.", c.Value, c.Label, deeds.PriorDocumentTypeMeanings[c.Value]))
	}
	return strings.Join(lines, "\n")
}

func districtContext() string {
	var lines []string
	for _, d := range deeds.KeralaDistricts {
		lines = append(lines, fmt.Sprintf("%s: SROs: [%s], Taluks: [%s]", d.Name, strings.Join(d.SROs, ", "), strings.Join(d.Taluks, ", ")))
	}
	return strings.Join(lines, "\n")
}

// SystemPrompt is the extraction instruction given to either provider.
func SystemPrompt() string {
	var types []string
	for _, m := range deeds.DeedTypePortalMapping {
		types = append(types, m.Name)
	}
	relation := options(deeds.RelationChoices)
	gender := options(deeds.GenderChoices)
	idType := options(deeds.IDTypeChoices)
	docType := options(deeds.PriorDocumentTypeChoices)
	deedType := strings.Join(types, "/")
	return `You are a highly efficient assistant specialized in extracting structured data from Malayalam property deed text. Extract the giver(s) (the party transferring the property away - "executant"), the recipient(s) (the party receiving it - "claimant"), any previous documents referenced (chain of title), and the deed's own classification, and return them as JSON matching exactly this shape:

{
  "giver": [{"name", "house_name", "relative_name", "relation" (one of ` + relation + `), "gender" (one of ` + gender + ` - inferred from relation/name/context; ബന്ധു doesn't imply one either way, so rely on name/context for those), "age" (int), "job", "id_type" (one of ` + idType + `), "id_number", "post", "pin", "district", "taluk", "village", "place"}],
  "recipient": [... same shape as giver ...],
  "previous_documents": [{"document_type" (one of ` + docType + `), "number", "district", "sro", "book", "volume", "page_from", "page_to", "year", "remarks"}],
  "other_details": {"document_price" (int), "fair_value" (int), "deed_type" (one of ` + deedType + `)}
}

Rules:
- If "deed_type" is "ഭാഗപത്രം" (partition deed), the giver list equals the recipient list -
  the same people are dividing property amongst themselves.
- A deed releasing an inheritance share (ഓഹരി അവകാശം ... ഒഴിമുറി) is "ഓഹരി അവകാശ ഒഴിമുറി";
  any other release, e.g. between co-owners (കൂട്ടവകാശ ഒഴിമുറി), is "ഒഴിമുറി".
- "document_type" of each previous document must be exactly one of these values, copied
  character for character (the wizard and the registration portal only accept these):
` + documentTypeMeanings() + `
- The text was OCR'd from a PDF - there may be stray characters or a page-break interrupting
  a name or number; use judgement to reconstruct the intended value.
- Use the following valid district/SRO/taluk names as context (match the closest one, the
  text's spelling may be slightly off due to OCR noise):
` + districtContext() + `

Worked example - input:
` + workedExampleInput + `

Worked example - output:
` + workedExampleOutput + `

Respond with ONLY the JSON object, no other text.`
}

func userQuery(text string) string {
	return "Please extract the data from the following deed text. Note that the text was " +
		"extracted from a PDF, so there may be a signer's name interrupting a sentence as " +
		"noise - use judgement to separate that out. Text:\n\n" + text
}

// --- providers -------------------------------------------------------------

//go:embed schema.json
var schemaJSON []byte

// Client talks to the AI providers.
type Client struct {
	Cfg  *config.Config
	HTTP *http.Client
	// Endpoints can be overridden in tests.
	OpenAIURL    string
	AnthropicURL string
}

// New makes a client with the production endpoints.
func New(cfg *config.Config) *Client {
	return &Client{
		Cfg:          cfg,
		HTTP:         &http.Client{Timeout: 10 * time.Minute},
		OpenAIURL:    "https://api.openai.com/v1/chat/completions",
		AnthropicURL: "https://api.anthropic.com/v1/messages",
	}
}

func (c *Client) post(ctx context.Context, url string, headers map[string]string, body any) ([]byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not connect: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, truncate(string(data), 500))
	}
	return data, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func (c *Client) structureWithOpenAI(ctx context.Context, text string) (*ExtractedDeedData, error) {
	if c.Cfg.OpenAIAPIKey == "" {
		return nil, errors.New("OPENAI_API_KEY is not configured.")
	}
	data, err := c.post(ctx, c.OpenAIURL, map[string]string{"Authorization": "Bearer " + c.Cfg.OpenAIAPIKey}, map[string]any{
		"model": c.Cfg.OpenAIModel,
		"messages": []map[string]string{
			{"role": "system", "content": SystemPrompt()},
			{"role": "user", "content": userQuery(text)},
		},
		"response_format": map[string]string{"type": "json_object"},
	})
	if err != nil {
		slog.Error("OpenAI request failed", "err", err)
		return nil, err
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &resp); err != nil || len(resp.Choices) == 0 {
		return nil, fmt.Errorf("unexpected OpenAI response: %s", truncate(string(data), 500))
	}
	content := resp.Choices[0].Message.Content
	out, err := ParseExtracted([]byte(content))
	if err != nil {
		slog.Error("OpenAI response did not match the expected schema", "err", err, "raw", truncate(content, 2000))
		return nil, err
	}
	return out, nil
}

func (c *Client) structureWithClaude(ctx context.Context, text string) (*ExtractedDeedData, error) {
	if c.Cfg.AnthropicAPIKey == "" {
		return nil, errors.New("ANTHROPIC_API_KEY is not configured.")
	}
	data, err := c.post(ctx, c.AnthropicURL, map[string]string{
		"x-api-key": c.Cfg.AnthropicAPIKey, "anthropic-version": "2023-06-01",
	}, map[string]any{
		"model":      c.Cfg.AnthropicModel,
		"max_tokens": 8192,
		"system":     SystemPrompt(),
		"messages":   []map[string]string{{"role": "user", "content": userQuery(text)}},
		"tools": []map[string]any{{
			"name":         "extract_deed_data",
			"description":  "Records the structured data extracted from the deed text.",
			"input_schema": json.RawMessage(schemaJSON),
		}},
		"tool_choice": map[string]string{"type": "tool", "name": "extract_deed_data"},
	})
	if err != nil {
		slog.Error("Anthropic API error", "err", err)
		return nil, err
	}
	var resp struct {
		Content []struct {
			Type  string          `json:"type"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	for _, b := range resp.Content {
		if b.Type == "tool_use" {
			out, err := ParseExtracted(b.Input)
			if err != nil {
				slog.Error("Claude's tool input did not match the expected schema", "err", err, "input", string(b.Input))
			}
			return out, err
		}
	}
	return nil, errors.New("Claude did not return a tool_use block with the extracted data.")
}

// StructureDeedText structures deed text with OpenAI, falling back to Claude
// only when AI_CLAUDE_FALLBACK is on.
func (c *Client) StructureDeedText(ctx context.Context, text string) (*ExtractedDeedData, error) {
	out, err := c.structureWithOpenAI(ctx, text)
	if err == nil {
		return out, nil
	}
	if !c.Cfg.AIClaudeFallback {
		return nil, err
	}
	slog.Warn("OpenAI structuring failed; falling back to Claude.", "err", err)
	return c.structureWithClaude(ctx, text)
}
