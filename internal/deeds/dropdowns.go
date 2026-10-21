package deeds

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/models"
)

// DropdownSource is where the portal's scraped dropdown options are stored.
type DropdownSource interface {
	DropdownFirst(ctx context.Context, page, field string) (*models.PortalDropdown, error)
	DropdownOptionsByKey(ctx context.Context, page, field, key string) (json.RawMessage, bool, error)
	DropdownOptionsByPattern(ctx context.Context, page, field, pattern string) (json.RawMessage, bool, error)
}

// Dropdowns looks the portal's own options up: the registration portal's
// dropdown options as scraped into the PortalDropdown table, which the wizard
// offers so it matches exactly what the portal will accept.
type Dropdowns struct{ Src DropdownSource }

func parseOptions(raw json.RawMessage) []Option {
	var pairs [][]any
	if err := json.Unmarshal(raw, &pairs); err != nil {
		return nil
	}
	out := make([]Option, 0, len(pairs))
	for _, p := range pairs {
		if len(p) < 2 {
			continue
		}
		out = append(out, Option{Value: toStr(p[0]), Text: toStr(p[1])})
	}
	return out
}

func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strings.TrimSuffix(strings.TrimSuffix(jsonNum(t), ".0"), ".")
		}
		return jsonNum(t)
	case nil:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonNum(f float64) string { b, _ := json.Marshal(f); return string(b) }

func parentFieldsOf(raw json.RawMessage) []string {
	var parents [][]any
	_ = json.Unmarshal(raw, &parents)
	out := make([]string, 0, len(parents))
	for _, p := range parents {
		if len(p) > 0 {
			out = append(out, toStr(p[0]))
		}
	}
	return out
}

// MakeParentKey builds the key a row is stored under from its parents' texts.
func MakeParentKey(fields, texts []string) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = f + "=" + Normalize(texts[i])
	}
	key := strings.Join(parts, "|")
	if r := []rune(key); len(r) > 500 {
		key = string(r[:500])
	}
	return key
}

// valueAliases are our own stored labels for a dropdown's option values (the
// PR document type is stored as our label; the portal knows a one-letter code).
func valueAliases(page, field string) []Alias {
	if page == "prior_document" && field == "documentType" {
		return PriorDocAliases()
	}
	return nil
}

// ParentFields are the portal fields field's options depend on, in cascade
// order; ok is false when nothing is stored for it.
func (d *Dropdowns) ParentFields(ctx context.Context, page, field string) ([]string, bool) {
	row, err := d.Src.DropdownFirst(ctx, page, field)
	if err != nil || row == nil {
		return nil, false
	}
	return parentFieldsOf(row.Parents), true
}

// OptionsFor returns the portal's options for one dropdown. known maps portal
// field -> the text currently chosen in it for the dropdowns this one cascades
// from. ok is false when nothing is stored for the dropdown at all (the caller
// falls back to its own data); the options are empty when a parent hasn't been
// chosen yet or the portal offers nothing for that choice.
func (d *Dropdowns) OptionsFor(ctx context.Context, page, field string, known map[string]string) ([]Option, bool) {
	k := map[string]string{}
	for f, v := range known {
		if strings.TrimSpace(v) != "" {
			k[f] = v
		}
	}
	first, err := d.Src.DropdownFirst(ctx, page, field)
	if err != nil || first == nil {
		return nil, false
	}
	fields := parentFieldsOf(first.Parents)
	if len(fields) == 0 {
		return parseOptions(first.Options), true
	}
	any := false
	for _, f := range fields {
		if _, ok := k[f]; ok {
			any = true
		}
	}
	if !any {
		return []Option{}, true
	}
	k = d.resolveKnown(ctx, page, fields, k)
	all := true
	for _, f := range fields {
		if _, ok := k[f]; !ok {
			all = false
		}
	}
	if all {
		texts := make([]string, len(fields))
		for i, f := range fields {
			texts[i] = k[f]
		}
		raw, found, err := d.Src.DropdownOptionsByKey(ctx, page, field, MakeParentKey(fields, texts))
		if err != nil || !found {
			return []Option{}, true
		}
		return parseOptions(raw), true
	}
	// Some parents not given: a dropdown the wizard has no field for (e.g. the
	// executant page's permanent-address district, which the scrape saw fire
	// alongside the present-address one). Match on the rest.
	parts := make([]string, len(fields))
	for i, f := range fields {
		if v, ok := k[f]; ok {
			parts[i] = regexp.QuoteMeta(f + "=" + Normalize(v))
		} else {
			parts[i] = regexp.QuoteMeta(f+"=") + "[^|]*"
		}
	}
	raw, found, err := d.Src.DropdownOptionsByPattern(ctx, page, field, "^"+strings.Join(parts, `\|`)+"$")
	if err != nil || !found {
		return []Option{}, true
	}
	return parseOptions(raw), true
}

// resolveKnown swaps each chosen parent text for the portal's own spelling of
// it, where the stored text says the same thing in other words (another
// chillu spelling, no English gloss, our label for the option, ...), so the
// parent_key lookup still finds the right options.
func (d *Dropdowns) resolveKnown(ctx context.Context, page string, fields []string, known map[string]string) map[string]string {
	resolved := map[string]string{}
	for f, v := range known {
		resolved[f] = v
	}
	for _, f := range fields {
		if _, ok := resolved[f]; !ok {
			continue
		}
		parentOptions, ok := d.OptionsFor(ctx, page, f, resolved)
		if ok && len(parentOptions) > 0 {
			if match, found := BestMatch(resolved[f], parentOptions, 0.75, valueAliases(page, f)); found {
				resolved[f] = match
			}
		}
	}
	return resolved
}

// OptionTexts is OptionsFor reduced to the option texts.
func (d *Dropdowns) OptionTexts(ctx context.Context, page, field string, known map[string]string) ([]string, bool) {
	opts, ok := d.OptionsFor(ctx, page, field, known)
	if !ok {
		return nil, false
	}
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Text
	}
	return out, true
}

// MatchOption swaps text for the portal's closest option for that dropdown
// (given the choices above it in known), or returns text itself when nothing
// is close enough or nothing is stored for the dropdown.
func (d *Dropdowns) MatchOption(ctx context.Context, page, field, text string, known map[string]string) string {
	opts, ok := d.OptionsFor(ctx, page, field, known)
	if ok {
		if m, found := BestMatch(text, opts, 0.75, valueAliases(page, field)); found {
			return m
		}
	}
	return text
}

// ScrapeFile is the JSON the portal scraper writes.
type ScrapeFile struct {
	ScrapedAt string `json:"scraped_at"`
	Pages     map[string]struct {
		Fields map[string]struct {
			Label string `json:"label"`
		} `json:"fields"`
		Rows []struct {
			Field   string          `json:"field"`
			Parents json.RawMessage `json:"parents"`
			Options json.RawMessage `json:"options"`
		} `json:"rows"`
	} `json:"pages"`
}

// ScrapeRows turns one page of a scrape file into rows to store (the first row
// wins for a repeated (field, parent key)).
func ScrapeRows(f *ScrapeFile, page string, scrapedAt time.Time) []models.PortalDropdown {
	pg := f.Pages[page]
	var out []models.PortalDropdown
	seen := map[string]bool{}
	for _, row := range pg.Rows {
		var parents [][]any
		_ = json.Unmarshal(row.Parents, &parents)
		var fields, texts []string
		for _, p := range parents {
			if len(p) >= 3 {
				fields = append(fields, toStr(p[0]))
				texts = append(texts, toStr(p[2]))
			}
		}
		key := MakeParentKey(fields, texts)
		if seen[row.Field+"\x00"+key] {
			continue
		}
		seen[row.Field+"\x00"+key] = true
		label := pg.Fields[row.Field].Label
		if r := []rune(label); len(r) > 255 {
			label = string(r[:255])
		}
		parentsRaw := row.Parents
		if len(parentsRaw) == 0 || string(parentsRaw) == "null" {
			parentsRaw = json.RawMessage("[]")
		}
		optsRaw := row.Options
		if len(optsRaw) == 0 || string(optsRaw) == "null" {
			optsRaw = json.RawMessage("[]")
		}
		out = append(out, models.PortalDropdown{Page: page, Field: row.Field, Label: label, Parents: parentsRaw,
			ParentKey: key, Options: optsRaw, ScrapedAt: scrapedAt})
	}
	return out
}
