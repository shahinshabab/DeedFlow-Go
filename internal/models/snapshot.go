package models

import (
	"encoding/json"
	"fmt"
)

// RowFP is the fingerprint and display key of one row last confirmed on the
// portal (Deed.portal_snapshot["_rows"][list][i]).
type RowFP struct {
	FP  string `json:"fp"`
	Key string `json:"key"`
}

// Snapshot is Deed.portal_snapshot: section -> fingerprint of that section as
// last confirmed saved on the portal, plus "_rows", the per-row record that
// lets a single edited row be sent alone. It marshals to the same flat JSON
// object the Django project stored.
type Snapshot struct {
	Sections map[string]string
	Rows     map[string][]RowFP
}

// NewSnapshot returns an empty snapshot.
func NewSnapshot() Snapshot {
	return Snapshot{Sections: map[string]string{}}
}

// Clone copies the snapshot.
func (s Snapshot) Clone() Snapshot {
	out := Snapshot{Sections: make(map[string]string, len(s.Sections))}
	for k, v := range s.Sections {
		out.Sections[k] = v
	}
	if s.Rows != nil {
		out.Rows = make(map[string][]RowFP, len(s.Rows))
		for k, v := range s.Rows {
			out.Rows[k] = append([]RowFP(nil), v...)
		}
	}
	return out
}

// IsEmpty reports whether nothing has been recorded.
func (s Snapshot) IsEmpty() bool { return len(s.Sections) == 0 && len(s.Rows) == 0 }

// MarshalJSON writes the flat object.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(s.Sections)+1)
	for k, v := range s.Sections {
		out[k] = v
	}
	if s.Rows != nil {
		out["_rows"] = s.Rows
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads the flat object.
func (s *Snapshot) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if string(b) == "null" {
		*s = NewSnapshot()
		return nil
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("portal_snapshot: %w", err)
	}
	s.Sections = map[string]string{}
	s.Rows = nil
	for k, v := range raw {
		if k == "_rows" {
			if err := json.Unmarshal(v, &s.Rows); err != nil {
				return fmt.Errorf("portal_snapshot rows: %w", err)
			}
			continue
		}
		var str string
		if err := json.Unmarshal(v, &str); err == nil {
			s.Sections[k] = str
		}
	}
	return nil
}

// SurveyMapping is one schedule whose old survey numbers couldn't be paired
// with their sub-divisions from the deed alone (Deed.survey_mapping).
type SurveyMapping struct {
	Start      int       `json:"start"`
	Count      int       `json:"count"`
	Number     string    `json:"number"`
	OldSurveys []string  `json:"old_surveys"`
	Subs       []string  `json:"subs"`
	Assignment []int     `json:"assignment"`
	Areas      []float64 `json:"areas"`
}
