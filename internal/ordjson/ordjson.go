// Package ordjson decodes JSON keeping object key order, which Python's dict
// preserved and the reference data (district lists, fuzzy-match choices)
// relies on.
package ordjson

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Map is a JSON object with its keys in document order.
type Map struct {
	Keys []string
	M    map[string]any
}

// Get returns the value for key.
func (m *Map) Get(key string) (any, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m.M[key]
	return v, ok
}

// Obj returns the nested object at key, or nil.
func (m *Map) Obj(key string) *Map {
	if m == nil {
		return nil
	}
	o, _ := m.M[key].(*Map)
	return o
}

// Str returns the string at key, or "".
func (m *Map) Str(key string) string {
	if m == nil {
		return ""
	}
	s, _ := m.M[key].(string)
	return s
}

// Len is the number of keys.
func (m *Map) Len() int {
	if m == nil {
		return 0
	}
	return len(m.Keys)
}

// KeyList returns a copy of the keys in order.
func (m *Map) KeyList() []string {
	if m == nil {
		return nil
	}
	return append([]string(nil), m.Keys...)
}

// Parse decodes a JSON document. Objects become *Map, arrays []any, numbers float64.
func Parse(data []byte) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	v, err := value(dec)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func value(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := &Map{M: map[string]any{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("ordjson: non-string key %v", kt)
				}
				v, err := value(dec)
				if err != nil {
					return nil, err
				}
				if _, dup := m.M[key]; !dup {
					m.Keys = append(m.Keys, key)
				}
				m.M[key] = v
			}
			if _, err := dec.Token(); err != nil && err != io.EOF {
				return nil, err
			}
			return m, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := value(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil && err != io.EOF {
				return nil, err
			}
			return arr, nil
		}
	}
	return tok, nil
}
