package syntax

import (
	"strings"
)

// dollar parses an expansion starting at '$'. It returns nil when the
// dollar sign is just a dollar sign.
func (p *Parser) dollar(inDQ bool) WordPart {
	start := p.pos
	rest := p.src[p.pos+1:]
	switch {
	case strings.HasPrefix(rest, "(("):
		if p.matchArith(p.pos+3) >= 0 {
			p.pos += 3
			w := p.arithWord(func(depth int) bool { return depth == 0 && p.has("))") })
			if p.eof() {
				p.fail(start, true, "unterminated $((")
			}
			p.pos += 2
			return &ArithExp{Expr: w}
		}
		fallthrough
	case strings.HasPrefix(rest, "("):
		p.pos += 2
		stmts := p.stmtList(nil)
		p.skipSpace()
		if p.eof() {
			p.fail(start, true, "unterminated $(")
		}
		if p.peek() != ')' {
			p.fail(p.pos, false, "expected ')', found %s", p.describe())
		}
		p.pos++
		return &CmdSubst{Stmts: stmts}
	case strings.HasPrefix(rest, "{"):
		return p.bracedParam()
	case strings.HasPrefix(rest, "'") && !inDQ:
		return p.ansiCQuoted()
	case strings.HasPrefix(rest, "\"") && !inDQ:
		p.pos++
		return p.dblQuoted()
	case len(rest) > 0 && isNameStart(rest[0]):
		i := 1
		for i < len(rest) && isNameChar(rest[i]) {
			i++
		}
		pe := &ParamExp{Name: rest[:i], Short: true}
		p.pos += 1 + i
		if p.peek() == '[' {
			if idx := p.fishIndex(); idx != nil {
				pe.Index = idx
			}
		}
		return pe
	case p.fish && len(rest) > 1 && rest[0] == '$' && isNameStart(rest[1]):
		// fish: $$name expands the variable that $name names
		p.pos++
		pe := p.dollar(inDQ).(*ParamExp)
		pe.Indirect = true
		return pe
	case len(rest) > 0 && strings.IndexByte("?$!#@*-0123456789", rest[0]) >= 0:
		p.pos += 2
		return &ParamExp{Name: rest[:1], Short: true}
	}
	return nil
}

// fishIndex parses $var[...] if the brackets look like a fish index (so
// "$x[0-9]" in a regex keeps working as text).
func (p *Parser) fishIndex() *Index {
	end := matchBracket(p.src, p.pos)
	if end < 0 {
		return nil
	}
	inner := p.src[p.pos+1 : end]
	if !validFishIndex(inner) {
		return nil
	}
	save := p.pos
	p.pos++
	idx := &Index{Fish: true}
	for {
		p.skipBlanks()
		if p.peek() == ']' {
			p.pos++
			return idx
		}
		if p.eof() {
			p.pos = save
			return nil
		}
		w := p.word(ctxIndex)
		if w == nil {
			p.pos = save
			return nil
		}
		idx.Words = append(idx.Words, w)
	}
}

func matchBracket(s string, i int) int {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return j
			}
		case '(':
			// skip a nested command substitution
			k := matchParen(s, j)
			if k < 0 {
				return -1
			}
			j = k
		case '\n', ';', '|', '&':
			return -1
		}
	}
	return -1
}

func matchParen(s string, i int) int {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j
			}
		case '\'':
			k := strings.IndexByte(s[j+1:], '\'')
			if k < 0 {
				return -1
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
			j = k
		}
	}
	return -1
}

// validFishIndex accepts things like "1", "-1", "2..3", "..-2", "$i",
// "(math $i + 1)..", "1 3 5".
func validFishIndex(s string) bool {
	toks := splitTopLevel(s)
	if len(toks) == 0 {
		return false
	}
	for _, t := range toks {
		if !validIndexToken(t) {
			return false
		}
	}
	return true
}

func splitTopLevel(s string) []string {
	var toks []string
	depth := 0
	start := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '(':
			depth++
		case c == ')':
			depth--
		}
		if (c == ' ' || c == '\t') && depth == 0 {
			if start >= 0 {
				toks = append(toks, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		toks = append(toks, s[start:])
	}
	return toks
}

func validIndexToken(t string) bool {
	if t == "" {
		return false
	}
	var ends []string
	if i := strings.Index(t, ".."); i >= 0 && !strings.Contains(t[:i], "(") {
		ends = []string{t[:i], t[i+2:]}
		if ends[0] == "" && ends[1] == "" {
			return true
		}
	} else if i >= 0 {
		// ".." after a parenthesised term
		k := matchParen(t, strings.IndexByte(t, '('))
		if k >= 0 && strings.HasPrefix(t[k+1:], "..") {
			ends = []string{t[:k+1], t[k+3:]}
		} else {
			ends = []string{t}
		}
	} else {
		ends = []string{t}
	}
	for _, e := range ends {
		if e == "" {
			continue
		}
		if !validIndexTerm(e) {
			return false
		}
	}
	return true
}

func validIndexTerm(e string) bool {
	if e[0] == '(' {
		return matchParen(e, 0) == len(e)-1
	}
	if e[0] == '$' {
		if len(e) < 2 || !isNameStart(e[1]) {
			return false
		}
		i := 2
		for i < len(e) && isNameChar(e[i]) {
			i++
		}
		rest := e[i:]
		return rest == "" || rest[0] == '['
	}
	if e[0] == '-' {
		e = e[1:]
	}
	if e == "" {
		return false
	}
	for i := 0; i < len(e); i++ {
		if !isDigit(e[i]) {
			return false
		}
	}
	return true
}

func (p *Parser) bracedParam() WordPart {
	start := p.pos
	p.pos += 2
	pe := &ParamExp{}
	if p.eof() {
		p.fail(start, true, "unterminated ${")
	}
	switch p.peek() {
	case '#':
		if n := p.peekAt(1); n != '}' && n != 0 && !(n == ':' || n == '-' || n == '=' || n == '?' || n == '+') {
			pe.Length = true
			p.pos++
		}
	case '!':
		if n := p.peekAt(1); n != '}' && n != 0 {
			pe.Indirect = true
			p.pos++
		}
	}
	// name
	i := p.pos
	switch {
	case i < len(p.src) && isNameStart(p.src[i]):
		for i < len(p.src) && isNameChar(p.src[i]) {
			i++
		}
	case i < len(p.src) && isDigit(p.src[i]):
		for i < len(p.src) && isDigit(p.src[i]) {
			i++
		}
	case i < len(p.src) && strings.IndexByte("?$!#@*-", p.src[i]) >= 0:
		i++
	default:
		return p.badParam(start)
	}
	pe.Name = p.src[p.pos:i]
	p.pos = i
	if p.peek() == '[' {
		end := matchBracket(p.src, p.pos)
		if end < 0 {
			p.fail(p.pos, p.eof(), "unterminated subscript")
		}
		inner := p.src[p.pos+1 : end]
		idx := &Index{}
		switch inner {
		case "@":
			idx.All = true
		case "*":
			idx.All, idx.Star = true, true
		default:
			sub := &Parser{src: inner}
			off := p.pos + 1
			p.nested(func(pos int) int { return off + pos }, func() {
				idx.Expr = sub.arithWord(func(int) bool { return false })
			})
		}
		pe.Index = idx
		p.pos = end + 1
	}
	if pe.Indirect {
		switch {
		case p.has("*}") || p.has("@}"):
			pe.Op = OpPrefixNames
			pe.Indirect = false
			p.pos++
		case pe.Index != nil && pe.Index.All && p.peek() == '}':
			pe.Op = OpIndices
			pe.Indirect = false
		}
	}
	if p.eof() {
		p.fail(start, true, "unterminated ${")
	}
	argWord := func(ctx wordCtx) *Word {
		s := p.pos
		parts := p.wordParts(ctx)
		return &Word{Pos: s, End: p.pos, Parts: parts}
	}
	switch c := p.peek(); c {
	case '}':
	case ':':
		p.pos++
		switch p.peek() {
		case '-':
			pe.Op = OpDefault
		case '=':
			pe.Op = OpAssign
		case '?':
			pe.Op = OpError
		case '+':
			pe.Op = OpAlt
		default:
			pe.Op = OpSubstr
			pe.Arg = p.arithWord(func(depth int) bool { return depth == 0 && (p.peek() == ':' || p.peek() == '}') })
			if p.peek() == ':' {
				p.pos++
				pe.HasArg2 = true
				pe.Arg2 = p.arithWord(func(depth int) bool { return depth == 0 && p.peek() == '}' })
			}
		}
		if pe.Op != OpSubstr {
			p.pos++
			pe.Arg = argWord(ctxParamArg)
		}
	case '-', '=', '?', '+':
		pe.Op = map[byte]ParamOp{'-': OpDefaultUnset, '=': OpAssignUnset, '?': OpErrorUnset, '+': OpAltUnset}[c]
		p.pos++
		pe.Arg = argWord(ctxParamArg)
	case '#', '%':
		long := p.peekAt(1) == c
		if c == '#' {
			pe.Op = OpTrimPrefix
			if long {
				pe.Op = OpTrimPrefixLong
			}
		} else {
			pe.Op = OpTrimSuffix
			if long {
				pe.Op = OpTrimSuffixLong
			}
		}
		p.pos++
		if long {
			p.pos++
		}
		pe.Arg = argWord(ctxParamArg)
	case '/':
		p.pos++
		pe.Op = OpReplace
		switch p.peek() {
		case '/':
			pe.Op = OpReplaceAll
			p.pos++
		case '#':
			pe.Op = OpReplacePrefix
			p.pos++
		case '%':
			pe.Op = OpReplaceSuffix
			p.pos++
		}
		pe.Arg = argWord(ctxParamPat)
		if p.peek() == '/' {
			p.pos++
			pe.HasArg2 = true
			pe.Arg2 = argWord(ctxParamArg)
		}
	case '^', ',':
		long := p.peekAt(1) == c
		if c == '^' {
			pe.Op = OpUpperFirst
			if long {
				pe.Op = OpUpperAll
			}
		} else {
			pe.Op = OpLowerFirst
			if long {
				pe.Op = OpLowerAll
			}
		}
		p.pos++
		if long {
			p.pos++
		}
		pe.Arg = argWord(ctxParamArg)
	case '@':
		p.pos++
		pe.Op = OpTransform
		if p.eof() {
			p.fail(start, true, "unterminated ${")
		}
		pe.Arg = litWord(string(p.peek()))
		p.pos++
	default:
		return p.badParam(start)
	}
	if p.eof() {
		p.fail(start, true, "unterminated ${")
	}
	if p.peek() != '}' {
		return p.badParam(start)
	}
	p.pos++
	return pe
}

// badParam reads the ${...} at start, which isn't an expansion ash knows,
// such as zsh's ${(f)x}. Like bash, which only complains about it when it
// expands it, ash accepts it so that a script can keep it in code that only
// runs under zsh.
func (p *Parser) badParam(start int) WordPart {
	depth := 0
	for i := start + 1; i < len(p.src); i++ {
		switch p.src[i] {
		case '\\':
			i++
		case '\'', '"', '`':
			k := strings.IndexByte(p.src[i+1:], p.src[i])
			if k < 0 {
				i = len(p.src)
				break
			}
			i += k + 1
		case '(':
			k := matchParen(p.src, i)
			if k < 0 {
				i = len(p.src)
				break
			}
			i = k
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				p.pos = i + 1
				return &ParamExp{Op: OpBad, Arg: litWord(p.src[start:p.pos])}
			}
		}
	}
	p.fail(start, true, "unterminated ${")
	return nil
}

// arithWord reads arithmetic text up to stop(depth). Only $ and `
// expansions are recognized; everything else is kept as text.
func (p *Parser) arithWord(stop func(depth int) bool) *Word {
	w := &Word{Pos: p.pos}
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			w.Parts = append(w.Parts, &Lit{Value: lit.String()})
			lit.Reset()
		}
	}
	depth := 0
	for !p.eof() {
		if stop(depth) {
			break
		}
		c := p.src[p.pos]
		switch c {
		case '(':
			depth++
			lit.WriteByte(c)
			p.pos++
		case ')':
			depth--
			lit.WriteByte(c)
			p.pos++
		case '$':
			flush()
			if part := p.dollar(true); part != nil {
				w.Parts = append(w.Parts, part)
			} else {
				lit.WriteByte('$')
				p.pos++
			}
		case '`':
			flush()
			w.Parts = append(w.Parts, p.backquote())
		case '\'', '"':
			// quotes are allowed and simply removed
			p.pos++
		case '\\':
			if p.peekAt(1) == '\n' {
				p.pos += 2
				continue
			}
			lit.WriteByte(c)
			p.pos++
		case '\n':
			lit.WriteByte(' ')
			p.pos++
		default:
			lit.WriteByte(c)
			p.pos++
		}
	}
	flush()
	w.End = p.pos
	// Quote everything: arithmetic text never globs.
	return &Word{Pos: w.Pos, End: w.End, Parts: []WordPart{&DblQuoted{Parts: w.Parts}}}
}
