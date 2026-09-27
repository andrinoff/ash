package shell

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// matchPattern reports whether s matches the glob pattern pat. The
// pattern supports *, ?, [...] (with ! or ^ negation, ranges and
// [:class:]) and backslash escapes.
func matchPattern(pat, s string) bool {
	return match(pat, s, false)
}

// matchPatternFold is matchPattern ignoring case.
func matchPatternFold(pat, s string) bool {
	return match(pat, s, true)
}

func match(p, s string, fold bool) bool {
	px, sx := 0, 0
	starPx, starSx := -1, -1
	for px < len(p) || sx < len(s) {
		if px < len(p) {
			c := p[px]
			switch c {
			case '*':
				for px < len(p) && p[px] == '*' {
					px++
				}
				if px == len(p) {
					return true
				}
				starPx, starSx = px, sx
				continue
			case '?':
				if sx < len(s) {
					_, n := utf8.DecodeRuneInString(s[sx:])
					px++
					sx += n
					continue
				}
			case '[':
				if sx < len(s) {
					r, n := utf8.DecodeRuneInString(s[sx:])
					ok, plen := matchClass(p[px:], r, fold)
					if plen > 0 {
						if ok {
							px += plen
							sx += n
							continue
						}
						break
					}
					// not a valid class: literal '['
					if s[sx] == '[' {
						px++
						sx++
						continue
					}
				}
			case '\\':
				if px+1 < len(p) && sx < len(s) {
					pr, pn := utf8.DecodeRuneInString(p[px+1:])
					sr, sn := utf8.DecodeRuneInString(s[sx:])
					if pr == sr || fold && unicode.SimpleFold(pr) == sr || fold && unicode.ToLower(pr) == unicode.ToLower(sr) {
						px += 1 + pn
						sx += sn
						continue
					}
				}
			default:
				if sx < len(s) {
					pr, pn := utf8.DecodeRuneInString(p[px:])
					sr, sn := utf8.DecodeRuneInString(s[sx:])
					if pr == sr || fold && unicode.ToLower(pr) == unicode.ToLower(sr) {
						px += pn
						sx += sn
						continue
					}
				}
			}
		}
		if starPx >= 0 && starSx < len(s) {
			_, n := utf8.DecodeRuneInString(s[starSx:])
			starSx += n
			px, sx = starPx, starSx
			continue
		}
		return false
	}
	return true
}

// matchClass matches r against the bracket expression at the start of
// p. It returns the match result and the length of the expression, or
// length 0 if p does not hold a valid bracket expression.
func matchClass(p string, r rune, fold bool) (bool, int) {
	i := 1
	negate := false
	if i < len(p) && (p[i] == '!' || p[i] == '^') {
		negate = true
		i++
	}
	matched := false
	first := true
	lr := r
	if fold {
		lr = unicode.ToLower(r)
	}
	for i < len(p) {
		if p[i] == ']' && !first {
			if negate {
				return !matched, i + 1
			}
			return matched, i + 1
		}
		first = false
		if p[i] == '[' && i+1 < len(p) && p[i+1] == ':' {
			end := strings.Index(p[i+2:], ":]")
			if end >= 0 {
				class := p[i+2 : i+2+end]
				if classMatch(class, r) {
					matched = true
				}
				i += end + 4
				continue
			}
		}
		var lo rune
		var n int
		if p[i] == '\\' && i+1 < len(p) {
			lo, n = utf8.DecodeRuneInString(p[i+1:])
			n++
		} else {
			lo, n = utf8.DecodeRuneInString(p[i:])
		}
		i += n
		hi := lo
		if i+1 < len(p) && p[i] == '-' && p[i+1] != ']' {
			var m int
			if p[i+1] == '\\' && i+2 < len(p) {
				hi, m = utf8.DecodeRuneInString(p[i+2:])
				m++
			} else {
				hi, m = utf8.DecodeRuneInString(p[i+1:])
			}
			i += 1 + m
		}
		if lo <= r && r <= hi {
			matched = true
		} else if fold {
			llo, lhi := unicode.ToLower(lo), unicode.ToLower(hi)
			if llo <= lr && lr <= lhi {
				matched = true
			}
		}
	}
	return false, 0
}

func classMatch(class string, r rune) bool {
	switch class {
	case "alpha":
		return unicode.IsLetter(r)
	case "digit":
		return r >= '0' && r <= '9'
	case "alnum":
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	case "upper":
		return unicode.IsUpper(r)
	case "lower":
		return unicode.IsLower(r)
	case "space":
		return unicode.IsSpace(r)
	case "blank":
		return r == ' ' || r == '\t'
	case "punct":
		return unicode.IsPunct(r) || unicode.IsSymbol(r)
	case "print":
		return unicode.IsPrint(r)
	case "graph":
		return unicode.IsGraphic(r) && !unicode.IsSpace(r)
	case "cntrl":
		return unicode.IsControl(r)
	case "xdigit":
		return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
	case "word":
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
	}
	return false
}

// hasMeta reports whether a pattern has unescaped glob characters.
func hasMeta(p string) bool {
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '\\':
			i++
		case '*', '?', '[':
			return true
		}
	}
	return false
}

func unescapePattern(p string) string {
	if !strings.Contains(p, "\\") {
		return p
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' && i+1 < len(p) {
			i++
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

// collapseStars reports whether a pattern segment has "**" made of
// unescaped stars (so c\** from c'*'* is not one), and returns the
// segment with each such run of stars reduced to one.
func collapseStars(seg string) (string, bool) {
	var b strings.Builder
	found := false
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if c == '\\' && i+1 < len(seg) {
			b.WriteByte(c)
			b.WriteByte(seg[i+1])
			i++
			continue
		}
		if c == '*' {
			for i+1 < len(seg) && seg[i+1] == '*' {
				found = true
				i++
			}
		}
		b.WriteByte(c)
	}
	return b.String(), found
}

// glob expands a pattern against the file system. Results are relative
// if the pattern is relative.
func (sh *Shell) glob(pat string) []string {
	if pat == "" {
		return nil
	}
	abs := strings.HasPrefix(pat, "/")
	segs := strings.Split(strings.TrimPrefix(pat, "/"), "/")
	// fish-style "**.go" means "**/*.go"
	var expanded []string
	for _, s := range segs {
		if s != "**" {
			if c, ok := collapseStars(s); ok {
				expanded = append(expanded, "**", c)
				continue
			}
		}
		expanded = append(expanded, s)
	}
	segs = expanded
	base := sh.dir
	prefix := ""
	if abs {
		base = "/"
		prefix = "/"
	}
	var results []string
	var walk func(dir, rel string, i int)
	walk = func(dir, rel string, i int) {
		if i == len(segs) {
			results = append(results, rel)
			return
		}
		seg := segs[i]
		join := func(name string) (string, string) {
			if rel == "" {
				return filepath.Join(dir, name), prefix + name
			}
			return filepath.Join(dir, name), rel + "/" + name
		}
		if seg == "" {
			// trailing slash or "//"
			if i == len(segs)-1 {
				if rel != "" {
					results = append(results, rel+"/")
				}
				return
			}
			walk(dir, rel, i+1)
			return
		}
		if seg == "**" {
			// A final ** matches everything below dir (like fish and
			// bash's globstar); otherwise it stands for zero or more
			// directories.
			last := i == len(segs)-1
			if !last {
				walk(dir, rel, i+1)
			}
			var rec func(d, r string, depth int)
			rec = func(d, r string, depth int) {
				if depth > 64 {
					return
				}
				ents, err := os.ReadDir(d)
				if err != nil {
					return
				}
				for _, e := range ents {
					name := e.Name()
					if name[0] == '.' {
						continue
					}
					nd := filepath.Join(d, name)
					nr := name
					if r != "" {
						nr = r + "/" + name
					} else if prefix != "" {
						nr = prefix + name
					}
					if last {
						results = append(results, nr)
					}
					// Symlinked directories are not followed (e.IsDir
					// is false for them), which also avoids loops.
					if !e.IsDir() {
						continue
					}
					if !last {
						walk(nd, nr, i+1)
					}
					rec(nd, nr, depth+1)
				}
			}
			rec(dir, rel, 0)
			return
		}
		if !hasMeta(seg) {
			name := unescapePattern(seg)
			d, r := join(name)
			if i == len(segs)-1 {
				if _, err := os.Lstat(d); err == nil {
					results = append(results, r)
				}
				return
			}
			walk(d, r, i+1)
			return
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		dotOK := strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "\\.")
		for _, e := range ents {
			name := e.Name()
			if name[0] == '.' && !dotOK {
				continue
			}
			if !matchPattern(seg, name) {
				continue
			}
			d, r := join(name)
			if i < len(segs)-1 {
				if !e.IsDir() {
					if st, err := os.Stat(d); err != nil || !st.IsDir() {
						continue
					}
				}
			}
			walk(d, r, i+1)
		}
	}
	walk(base, "", 0)
	if len(results) > 1 {
		// "**" can reach the same path twice
		seen := make(map[string]bool, len(results))
		out := results[:0]
		for _, r := range results {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
		results = out
		sort.Slice(results, func(a, b int) bool { return naturalLess(results[a], results[b]) })
	}
	return results
}

// naturalLess compares strings the way fish sorts glob results: letter
// case is ignored and runs of digits compare by value, so "file2" <
// "File3" < "file10". Case and leading zeros only break ties between
// names that are otherwise equal.
func naturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if isDigitByte(a[i]) && isDigitByte(b[j]) {
			si := i
			for i < len(a) && isDigitByte(a[i]) {
				i++
			}
			sj := j
			for j < len(b) && isDigitByte(b[j]) {
				j++
			}
			na := strings.TrimLeft(a[si:i], "0")
			nb := strings.TrimLeft(b[sj:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		ra, sa := utf8.DecodeRuneInString(a[i:])
		rb, sb := utf8.DecodeRuneInString(b[j:])
		if la, lb := unicode.ToLower(ra), unicode.ToLower(rb); la != lb {
			return la < lb
		}
		i += sa
		j += sb
	}
	if i < len(a) || j < len(b) {
		// One is a prefix of the other.
		return i == len(a)
	}
	return a < b
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// NaturalSort sorts strings with naturalLess.
func NaturalSort(s []string) { sort.Slice(s, func(a, b int) bool { return naturalLess(s[a], s[b]) }) }
