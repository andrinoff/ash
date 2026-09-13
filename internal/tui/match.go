package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The scoring follows fzf's (https://github.com/junegunn/fzf, MIT): a match
// earns points per character, more at word boundaries and in consecutive
// runs, and loses points for gaps. Using the same weights keeps the ranking
// familiar to anyone who uses fzf, and lets the tests check it against fzf.
const (
	scoreMatch        = 16
	scoreGapStart     = -3
	scoreGapExtension = -1

	bonusBoundary          = scoreMatch / 2
	bonusNonWord           = scoreMatch / 2
	bonusCamel123          = bonusBoundary + scoreGapExtension
	bonusConsecutive       = -(scoreGapStart + scoreGapExtension)
	bonusBoundaryWhite     = bonusBoundary + 2
	bonusBoundaryDelimiter = bonusBoundary + 1

	bonusFirstCharMultiplier = 2
)

type charClass uint8

const (
	classWhite charClass = iota
	classNonWord
	classDelimiter
	classLower
	classUpper
	classLetter
	classNumber
)

func classOf(r rune) charClass {
	if r < utf8.RuneSelf {
		switch {
		case r >= 'a' && r <= 'z':
			return classLower
		case r >= 'A' && r <= 'Z':
			return classUpper
		case r >= '0' && r <= '9':
			return classNumber
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f':
			return classWhite
		case strings.IndexByte("/,:;|", byte(r)) >= 0:
			return classDelimiter
		}
		return classNonWord
	}
	switch {
	case unicode.IsLower(r):
		return classLower
	case unicode.IsUpper(r):
		return classUpper
	case unicode.IsNumber(r):
		return classNumber
	case unicode.IsLetter(r):
		return classLetter
	case unicode.IsSpace(r):
		return classWhite
	}
	return classNonWord
}

// bonusFor is the bonus for matching a character of class cur that follows
// one of class prev. Like fzf 0.74, punctuation counts as starting a word
// too: the "-" of "ls -la" or the "/" of "cd /src" earns the boundary bonus.
func bonusFor(prev, cur charClass) int {
	if cur >= classNonWord {
		switch prev {
		case classWhite:
			return bonusBoundaryWhite
		case classDelimiter:
			return bonusBoundaryDelimiter
		case classNonWord:
			return bonusBoundary
		}
	}
	if prev == classLower && cur == classUpper || prev != classNumber && cur == classNumber {
		return bonusCamel123
	}
	switch cur {
	case classNonWord, classDelimiter:
		return bonusNonWord
	case classWhite:
		return bonusBoundaryWhite
	}
	return 0
}

func toLower(r rune) rune {
	if r < utf8.RuneSelf {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}
	return unicode.ToLower(r)
}

// termKind is the kind of one space-separated word of a query.
type termKind uint8

const (
	termFuzzy  termKind = iota // abc: a, b and c in that order
	termExact                  // 'abc: the substring abc
	termPrefix                 // ^abc: starts with abc
	termSuffix                 // abc$: ends with abc
	termEqual                  // ^abc$: is abc
)

type term struct {
	kind          termKind
	inv           bool // !abc: must not contain abc
	caseSensitive bool // smart case: only when the term has upper case
	text          []rune
}

// pattern is a parsed query. Items must match every term.
type pattern struct {
	terms    []term
	src      string
	sortable bool // some term scores matches; negations alone do not
}

// parsePattern splits a query into terms, using fzf's extended syntax:
// words separated by spaces must all match ("\ " is a literal space), and
// 'word, ^word, word$ and !word select exact, prefix, suffix and negated
// matching. Matching is case-insensitive unless the word has upper case.
func parsePattern(q string) pattern {
	p := pattern{src: q}
	for _, tok := range splitQuery(q) {
		t := term{kind: termFuzzy}
		if strings.HasPrefix(tok, "!") {
			t.inv = true
			t.kind = termExact
			tok = tok[1:]
		}
		if tok != "$" && strings.HasSuffix(tok, "$") {
			t.kind = termSuffix
			tok = tok[:len(tok)-1]
		}
		switch {
		case strings.HasPrefix(tok, "'"):
			if t.inv {
				t.kind = termFuzzy
			} else {
				t.kind = termExact
			}
			tok = tok[1:]
		case strings.HasPrefix(tok, "^"):
			if t.kind == termSuffix {
				t.kind = termEqual
			} else {
				t.kind = termPrefix
			}
			tok = tok[1:]
		}
		if tok == "" {
			continue
		}
		t.caseSensitive = strings.ToLower(tok) != tok
		for _, r := range tok {
			if !t.caseSensitive {
				r = toLower(r)
			}
			t.text = append(t.text, r)
		}
		p.terms = append(p.terms, t)
		p.sortable = p.sortable || !t.inv
	}
	return p
}

func splitQuery(q string) []string {
	var out []string
	var b strings.Builder
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\\' && i+1 < len(q) && q[i+1] == ' ':
			b.WriteByte(' ')
			i++
		case c == ' ':
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// empty reports whether the pattern matches everything.
func (p *pattern) empty() bool { return len(p.terms) == 0 }

// narrows reports whether every item matching the query next also matches
// prev, so filtering for next can start from prev's matches.
func narrows(prev, next string) bool {
	if prev == "" || !strings.HasPrefix(next, prev) {
		return false
	}
	// Negations and anchors can make a longer query match more.
	return !strings.ContainsAny(next, "!$\\")
}

// matcher holds scratch buffers so matching does not allocate.
type matcher struct {
	text  []rune
	bonus []int
	h, c  []int
	first []int
	pos   []int
}

// match scores text against every term of p. It returns ok=false when an
// item does not match. With withPos, positions holds the matched rune
// indices in ascending order; the slice is reused by the next call.
func (m *matcher) match(p *pattern, text string, withPos bool) (score int, positions []int, ok bool) {
	if withPos {
		m.pos = m.pos[:0]
	}
	for i := range p.terms {
		t := &p.terms[i]
		s, found := m.matchTerm(t, text, withPos && !t.inv)
		if found == t.inv {
			return 0, nil, false
		}
		if !t.inv {
			score += s
		}
	}
	if withPos {
		positions = sortUnique(m.pos)
	}
	return score, positions, true
}

func sortUnique(a []int) []int {
	// Insertion sort: there are only as many positions as query runes.
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
	out := a[:0]
	for i, v := range a {
		if i == 0 || v != a[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func (m *matcher) matchTerm(t *term, text string, withPos bool) (int, bool) {
	switch t.kind {
	case termFuzzy:
		return m.fuzzy(t, text, withPos)
	default:
		return m.literal(t, text, withPos)
	}
}

// isSubsequence is a quick allocation-free check that rejects most items
// before the scoring pass decodes them.
func isSubsequence(text string, pat []rune, caseSensitive bool) bool {
	pi := 0
	for _, r := range text {
		if !caseSensitive {
			r = toLower(r)
		}
		if r == pat[pi] {
			pi++
			if pi == len(pat) {
				return true
			}
		}
	}
	return false
}

// prepare decodes text into m.text (folded to lower case unless
// caseSensitive) and fills m.bonus with the bonus for each position.
func (m *matcher) prepare(text string, caseSensitive bool) {
	m.text = m.text[:0]
	m.bonus = m.bonus[:0]
	prev := classWhite
	for _, r := range text {
		cl := classOf(r)
		if !caseSensitive && cl == classUpper {
			r = toLower(r)
		}
		m.text = append(m.text, r)
		m.bonus = append(m.bonus, bonusFor(prev, cl))
		prev = cl
	}
}

func grow(s []int, n int) []int {
	if cap(s) < n {
		return make([]int, n, n+n/2)
	}
	return s[:n]
}

// fuzzy finds the best-scoring way to match the term's runes in order,
// with a Smith-Waterman style dynamic program over the window between the
// first possible start and the last possible end (fzf's algorithm v2).
func (m *matcher) fuzzy(t *term, text string, withPos bool) (int, bool) {
	pat := t.text
	M := len(pat)
	if !isSubsequence(text, pat, t.caseSensitive) {
		return 0, false
	}
	m.prepare(text, t.caseSensitive)
	T, B := m.text, m.bonus

	// First occurrence of each pattern rune, matched greedily, and the
	// last occurrence of the final pattern rune.
	F := grow(m.first, M)
	m.first = F
	pidx, last := 0, 0
	for i, r := range T {
		if r == pat[min(pidx, M-1)] {
			if pidx < M {
				F[pidx] = i
				pidx++
			}
			last = i
		}
	}
	f0 := F[0]
	width := last - f0 + 1
	H := grow(m.h, width*M)
	C := grow(m.c, width*M)
	m.h, m.c = H, C

	// Row 0: the first pattern rune, whose bonus counts double.
	maxScore, maxPos := 0, f0
	prevH, inGap := 0, false
	for j := f0; j <= last; j++ {
		col := j - f0
		if T[j] == pat[0] {
			s := scoreMatch + B[j]*bonusFirstCharMultiplier
			H[col], C[col] = s, 1
			if M == 1 && s > maxScore {
				maxScore, maxPos = s, j
				if B[j] >= bonusBoundary {
					break
				}
			}
			inGap = false
		} else {
			if inGap {
				H[col] = max(prevH+scoreGapExtension, 0)
			} else {
				H[col] = max(prevH+scoreGapStart, 0)
			}
			C[col] = 0
			inGap = true
		}
		prevH = H[col]
	}
	if M == 1 {
		if withPos {
			m.pos = append(m.pos, maxPos)
		}
		return maxScore, true
	}

	for i := 1; i < M; i++ {
		row := i * width
		pc := pat[i]
		start := F[i] - f0
		for col := 0; col < start; col++ {
			H[row+col], C[row+col] = 0, 0
		}
		inGap := false
		for j := F[i]; j <= last; j++ {
			col := j - f0
			left := H[row+col-1]
			if col == start {
				left = 0
			}
			var s1, s2, consecutive int
			if inGap {
				s2 = left + scoreGapExtension
			} else {
				s2 = left + scoreGapStart
			}
			if T[j] == pc {
				s1 = H[row-width+col-1] + scoreMatch
				b := B[j]
				consecutive = C[row-width+col-1] + 1
				if consecutive > 1 {
					fb := B[j-consecutive+1]
					if b >= bonusBoundary && b > fb {
						// A boundary inside a run starts a new chunk.
						consecutive = 1
					} else {
						b = max(b, bonusConsecutive, fb)
					}
				}
				if s1+b < s2 {
					s1 += B[j]
					consecutive = 0
				} else {
					s1 += b
				}
			}
			C[row+col] = consecutive
			inGap = s1 < s2
			s := max(s1, s2, 0)
			if i == M-1 && s > maxScore {
				maxScore, maxPos = s, j
			}
			H[row+col] = s
		}
	}

	if withPos {
		// Walk back from the best end, preferring matches over gaps.
		i, j := M-1, maxPos
		preferMatch := true
		for {
			I := i * width
			j0 := j - f0
			s := H[I+j0]
			var s1, s2 int
			if i > 0 && j >= F[i] {
				s1 = H[I-width+j0-1]
			}
			if j > F[i] {
				s2 = H[I+j0-1]
			}
			if s > s1 && (s > s2 || s == s2 && preferMatch) {
				m.pos = append(m.pos, j)
				if i == 0 {
					break
				}
				i--
			}
			preferMatch = C[I+j0] > 1 || I+width+j0+1 < len(C) && C[I+width+j0+1] > 0
			j--
		}
	}
	return maxScore, true
}

// literal handles the exact, prefix, suffix and equal term kinds.
func (m *matcher) literal(t *term, text string, withPos bool) (int, bool) {
	pat := t.text
	m.prepare(text, t.caseSensitive)
	T := m.text
	n, k := len(T), len(pat)
	if k > n {
		return 0, false
	}
	// Leading and trailing white space is ignored by the anchors.
	lo, hi := 0, n
	for lo < n && unicode.IsSpace(T[lo]) && !unicode.IsSpace(pat[0]) {
		lo++
	}
	for hi > lo && unicode.IsSpace(T[hi-1]) && !unicode.IsSpace(pat[k-1]) {
		hi--
	}
	start := -1
	switch t.kind {
	case termPrefix:
		if hi-lo >= k && runesEqual(T[lo:lo+k], pat) {
			start = lo
		}
	case termSuffix:
		if hi-lo >= k && runesEqual(T[hi-k:hi], pat) {
			start = hi - k
		}
	case termEqual:
		if hi-lo == k && runesEqual(T[lo:hi], pat) {
			start = lo
		}
	case termExact:
		// The occurrence whose first rune has the highest bonus.
		best := -1
		for i := 0; i+k <= n; i++ {
			if !runesEqual(T[i:i+k], pat) {
				continue
			}
			if m.bonus[i] > best {
				best, start = m.bonus[i], i
			}
			if m.bonus[i] >= bonusBoundary {
				break
			}
		}
	}
	if start < 0 {
		return 0, false
	}
	if t.kind == termEqual {
		if withPos {
			for i := start; i < start+k; i++ {
				m.pos = append(m.pos, i)
			}
		}
		return (scoreMatch+bonusBoundaryWhite)*k + (bonusFirstCharMultiplier-1)*bonusBoundaryWhite, true
	}
	return m.scoreRun(start, k, withPos), true
}

func runesEqual(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// scoreRun scores a contiguous match of k runes at start of m.text.
func (m *matcher) scoreRun(start, k int, withPos bool) int {
	score, firstBonus := 0, 0
	for i := start; i < start+k; i++ {
		if withPos {
			m.pos = append(m.pos, i)
		}
		score += scoreMatch
		b := m.bonus[i]
		if i == start {
			firstBonus = b
			score += b * bonusFirstCharMultiplier
			continue
		}
		if b >= bonusBoundary && b > firstBonus {
			firstBonus = b
		}
		score += max(b, firstBonus, bonusConsecutive)
	}
	return score
}
