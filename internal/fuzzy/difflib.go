// Package fuzzy ports the string-similarity routines the Django project relied
// on: Python's difflib.SequenceMatcher, and fuzzywuzzy's ratio / partial_ratio
// as computed by python-Levenshtein. They are ported faithfully (not
// reimplemented "better") because match thresholds were tuned against them.
package fuzzy

import "sort"

// Match is a run of equal elements: a[A:A+Size] == b[B:B+Size].
type Match struct{ A, B, Size int }

// SequenceMatcher is difflib.SequenceMatcher(None, a, b) with autojunk on.
type SequenceMatcher struct {
	a, b    []rune
	b2j     map[rune][]int
	blocks  []Match
	haveBlk bool
}

// NewSequenceMatcher compares a with b.
func NewSequenceMatcher(a, b string) *SequenceMatcher {
	m := &SequenceMatcher{a: []rune(a), b: []rune(b)}
	m.chainB()
	return m
}

func (m *SequenceMatcher) chainB() {
	m.b2j = map[rune][]int{}
	for i, r := range m.b {
		m.b2j[r] = append(m.b2j[r], i)
	}
	n := len(m.b)
	if n >= 200 {
		ntest := n/100 + 1
		for r, idxs := range m.b2j {
			if len(idxs) > ntest {
				delete(m.b2j, r)
			}
		}
	}
}

// FindLongestMatch is difflib's find_longest_match over a[alo:ahi], b[blo:bhi].
func (m *SequenceMatcher) FindLongestMatch(alo, ahi, blo, bhi int) Match {
	besti, bestj, bestsize := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		newj2len := map[int]int{}
		for _, j := range m.b2j[m.a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			newj2len[j] = k
			if k > bestsize {
				besti, bestj, bestsize = i-k+1, j-k+1, k
			}
		}
		j2len = newj2len
	}
	// Popular elements were dropped from b2j; extend the match over them.
	for besti > alo && bestj > blo && m.a[besti-1] == m.b[bestj-1] {
		besti, bestj, bestsize = besti-1, bestj-1, bestsize+1
	}
	for besti+bestsize < ahi && bestj+bestsize < bhi && m.a[besti+bestsize] == m.b[bestj+bestsize] {
		bestsize++
	}
	return Match{besti, bestj, bestsize}
}

// GetMatchingBlocks lists the maximal matching blocks, ending with the
// (len(a), len(b), 0) sentinel, like difflib.
func (m *SequenceMatcher) GetMatchingBlocks() []Match {
	if m.haveBlk {
		return m.blocks
	}
	la, lb := len(m.a), len(m.b)
	type span struct{ alo, ahi, blo, bhi int }
	queue := []span{{0, la, 0, lb}}
	var found []Match
	for len(queue) > 0 {
		s := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x := m.FindLongestMatch(s.alo, s.ahi, s.blo, s.bhi)
		if x.Size > 0 {
			found = append(found, x)
			if s.alo < x.A && s.blo < x.B {
				queue = append(queue, span{s.alo, x.A, s.blo, x.B})
			}
			if x.A+x.Size < s.ahi && x.B+x.Size < s.bhi {
				queue = append(queue, span{x.A + x.Size, s.ahi, x.B + x.Size, s.bhi})
			}
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].A != found[j].A {
			return found[i].A < found[j].A
		}
		if found[i].B != found[j].B {
			return found[i].B < found[j].B
		}
		return found[i].Size < found[j].Size
	})
	var out []Match
	var i1, j1, k1 int
	for _, x := range found {
		if i1+k1 == x.A && j1+k1 == x.B {
			k1 += x.Size
		} else {
			if k1 > 0 {
				out = append(out, Match{i1, j1, k1})
			}
			i1, j1, k1 = x.A, x.B, x.Size
		}
	}
	if k1 > 0 {
		out = append(out, Match{i1, j1, k1})
	}
	out = append(out, Match{la, lb, 0})
	m.blocks, m.haveBlk = out, true
	return out
}

func calcRatio(matches, length int) float64 {
	if length > 0 {
		return 2.0 * float64(matches) / float64(length)
	}
	return 1.0
}

// Ratio is 2*M/T where M is the number of matched elements.
func (m *SequenceMatcher) Ratio() float64 {
	matches := 0
	for _, b := range m.GetMatchingBlocks() {
		matches += b.Size
	}
	return calcRatio(matches, len(m.a)+len(m.b))
}

// QuickRatio is an upper bound on Ratio computed from element counts.
func (m *SequenceMatcher) QuickRatio() float64 {
	full := map[rune]int{}
	for _, r := range m.b {
		full[r]++
	}
	avail := map[rune]int{}
	matches := 0
	for _, r := range m.a {
		numb, ok := avail[r]
		if !ok {
			numb = full[r]
		}
		avail[r] = numb - 1
		if numb > 0 {
			matches++
		}
	}
	return calcRatio(matches, len(m.a)+len(m.b))
}

// SequenceRatio is difflib.SequenceMatcher(None, a, b).ratio().
func SequenceRatio(a, b string) float64 { return NewSequenceMatcher(a, b).Ratio() }
