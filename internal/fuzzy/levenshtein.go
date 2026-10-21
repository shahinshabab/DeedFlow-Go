package fuzzy

import "math"

// The routines below reproduce python-Levenshtein (0.26+, rapidfuzz based) as
// fuzzywuzzy uses it: Levenshtein.ratio, and the matching blocks derived from
// Levenshtein.editops, whose tie-breaking decides partial_ratio's windows.

// lcsLen is the length of the longest common subsequence of a and b.
func lcsLen(a, b []rune) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else if prev[j] >= cur[j-1] {
				cur[j] = prev[j]
			} else {
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// LevRatio is Levenshtein.ratio(a, b): the normalized indel similarity
// 2*LCS/(len(a)+len(b)), 1.0 for two empty strings.
func LevRatio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	total := len(ra) + len(rb)
	if total == 0 {
		return 1.0
	}
	return 2.0 * float64(lcsLen(ra, rb)) / float64(total)
}

type editOp struct {
	kind     byte // 'd', 'i', 'r'
	src, dst int
}

// editops lists the edits turning a into b, in order, the way
// rapidfuzz's Levenshtein editops does.
func editops(a, b []rune) []editOp {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	a, b = a[prefix:], b[prefix:]
	suffix := 0
	for suffix < len(a) && suffix < len(b) && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	a, b = a[:len(a)-suffix], b[:len(b)-suffix]
	n, m := len(a), len(b)

	// D[i][j] = edit distance between a[:i] and b[:j].
	d := make([][]int32, n+1)
	for i := range d {
		d[i] = make([]int32, m+1)
		d[i][0] = int32(i)
	}
	for j := 0; j <= m; j++ {
		d[0][j] = int32(j)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost := int32(1)
			if a[i-1] == b[j-1] {
				cost = 0
			}
			v := d[i-1][j-1] + cost
			if x := d[i-1][j] + 1; x < v {
				v = x
			}
			if x := d[i][j-1] + 1; x < v {
				v = x
			}
			d[i][j] = v
		}
	}

	var rev []editOp
	col, row := n, m
	for row > 0 && col > 0 {
		if d[col][row]-d[col-1][row] == 1 {
			col--
			rev = append(rev, editOp{'d', col + prefix, row + prefix})
			continue
		}
		row--
		if row > 0 && d[col][row]-d[col-1][row] == -1 {
			rev = append(rev, editOp{'i', col + prefix, row + prefix})
			continue
		}
		col--
		if a[col] != b[row] {
			rev = append(rev, editOp{'r', col + prefix, row + prefix})
		}
	}
	for col > 0 {
		col--
		rev = append(rev, editOp{'d', col + prefix, row + prefix})
	}
	for row > 0 {
		row--
		rev = append(rev, editOp{'i', col + prefix, row + prefix})
	}
	ops := make([]editOp, len(rev))
	for i, op := range rev {
		ops[len(rev)-1-i] = op
	}
	return ops
}

// LevMatchingBlocks is Levenshtein.matching_blocks(editops(a, b), a, b): the
// runs of unchanged text, ending with the (len(a), len(b), 0) sentinel.
func LevMatchingBlocks(a, b []rune) []Match {
	srcPos, dstPos := 0, 0
	var blocks []Match
	for _, op := range editops(a, b) {
		if srcPos < op.src && dstPos < op.dst {
			blocks = append(blocks, Match{srcPos, dstPos, op.src - srcPos})
			srcPos, dstPos = op.src, op.dst
		}
		switch op.kind {
		case 'd':
			srcPos++
		case 'i':
			dstPos++
		case 'r':
			srcPos++
			dstPos++
		}
	}
	if len(a)-srcPos != 0 {
		blocks = append(blocks, Match{srcPos, dstPos, len(a) - srcPos})
	}
	blocks = append(blocks, Match{len(a), len(b), 0})
	return blocks
}

func pyRound(x float64) int { return int(math.RoundToEven(x)) }

// Ratio is fuzzywuzzy's fuzz.ratio(a, b), an integer 0-100.
func Ratio(a, b string) int {
	if a == b {
		return 100
	}
	if a == "" || b == "" {
		return 0
	}
	return pyRound(100 * LevRatio(a, b))
}

// PartialRatio is fuzzywuzzy's fuzz.partial_ratio(a, b): the ratio of the
// best-matching window of the longer string against the shorter one.
func PartialRatio(s1, s2 string) int {
	if s1 == s2 {
		return 100
	}
	if s1 == "" || s2 == "" {
		return 0
	}
	a, b := []rune(s1), []rune(s2)
	shorter, longer := a, b
	if len(a) > len(b) {
		shorter, longer = b, a
	}
	best := 0.0
	for _, block := range LevMatchingBlocks(shorter, longer) {
		longStart := block.B - block.A
		if longStart < 0 {
			longStart = 0
		}
		longEnd := longStart + len(shorter)
		if longEnd > len(longer) {
			longEnd = len(longer)
		}
		window := longer[longStart:longEnd]
		total := len(shorter) + len(window)
		r := 1.0
		if total > 0 {
			r = 2.0 * float64(lcsLen(shorter, window)) / float64(total)
		}
		if r > .995 {
			return 100
		}
		if r > best {
			best = r
		}
	}
	return pyRound(100 * best)
}

// ExtractOne is fuzzywuzzy's process.extractOne(query, choices, processor=None,
// scorer=fuzz.ratio): the first choice with the highest score.
func ExtractOne(query string, choices []string) (string, int, bool) {
	if len(choices) == 0 {
		return "", 0, false
	}
	best, bestScore := choices[0], Ratio(query, choices[0])
	for _, c := range choices[1:] {
		if s := Ratio(query, c); s > bestScore {
			best, bestScore = c, s
		}
	}
	return best, bestScore, true
}
