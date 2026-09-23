package syntax

import (
	"strings"
)

// braceExp parses {a,b} or {1..3} at the cursor, or returns nil if the
// brace is just a brace.
func (p *Parser) braceExp() WordPart {
	end, comma := scanBrace(p.src, p.pos)
	if end < 0 {
		return nil
	}
	inner := p.src[p.pos+1 : end]
	if !comma {
		if seq := parseSeq(inner); seq != nil {
			p.pos = end + 1
			return &BraceExp{Seq: seq}
		}
		// fish drops the braces of {$var}, which sets a variable off
		// from text ({$x}s), but keeps those of {a}
		if !p.fish || !hasVariable(inner) {
			return nil
		}
	}
	p.pos++
	be := &BraceExp{}
	for {
		alt := p.wordParts(ctxBraceAlt)
		be.Alts = append(be.Alts, alt)
		if p.eof() {
			p.fail(p.pos, true, "unterminated brace")
		}
		if p.peek() == ',' {
			p.pos++
			continue
		}
		p.pos++ // '}'
		return be
	}
}

// hasVariable reports whether s expands a variable outside single quotes
// and command substitutions.
func hasVariable(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '\'':
			k := strings.IndexByte(s[i+1:], '\'')
			if k < 0 {
				return false
			}
			i += k + 1
		case '(':
			k := matchParen(s, i)
			if k < 0 {
				return false
			}
			i = k
		case '$':
			if i+1 < len(s) && isNameStart(s[i+1]) {
				return true
			}
		}
	}
	return false
}

// scanBrace finds the '}' matching the '{' at i. It reports whether the
// group has a top-level comma.
func scanBrace(s string, i int) (int, bool) {
	depth := 0
	comma := false
	for j := i; j < len(s); j++ {
		c := s[j]
		switch c {
		case '\\':
			j++
		case '\'':
			k := strings.IndexByte(s[j+1:], '\'')
			if k < 0 {
				return -1, false
			}
			j += k + 1
		case '"':
			k := j + 1
			for k < len(s) && s[k] != '"' {
				if s[k] == '\\' {
					k++
				}
				k++
			}
			if k >= len(s) {
				return -1, false
			}
			j = k
		case '$':
			if j+1 < len(s) && (s[j+1] == '(' || s[j+1] == '{') {
				var k int
				if s[j+1] == '(' {
					k = matchParen(s, j+1)
				} else {
					k = strings.IndexByte(s[j+1:], '}')
					if k >= 0 {
						k += j + 1
					}
				}
				if k < 0 {
					return -1, false
				}
				j = k
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j, comma
			}
		case ',':
			if depth == 1 {
				comma = true
			}
		case ' ', '\t', '\n', ';', '&', '|', '<', '>', ')':
			return -1, false
		case '(':
			k := matchParen(s, j)
			if k < 0 {
				return -1, false
			}
			j = k
		}
	}
	return -1, false
}

func parseSeq(s string) *BraceSeq {
	parts := strings.Split(s, "..")
	if len(parts) != 2 && len(parts) != 3 {
		return nil
	}
	isInt := func(x string) bool {
		if x == "" {
			return false
		}
		if x[0] == '-' || x[0] == '+' {
			x = x[1:]
		}
		if x == "" {
			return false
		}
		for i := 0; i < len(x); i++ {
			if !isDigit(x[i]) {
				return false
			}
		}
		return true
	}
	isChar := func(x string) bool { return len(x) == 1 && (x[0] >= 'a' && x[0] <= 'z' || x[0] >= 'A' && x[0] <= 'Z') }
	a, b := parts[0], parts[1]
	if !(isInt(a) && isInt(b)) && !(isChar(a) && isChar(b)) {
		return nil
	}
	seq := &BraceSeq{Start: a, End: b}
	if len(parts) == 3 {
		if !isInt(parts[2]) {
			return nil
		}
		seq.Step = parts[2]
	}
	return seq
}
