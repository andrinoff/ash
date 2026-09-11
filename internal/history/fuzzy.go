package history

import (
	"slices"
	"unicode"
	"unicode/utf8"
)

// Match is a history entry found by Fuzzy.
type Match struct {
	Entry
	Score     int   // how well it matched; higher is better
	Positions []int // the indices of the matched runes of Cmd, ascending
}

// Fuzzy returns the commands that contain the runes of q in order, best
// match first, ties going to the newest, and at most limit of them if limit
// is positive. A match scores for every rune, more for runes that follow
// one another, start a word or start the command, and loses for gaps. Case
// is ignored unless q has upper-case letters. With an empty q it returns
// the most recent commands.
func (s *Store) Fuzzy(q string, limit int) []Match {
	<-s.loaded
	s.mu.RLock()
	defer s.mu.RUnlock()
	if q == "" {
		n := len(s.items) - s.dead
		if limit > 0 && limit < n {
			n = limit
		}
		out := make([]Match, 0, n)
		for i := len(s.items) - 1; i >= 0 && len(out) < n; i-- {
			if s.items[i].cmd != "" {
				out = append(out, Match{Entry: s.items[i].entry()})
			}
		}
		return out
	}
	m := newFuzzyMatcher(q)
	r := ranking{limit: limit}
	for i := len(s.items) - 1; i >= 0; i-- {
		if cmd := s.items[i].cmd; cmd != "" {
			if score, _, ok := m.match(cmd, false); ok {
				r.add(hit{int32(score), int32(i)})
			}
		}
	}
	hits := r.sorted()
	out := make([]Match, len(hits))
	for k, h := range hits {
		it := &s.items[h.idx]
		_, pos, _ := m.match(it.cmd, true)
		out[k] = Match{Entry: it.entry(), Score: int(h.score), Positions: pos}
	}
	return out
}

// hit is a matching entry: its score and its slot.
type hit struct{ score, idx int32 }

// better reports whether a ranks before b: it scored higher, or as high
// and is newer.
func (a hit) better(b hit) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	return a.idx > b.idx
}

// ranking collects hits, keeping only the best limit of them if limit is
// positive.
type ranking struct {
	limit int
	hits  []hit // with a limit, a heap with the worst hit first
}

func (r *ranking) add(h hit) {
	switch {
	case r.limit <= 0:
		r.hits = append(r.hits, h)
	case len(r.hits) < r.limit:
		r.hits = append(r.hits, h)
		for i := len(r.hits) - 1; i > 0; {
			p := (i - 1) / 2
			if !r.hits[p].better(r.hits[i]) {
				break
			}
			r.hits[p], r.hits[i] = r.hits[i], r.hits[p]
			i = p
		}
	case h.better(r.hits[0]):
		r.hits[0] = h
		for i := 0; ; {
			w := 2*i + 1
			if w >= len(r.hits) {
				break
			}
			if c := w + 1; c < len(r.hits) && r.hits[w].better(r.hits[c]) {
				w = c
			}
			if !r.hits[i].better(r.hits[w]) {
				break
			}
			r.hits[i], r.hits[w] = r.hits[w], r.hits[i]
			i = w
		}
	}
}

func (r *ranking) sorted() []hit {
	slices.SortFunc(r.hits, func(a, b hit) int {
		if a.better(b) {
			return -1
		}
		return 1
	})
	return r.hits
}

// Scores, after fzf's.
const (
	scoreMatch        = 16
	scoreGapStart     = -3
	scoreGapExtension = -1

	// A match at the start of a word.
	bonusBoundary = scoreMatch / 2
	// A match on a non-word character such as "-" or ".".
	bonusNonWord = scoreMatch / 2
	// A match at a camelCase hump, or on the first digit of a number.
	bonusCamel123 = bonusBoundary + scoreGapExtension
	// The least a match right after the previous one gets: what a gap of
	// two costs.
	bonusConsecutive = -(scoreGapStart + scoreGapExtension)
	// Word starts after whitespace, and after a delimiter such as "/".
	bonusBoundaryWhite     = bonusBoundary + 2
	bonusBoundaryDelimiter = bonusBoundary + 1
	// The first rune of the query counts its bonus twice.
	bonusFirstCharMultiplier = 2
	// A match at the very start of the command.
	bonusPrefix = scoreMatch / 2

	// Commands whose search space is bigger than this are scored greedily.
	maxCells = 1 << 17
	negInf   = -(1 << 30)
)

type charClass uint8

const (
	charWhite charClass = iota
	charNonWord
	charDelimiter
	charLower
	charUpper
	charLetter
	charNumber
)

var asciiClasses [utf8.RuneSelf]charClass

func init() {
	for c := range asciiClasses {
		switch {
		case 'a' <= c && c <= 'z':
			asciiClasses[c] = charLower
		case 'A' <= c && c <= 'Z':
			asciiClasses[c] = charUpper
		case '0' <= c && c <= '9':
			asciiClasses[c] = charNumber
		case c == ' ' || '\t' <= c && c <= '\r':
			asciiClasses[c] = charWhite
		case c == '/' || c == ',' || c == ':' || c == ';' || c == '|':
			asciiClasses[c] = charDelimiter
		default:
			asciiClasses[c] = charNonWord
		}
	}
}

func classOf(r rune) charClass {
	switch {
	case r < utf8.RuneSelf:
		return asciiClasses[r]
	case unicode.IsLower(r):
		return charLower
	case unicode.IsUpper(r):
		return charUpper
	case unicode.IsNumber(r):
		return charNumber
	case unicode.IsLetter(r):
		return charLetter
	case unicode.IsSpace(r):
		return charWhite
	}
	return charNonWord
}

// bonusFor is the bonus for matching a rune of class c after one of class
// prev.
func bonusFor(prev, c charClass) int16 {
	if c > charNonWord {
		switch prev {
		case charWhite:
			return bonusBoundaryWhite
		case charDelimiter:
			return bonusBoundaryDelimiter
		case charNonWord:
			return bonusBoundary
		}
	}
	if prev == charLower && c == charUpper || prev != charNumber && c == charNumber {
		return bonusCamel123
	}
	switch c {
	case charNonWord, charDelimiter:
		return bonusNonWord
	case charWhite:
		return bonusBoundaryWhite
	}
	return 0
}

func foldRune(r rune) rune {
	if r < utf8.RuneSelf {
		if 'A' <= r && r <= 'Z' {
			r += 'a' - 'A'
		}
		return r
	}
	return unicode.ToLower(r)
}

// fuzzyMatcher scores commands against a query.
type fuzzyMatcher struct {
	pat   []rune // the query, lower-cased if case is ignored
	quick string // pat as bytes if it is ASCII, for a quick first test
	fold  bool   // ignore case

	// Scratch space reused from one command to the next.
	text, low  []rune // the command, and lower-cased if case is ignored
	bonus      []int16
	fwd, bwd   []int32 // the first and last place each query rune can match
	off        []int32 // where each query rune's cells start
	score      []int32 // the best score with the query rune matched here
	run        []int32 // how many matches in a row end here
	from       []int32 // where the previous query rune matched
	posScratch []int
}

func newFuzzyMatcher(q string) *fuzzyMatcher {
	m := &fuzzyMatcher{fold: !hasUpper(q)}
	for _, r := range q {
		if m.fold {
			r = foldRune(r)
		}
		m.pat = append(m.pat, r)
	}
	if isASCII(q) {
		m.quick = string(m.pat)
	}
	n := len(m.pat)
	m.fwd, m.bwd, m.off = make([]int32, n), make([]int32, n), make([]int32, n)
	return m
}

// mayMatch is a quick test for ASCII queries, on the bytes of s: false
// means s can't match.
func (m *fuzzyMatcher) mayMatch(s string) bool {
	q, k := m.quick, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= utf8.RuneSelf {
			if m.fold {
				return true // some non-ASCII letters lower-case to ASCII ones
			}
			continue
		}
		if m.fold && 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c == q[k] {
			if k++; k == len(q) {
				return true
			}
		}
	}
	return false
}

// match scores s. It reports false if s doesn't contain the query's runes
// in order. It returns the positions of the best match only if withPos is
// set.
func (m *fuzzyMatcher) match(s string, withPos bool) (int, []int, bool) {
	if m.quick != "" && !m.mayMatch(s) {
		return 0, nil, false
	}
	m.text, m.low = m.text[:0], m.low[:0]
	for _, r := range s {
		m.text = append(m.text, r)
		if m.fold {
			r = foldRune(r)
		}
		m.low = append(m.low, r)
	}
	t, p := m.low, m.pat
	n, k := len(t), len(p)
	j := 0
	for i := 0; i < k; i++ {
		for j < n && t[j] != p[i] {
			j++
		}
		if j == n {
			return 0, nil, false
		}
		m.fwd[i] = int32(j)
		j++
	}
	j = n - 1
	for i := k - 1; i >= 0; i-- {
		for t[j] != p[i] {
			j--
		}
		m.bwd[i] = int32(j)
		j--
	}
	lo, hi := int(m.fwd[0]), int(m.bwd[k-1])
	m.bonus = grow(m.bonus, n)
	prev := charWhite
	if lo > 0 {
		prev = classOf(m.text[lo-1])
	}
	for j := lo; j <= hi; j++ {
		c := classOf(m.text[j])
		m.bonus[j] = bonusFor(prev, c)
		prev = c
	}
	cells := 0
	for i := 0; i < k; i++ {
		m.off[i] = int32(cells)
		cells += int(m.bwd[i]-m.fwd[i]) + 1
	}
	if cells > maxCells {
		return m.greedy(withPos)
	}
	return m.best(cells, withPos)
}

// best finds the best-scoring way to match the query, like the
// Smith-Waterman alignment fzf uses: each query rune's cells hold the best
// score with the rune matched there, coming either right after the previous
// rune's match or after a gap.
func (m *fuzzyMatcher) best(cells int, withPos bool) (int, []int, bool) {
	t, p, bonus := m.low, m.pat, m.bonus
	k := len(p)
	m.score, m.run = grow(m.score, cells), grow(m.run, cells)
	if withPos {
		m.from = grow(m.from, cells)
	}
	score, run, from := m.score, m.run, m.from

	f0, b0 := int(m.fwd[0]), int(m.bwd[0])
	for j := f0; j <= b0; j++ {
		c := j - f0
		if t[j] != p[0] {
			score[c], run[c] = negInf, 0
			continue
		}
		sc := int32(scoreMatch + int(bonus[j])*bonusFirstCharMultiplier)
		if j == 0 {
			sc += bonusPrefix
		}
		score[c], run[c] = sc, 1
	}
	for i := 1; i < k; i++ {
		fp, bp, po := int(m.fwd[i-1]), int(m.bwd[i-1]), int(m.off[i-1])
		fi, bi, ro := int(m.fwd[i]), int(m.bwd[i]), int(m.off[i])
		// gap is the best score of the previous rune matched at fp..j-2,
		// less what the gap from there costs, but not below zero: as in
		// fzf, a long gap costs at most what the match had scored so far.
		gap, gapFrom := int32(negInf), int32(-1)
		for j := fp + 1; j <= bi; j++ {
			if gap > negInf {
				gap = max(gap+scoreGapExtension, 0)
			}
			if q := j - 2; q >= fp && q <= bp {
				if v := score[po+q-fp]; v > negInf && v+scoreGapStart >= gap {
					gap, gapFrom = max(v+scoreGapStart, 0), int32(q)
				}
			}
			if j < fi {
				continue
			}
			c := ro + j - fi
			score[c], run[c] = negInf, 0
			if t[j] != p[i] {
				continue
			}
			best, r, src := int32(negInf), int32(0), int32(-1)
			if q := j - 1; q <= bp {
				if v := score[po+q-fp]; v > negInf {
					// Consecutive matches get at least the bonus of the
					// first one, unless a new word starts here.
					b, n := bonus[j], run[po+q-fp]
					fb := bonus[j-int(n)]
					if b >= bonusBoundary && b > fb {
						r = 1
					} else {
						b = max(b, bonusConsecutive, fb)
						r = n + 1
					}
					best, src = v+scoreMatch+int32(b), int32(q)
				}
			}
			if gap > negInf {
				if v := gap + scoreMatch + int32(bonus[j]); v > best {
					best, r, src = v, 1, gapFrom
				}
			}
			score[c], run[c] = best, r
			if withPos {
				from[c] = src
			}
		}
	}
	fl, bl, lo := int(m.fwd[k-1]), int(m.bwd[k-1]), int(m.off[k-1])
	best, end := int32(negInf), -1
	for j := fl; j <= bl; j++ {
		if v := score[lo+j-fl]; v > best {
			best, end = v, j
		}
	}
	if end < 0 {
		return 0, nil, false
	}
	if !withPos {
		return int(best), nil, true
	}
	pos := make([]int, k)
	for i := k - 1; i >= 0; i-- {
		pos[i] = end
		if i > 0 {
			end = int(from[int(m.off[i])+end-int(m.fwd[i])])
		}
	}
	return int(best), pos, true
}

// greedy scores the tightest match that ends where the earliest one does,
// for commands too long to search fully.
func (m *fuzzyMatcher) greedy(withPos bool) (int, []int, bool) {
	t, p := m.low, m.pat
	k := len(p)
	m.posScratch = grow(m.posScratch, k)
	pos := m.posScratch
	j := int(m.fwd[k-1])
	for i := k - 1; i >= 0; i-- {
		for t[j] != p[i] {
			j--
		}
		pos[i] = j
		j--
	}
	score, fb := 0, int16(0)
	for i, j := range pos {
		b := m.bonus[j]
		switch {
		case i == 0:
			score += scoreMatch + int(b)*bonusFirstCharMultiplier
			if j == 0 {
				score += bonusPrefix
			}
			fb = b
		case j == pos[i-1]+1:
			if b >= bonusBoundary && b > fb {
				fb = b
			} else {
				b = max(b, bonusConsecutive, fb)
			}
			score += scoreMatch + int(b)
		default:
			score = max(score+scoreGapStart+(j-pos[i-1]-2)*scoreGapExtension, 0) + scoreMatch + int(b)
			fb = b
		}
	}
	if !withPos {
		return score, nil, true
	}
	return score, slices.Clone(pos), true
}

func grow[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	return s[:n]
}
