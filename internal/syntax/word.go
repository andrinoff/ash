package syntax

import (
	"strings"
	"unicode/utf8"
)

type wordCtx uint8

const (
	ctxNormal wordCtx = iota
	ctxAssign
	ctxBraceAlt
	ctxIndex
	ctxParamArg
	ctxParamPat
	ctxTest
)

// word parses one word. It returns nil if there is no word at the cursor.
func (p *Parser) word(ctx wordCtx) *Word {
	start := p.pos
	parts := p.wordParts(ctx)
	if p.pos == start && len(parts) == 0 {
		return nil
	}
	return &Word{Pos: start, End: p.pos, Parts: parts}
}

func (p *Parser) wordParts(ctx wordCtx) []WordPart {
	p.enter()
	defer p.leave()
	start := p.pos
	var parts []WordPart
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, &Lit{Value: lit.String()})
			lit.Reset()
		}
	}
	for !p.eof() {
		c := p.src[p.pos]
		switch ctx {
		case ctxBraceAlt:
			if c == ',' || c == '}' {
				flush()
				return parts
			}
		case ctxIndex:
			if c == ']' || c == ' ' || c == '\t' {
				flush()
				return parts
			}
		case ctxParamArg:
			if c == '}' {
				flush()
				return parts
			}
		case ctxParamPat:
			if c == '}' || c == '/' {
				flush()
				return parts
			}
		case ctxTest:
			if p.has("]]") && (p.pos+2 >= len(p.src) || isMeta(p.src[p.pos+2])) {
				flush()
				return parts
			}
		}
		if ctx != ctxParamArg && ctx != ctxParamPat {
			if (c == '<' || c == '>') && p.peekAt(1) == '(' && p.pos == start && ctx != ctxTest {
				flush()
				parts = append(parts, p.procSubst())
				continue
			}
			if c == '(' && ctx != ctxTest {
				flush()
				parts = append(parts, p.fishCmdSubst())
				continue
			}
			if isMeta(c) {
				break
			}
		}
		switch c {
		case '\\':
			if p.pos+1 >= len(p.src) {
				if ctx == ctxNormal || ctx == ctxAssign {
					p.fail(p.pos, true, "trailing backslash")
				}
				lit.WriteByte('\\')
				p.pos++
				continue
			}
			if p.src[p.pos+1] == '\n' {
				p.pos += 2
				continue
			}
			flush()
			val, n := unescapeOutside(p.src[p.pos:])
			parts = append(parts, &Quoted{Value: val})
			p.pos += n
		case '\'':
			flush()
			parts = append(parts, p.sglQuoted())
		case '"':
			flush()
			parts = append(parts, p.dblQuoted())
		case '`':
			if p.fish {
				lit.WriteByte(c)
				p.pos++
				continue
			}
			flush()
			parts = append(parts, p.backquote())
		case '$':
			flush()
			if part := p.dollar(false); part != nil {
				parts = append(parts, part)
			} else {
				lit.WriteByte('$')
				p.pos++
			}
		case '{':
			if ctx == ctxParamArg || ctx == ctxParamPat {
				lit.WriteByte(c)
				p.pos++
				continue
			}
			if be := p.braceExp(); be != nil {
				flush()
				parts = append(parts, be)
				continue
			}
			lit.WriteByte(c)
			p.pos++
		case '~':
			atStart := p.pos == start
			afterColon := ctx == ctxAssign && p.pos > 0 && p.src[p.pos-1] == ':' && lit.Len() > 0
			if (atStart || afterColon) && ctx != ctxParamPat {
				if t := p.tilde(ctx); t != nil {
					flush()
					parts = append(parts, t)
					continue
				}
			}
			lit.WriteByte(c)
			p.pos++
		default:
			lit.WriteByte(c)
			p.pos++
		}
		continue
	}
	flush()
	return parts
}

func (p *Parser) tilde(ctx wordCtx) WordPart {
	i := p.pos + 1
	for i < len(p.src) {
		c := p.src[i]
		if isNameChar(c) || c == '.' || c == '-' || c == '+' {
			i++
			continue
		}
		break
	}
	if i < len(p.src) {
		c := p.src[i]
		ok := c == '/' || isMeta(c) || (ctx == ctxAssign && c == ':') || (ctx == ctxBraceAlt && (c == ',' || c == '}'))
		if !ok {
			return nil
		}
	}
	user := p.src[p.pos+1 : i]
	if user == "+" || user == "-" {
		p.pos = i
		return &Tilde{User: user}
	}
	if strings.ContainsAny(user, "+") {
		return nil
	}
	p.pos = i
	return &Tilde{User: user}
}

// unescapeOutside handles a backslash escape outside quotes. Common C
// escapes are understood (fish-style); anything else stands for itself.
func unescapeOutside(s string) (string, int) {
	if len(s) < 2 {
		return "", len(s)
	}
	switch s[1] {
	case 'n':
		return "\n", 2
	case 't':
		return "\t", 2
	case 'r':
		return "\r", 2
	case 'e':
		return "\x1b", 2
	case 'x', 'X':
		v, n := parseHex(s[2:], 2)
		if n == 0 {
			return string(s[1]), 2
		}
		return string([]byte{byte(v)}), 2 + n
	case 'u':
		v, n := parseHex(s[2:], 4)
		if n == 0 {
			return "u", 2
		}
		return string(rune(v)), 2 + n
	case 'U':
		v, n := parseHex(s[2:], 8)
		if n == 0 {
			return "U", 2
		}
		return string(rune(v)), 2 + n
	case 'c':
		if len(s) > 2 && s[2] >= '@' && s[2] <= '~' {
			return string([]byte{s[2] & 0x1f}), 3
		}
		if len(s) > 2 && s[2] == '?' {
			return "\x7f", 3
		}
		return "c", 2
	}
	_, size := utf8.DecodeRuneInString(s[1:])
	return s[1 : 1+size], 1 + size
}

func parseHex(s string, max int) (int, int) {
	v, n := 0, 0
	for n < len(s) && n < max {
		c := s[n]
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return v, n
		}
		v = v*16 + d
		n++
	}
	return v, n
}

func (p *Parser) sglQuoted() WordPart {
	start := p.pos
	p.pos++
	if p.fish {
		var b strings.Builder
		for {
			if p.eof() {
				p.fail(start, true, "unterminated single quote")
			}
			c := p.src[p.pos]
			if c == '\'' {
				p.pos++
				return &Quoted{Value: b.String()}
			}
			if c == '\\' && (p.peekAt(1) == '\'' || p.peekAt(1) == '\\') {
				b.WriteByte(p.src[p.pos+1])
				p.pos += 2
				continue
			}
			b.WriteByte(c)
			p.pos++
		}
	}
	end := strings.IndexByte(p.src[p.pos:], '\'')
	if end < 0 {
		p.pos = len(p.src)
		p.fail(start, true, "unterminated single quote")
	}
	val := p.src[p.pos : p.pos+end]
	p.pos += end + 1
	return &Quoted{Value: val}
}

// ansiCQuoted parses $'...'.
func (p *Parser) ansiCQuoted() WordPart {
	start := p.pos
	p.pos += 2
	var b strings.Builder
	for {
		if p.eof() {
			p.fail(start, true, "unterminated $'...'")
		}
		c := p.src[p.pos]
		if c == '\'' {
			p.pos++
			return &Quoted{Value: b.String()}
		}
		if c != '\\' || p.pos+1 >= len(p.src) {
			b.WriteByte(c)
			p.pos++
			continue
		}
		n := p.src[p.pos+1]
		p.pos += 2
		switch n {
		case 'a':
			b.WriteByte(7)
		case 'b':
			b.WriteByte(8)
		case 'e', 'E':
			b.WriteByte(27)
		case 'f':
			b.WriteByte(12)
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte(11)
		case '\\', '\'', '"', '?':
			b.WriteByte(n)
		case 'x':
			v, k := parseHex(p.src[p.pos:], 2)
			if k == 0 {
				b.WriteString("\\x")
			} else {
				b.WriteByte(byte(v))
				p.pos += k
			}
		case 'u', 'U':
			max := 4
			if n == 'U' {
				max = 8
			}
			v, k := parseHex(p.src[p.pos:], max)
			if k == 0 {
				b.WriteByte('\\')
				b.WriteByte(n)
			} else {
				b.WriteRune(rune(v))
				p.pos += k
			}
		case 'c':
			if p.pos < len(p.src) && p.src[p.pos] != '\'' {
				b.WriteByte(p.src[p.pos] & 0x1f)
				p.pos++
			} else {
				b.WriteString(`\c`) // not the closing quote
			}
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v := int(n - '0')
			k := 0
			for k < 2 && p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '7' {
				v = v*8 + int(p.src[p.pos]-'0')
				p.pos++
				k++
			}
			b.WriteByte(byte(v))
		default:
			b.WriteByte('\\')
			b.WriteByte(n)
		}
	}
}

func (p *Parser) dblQuoted() WordPart {
	start := p.pos
	p.pos++
	dq := &DblQuoted{}
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			dq.Parts = append(dq.Parts, &Lit{Value: lit.String()})
			lit.Reset()
		}
	}
	for {
		if p.eof() {
			p.fail(start, true, "unterminated double quote")
		}
		c := p.src[p.pos]
		switch c {
		case '"':
			p.pos++
			flush()
			return dq
		case '\\':
			n := p.peekAt(1)
			switch n {
			case '$', '`', '"', '\\':
				lit.WriteByte(n)
				p.pos += 2
			case '\n':
				p.pos += 2
			default:
				lit.WriteByte('\\')
				p.pos++
			}
		case '$':
			flush()
			if part := p.dollar(true); part != nil {
				dq.Parts = append(dq.Parts, part)
			} else {
				lit.WriteByte('$')
				p.pos++
			}
		case '`':
			if p.fish {
				lit.WriteByte(c)
				p.pos++
				continue
			}
			flush()
			dq.Parts = append(dq.Parts, p.backquote())
		default:
			lit.WriteByte(c)
			p.pos++
		}
	}
}

func (p *Parser) backquote() WordPart {
	start := p.pos
	p.pos++
	var b strings.Builder
	for {
		if p.eof() {
			p.fail(start, true, "unterminated backquote")
		}
		c := p.src[p.pos]
		if c == '`' {
			p.pos++
			break
		}
		if c == '\\' && p.pos+1 < len(p.src) {
			n := p.src[p.pos+1]
			if n == '`' || n == '\\' || n == '$' {
				b.WriteByte(n)
				p.pos += 2
				continue
			}
		}
		b.WriteByte(c)
		p.pos++
	}
	sub := &Parser{src: b.String(), depth: p.depth, fish: p.fish}
	var stmts []*Stmt
	p.nested(func(int) int { return start }, func() {
		stmts = sub.stmtList(nil)
		sub.skipSeparators()
		if !sub.eof() {
			sub.fail(sub.pos, false, "unexpected %s in backquotes", sub.describe())
		}
	})
	return &CmdSubst{Stmts: stmts}
}

func (p *Parser) fishCmdSubst() WordPart {
	start := p.pos
	p.pos++
	stmts := p.stmtList(nil)
	p.skipSpace()
	if p.eof() {
		p.fail(start, true, "unterminated command substitution")
	}
	if p.peek() != ')' {
		p.fail(p.pos, false, "expected ')', found %s", p.describe())
	}
	p.pos++
	cs := &CmdSubst{Stmts: stmts, Fish: true}
	if p.peek() == '[' {
		if idx := p.fishIndex(); idx != nil {
			cs.Index = idx
		}
	}
	return cs
}

func (p *Parser) procSubst() WordPart {
	start := p.pos
	out := p.peek() == '>'
	p.pos += 2
	stmts := p.stmtList(nil)
	p.skipSpace()
	if p.eof() {
		p.fail(start, true, "unterminated process substitution")
	}
	if p.peek() != ')' {
		p.fail(p.pos, false, "expected ')', found %s", p.describe())
	}
	p.pos++
	return &ProcSubst{Out: out, Stmts: stmts}
}
