// Package translit ports indic_transliteration's Malayalam -> ITRANS path
// (sanscript._brahmic) using scheme data generated from the Python package.
package translit

// MalayalamToITRANS transliterates Malayalam script into ITRANS.
func MalayalamToITRANS(data string) string {
	for _, s := range fromShortcuts { // unapply_shortcuts (none for Malayalam)
		data = replaceAll(data, s[1], s[0])
	}
	out := brahmic([]rune(data))
	for _, s := range toShortcuts { // apply_shortcuts (none for ITRANS)
		out = replaceAll(out, s[0], s[1])
	}
	return out
}

func replaceAll(s, old, new string) string {
	if old == "" {
		return s
	}
	var b []byte
	for i := 0; i < len(s); {
		if len(s)-i >= len(old) && s[i:i+len(old)] == old {
			b = append(b, new...)
			i += len(old)
		} else {
			b = append(b, s[i])
			i++
		}
	}
	return string(b)
}

func brahmic(data []rune) string {
	var buf []byte
	i := 0
	hadConsonant, found := false, false
	for i <= len(data) {
		end := i + maxKey
		if end > len(data) {
			end = len(data)
		}
		var token []rune
		if i < len(data) {
			token = data[i:end]
		}
		for len(token) > 0 {
			t := string(token)
			if len(token) == 1 {
				if v, ok := m_vowel_marks[t]; ok {
					buf = append(buf, v...)
					found = true
				} else if v, ok := m_virama[t]; ok {
					buf = append(buf, v...)
					found = true
				} else {
					if hadConsonant {
						buf = append(buf, 'a')
					}
					if v, ok := m_non_marks_viraama[t]; ok {
						buf = append(buf, v...)
					} else {
						buf = append(buf, t...)
					}
					found = true
				}
			} else if v, ok := m_non_marks_viraama[t]; ok {
				if hadConsonant {
					buf = append(buf, 'a')
				}
				buf = append(buf, v...)
				found = true
			}
			if found {
				_, isCons := m_consonants[t]
				hadConsonant = toIsRoman && isCons
				i += len(token)
				break
			}
			token = token[:len(token)-1]
		}
		if !found {
			if hadConsonant {
				buf = append(buf, m_virama[firstViramaKey()]...)
			}
			if i < len(data) {
				buf = append(buf, string(data[i])...)
				hadConsonant = false
			}
			i++
		}
		found = false
	}
	if hadConsonant {
		buf = append(buf, 'a')
	}
	return string(buf)
}

// firstViramaKey returns the single virama key (Malayalam has one).
func firstViramaKey() string {
	for k := range m_virama {
		return k
	}
	return ""
}
