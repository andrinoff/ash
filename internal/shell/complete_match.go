package shell

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Match tiers, best first. Like fish, only candidates of the best tier
// that anything reached are offered.
const (
	tierPrefix     = iota // starts with what was typed
	tierPrefixFold        // ... ignoring case
	tierSubstr            // contains it
	tierSubstrFold        // ... ignoring case
	tierSubseq            // contains its characters in order
	tierNone
)

// matcher matches candidates against the typed text.
type matcher struct {
	needle string
	lower  string // lower-cased needle, computed on demand
	fuzzy  bool   // allow substring and subsequence matches
	ascii  bool
}

func newMatcher(needle string, fuzzy bool) *matcher {
	m := &matcher{needle: needle, fuzzy: fuzzy, ascii: true}
	for i := 0; i < len(needle); i++ {
		if needle[i] >= utf8.RuneSelf {
			m.ascii = false
			break
		}
	}
	return m
}

// tier returns how well s matches, or tierNone. Tiers worse than max are
// not computed.
func (m *matcher) tier(s string, max int) int {
	n := m.needle
	if len(n) == 0 || strings.HasPrefix(s, n) {
		return tierPrefix
	}
	if max < tierPrefixFold {
		return tierNone
	}
	if hasPrefixFold(s, n) {
		return tierPrefixFold
	}
	if !m.fuzzy || max < tierSubstr {
		return tierNone
	}
	if strings.Contains(s, n) {
		return tierSubstr
	}
	if max < tierSubstrFold {
		return tierNone
	}
	if m.lower == "" {
		m.lower = strings.ToLower(n)
	}
	if containsFold(s, m.lower, m.ascii) {
		return tierSubstrFold
	}
	if max < tierSubseq {
		return tierNone
	}
	if isSubsequence(n, s) {
		return tierSubseq
	}
	return tierNone
}

// hasPrefixFold reports whether s starts with prefix, ignoring case.
func hasPrefixFold(s, prefix string) bool {
	for i := 0; i < len(prefix); i++ {
		if prefix[i] >= utf8.RuneSelf {
			return hasPrefixFoldSlow(s, prefix)
		}
	}
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		a, b := s[i], prefix[i]
		if a == b {
			continue
		}
		if 'A' <= a && a <= 'Z' {
			a += 'a' - 'A'
		}
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

func hasPrefixFoldSlow(s, prefix string) bool {
	for prefix != "" {
		if s == "" {
			return false
		}
		r1, n1 := utf8.DecodeRuneInString(s)
		r2, n2 := utf8.DecodeRuneInString(prefix)
		if r1 != r2 && unicode.ToLower(r1) != unicode.ToLower(r2) {
			return false
		}
		s, prefix = s[n1:], prefix[n2:]
	}
	return true
}

// containsFold reports whether s contains lower (already lower-cased),
// ignoring case.
func containsFold(s, lower string, ascii bool) bool {
	if ascii {
		for i := 0; i+len(lower) <= len(s); i++ {
			if hasPrefixFold(s[i:], lower) {
				return true
			}
		}
		return false
	}
	return strings.Contains(strings.ToLower(s), lower)
}

// isSubsequence reports whether the characters of n appear in s in order.
func isSubsequence(n, s string) bool {
	for _, r := range n {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+utf8.RuneLen(r):]
	}
	return true
}

// compLess orders candidates like fish's wcsfilecmp: ignoring case, with
// numbers compared by value ("file2" < "file10") and '-' after letters, so
// short options come before long ones. Strings equal in that order are
// ordered byte by byte.
func compLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ca, cb := a[i], b[j]
		if isDigitByte(ca) && isDigitByte(cb) {
			si, sj := i, j
			for si < len(a) && a[si] == '0' {
				si++
			}
			for sj < len(b) && b[sj] == '0' {
				sj++
			}
			ei, ej := si, sj
			for ei < len(a) && isDigitByte(a[ei]) {
				ei++
			}
			for ej < len(b) && isDigitByte(b[ej]) {
				ej++
			}
			if ei-si != ej-sj {
				return ei-si < ej-sj
			}
			if c := strings.Compare(a[si:ei], b[sj:ej]); c != 0 {
				return c < 0
			}
			i, j = ei, ej
			continue
		}
		ra, na := utf8.DecodeRuneInString(a[i:])
		rb, nb := utf8.DecodeRuneInString(b[j:])
		if ra != rb {
			ua, ub := sortRune(ra), sortRune(rb)
			if ua != ub {
				return ua < ub
			}
		}
		i += na
		j += nb
	}
	if i < len(a) || j < len(b) {
		return i >= len(a)
	}
	return a < b
}

func sortRune(r rune) rune {
	if r == '-' {
		return '['
	}
	return unicode.ToUpper(r)
}

// Escaping for insertion into the command line. The interactive line is
// read with sh quoting rules: inside '...' nothing is special, inside
// "..." only \ $ ` and ", and outside quotes a backslash escapes the next
// character (\n, \t and friends are C escapes, as in fish).

// escapeFor escapes s for a quoting context: quote is 0 outside quotes,
// or the quote character open ('$' stands for $'...'). atStart says s
// starts a word, where ~ and # are special.
func escapeFor(s string, quote byte, atStart bool) string {
	switch quote {
	case '\'':
		if !strings.Contains(s, "'") {
			return s
		}
		return strings.ReplaceAll(s, "'", `'\''`)
	case '"':
		if !strings.ContainsAny(s, "\\$`\"") {
			return s
		}
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '\\', '$', '`', '"':
				b.WriteByte('\\')
			}
			b.WriteByte(s[i])
		}
		return b.String()
	case '$':
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			c := s[i]
			switch {
			case c == '\\' || c == '\'':
				b.WriteByte('\\')
				b.WriteByte(c)
			case c < 0x20 || c == 0x7f:
				b.WriteString(`\x`)
				b.WriteString(hex2(c))
			default:
				b.WriteByte(c)
			}
		}
		return b.String()
	}
	if !needsEscape(s, atStart) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		c := s[i]
		if c >= utf8.RuneSelf {
			r, n := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n == 1 {
				b.WriteString(`\x`)
				b.WriteString(hex2(c))
			} else {
				b.WriteString(s[i : i+n])
			}
			i += n
			continue
		}
		switch {
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == 0x1b:
			b.WriteString(`\e`)
		case c < 0x20 || c == 0x7f:
			b.WriteString(`\x`)
			b.WriteString(hex2(c))
		case isShellSpecial(c), (c == '~' || c == '#') && i == 0 && atStart:
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
		i++
	}
	return b.String()
}

// isShellSpecial reports whether c must be escaped outside quotes.
func isShellSpecial(c byte) bool {
	switch c {
	case ' ', '\\', '\'', '"', '$', '`', ';', '&', '|', '<', '>', '(', ')',
		'*', '?', '[', ']', '{', '}':
		return true
	}
	return false
}

func needsEscape(s string, atStart bool) bool {
	if atStart && s != "" && (s[0] == '~' || s[0] == '#') {
		return true
	}
	ascii := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= utf8.RuneSelf {
			ascii = false
			continue
		}
		if c < 0x20 || c == 0x7f || isShellSpecial(c) {
			return true
		}
	}
	return !ascii && !utf8.ValidString(s)
}

func hex2(c byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[c>>4], digits[c&15]})
}

// unescapeToken returns the value of a token's source text with quotes
// removed (open quotes are closed); expansions are kept as typed.
func unescapeToken(raw string) string {
	l := scanCompLine(raw, len(raw))
	if l.tok.start == 0 && len(l.words) == 0 {
		return l.tok.value
	}
	return raw
}

// truncateDesc shortens a description for the pager.
func truncateDesc(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + "…"
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max-1 {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// fishEscape quotes s the way fish prints arguments (complete's listing).
func fishEscape(s string) string {
	if s == "" {
		return "''"
	}
	special, hard := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' || c == '\'' || c < 0x20 || c == 0x7f:
			hard = true
		case isShellSpecial(c) || c == '#' || c == '~' || c == '%':
			special = true
		}
	}
	if !special && !hard {
		return s
	}
	if !hard {
		return "'" + s + "'"
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || c == 0x7f:
			b.WriteString(`\x` + hex2(c))
		case c == '\\' || c == '\'' || isShellSpecial(c) || c == '#' || c == '~' || c == '%':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
