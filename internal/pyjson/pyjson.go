// Package pyjson writes JSON exactly like Python's
// json.dumps(value, sort_keys=True, ensure_ascii=False): keys sorted, ", " and
// ": " separators, no HTML escaping. The portal section fingerprints are
// hashes of that text, so they must match what the Django project stored.
package pyjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Dumps encodes any JSON-marshalable value the Python way.
func Dumps(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return "", err
	}
	var b strings.Builder
	write(&b, generic)
	return b.String(), nil
}

// MustDumps is Dumps for values that can't fail to marshal.
func MustDumps(v any) string {
	s, err := Dumps(v)
	if err != nil {
		panic(err)
	}
	return s
}

func write(b *strings.Builder, v any) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case json.Number:
		b.WriteString(string(t))
	case string:
		writeString(b, t)
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteString(", ")
			}
			write(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys) // byte order of UTF-8 equals code point order, like Python's str sort
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writeString(b, k)
			b.WriteString(": ")
			write(b, t[k])
		}
		b.WriteByte('}')
	default:
		fmt.Fprintf(b, "%v", t)
	}
}

func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
