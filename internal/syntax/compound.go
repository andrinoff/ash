package syntax

import (
	"strings"
	"unicode/utf8"
)

// shFuncDef parses `name() compound-command`.
func (p *Parser) shFuncDef() Command {
	start := p.pos
	i := p.pos
	for i < len(p.src) {
		c := p.src[i]
		if isMeta(c) || c == '"' || c == '\'' || c == '$' || c == '\\' || c == '`' || c == '=' {
			break
		}
		i++
	}
	if i == p.pos {
		return nil
	}
	name := p.src[p.pos:i]
	j := i
	for j < len(p.src) && (p.src[j] == ' ' || p.src[j] == '\t') {
		j++
	}
	if j >= len(p.src) || p.src[j] != '(' {
		return nil
	}
	j++
	for j < len(p.src) && (p.src[j] == ' ' || p.src[j] == '\t') {
		j++
	}
	if j >= len(p.src) || p.src[j] != ')' {
		return nil
	}
	p.pos = j + 1
	p.skipSpace()
	if p.eof() {
		p.fail(p.pos, true, "expected function body")
	}
	body := p.command()
	return &FuncDecl{Pos: start, End: p.pos, Name: litWord(name), BodyCmd: body, Src: p.src[start:p.pos]}
}

func (p *Parser) funcDecl() Command {
	start := p.pos
	p.pos += len("function")
	p.skipBlanks()
	if p.atCommandEnd() {
		p.fail(p.pos, p.eof(), "expected a function name")
	}
	name := p.word(ctxNormal)
	if name == nil {
		p.fail(p.pos, false, "expected a function name, found %s", p.describe())
	}
	p.skipBlanks()
	// bash: function name() { ... } / function name { ... }
	if p.has("()") {
		p.pos += 2
		p.skipSpace()
		body := p.command()
		return &FuncDecl{Pos: start, End: p.pos, Name: name, BodyCmd: body, Src: p.src[start:p.pos]}
	}
	if p.isKeyword("{") {
		body := p.command()
		return &FuncDecl{Pos: start, End: p.pos, Name: name, BodyCmd: body, Src: p.src[start:p.pos]}
	}
	// fish: function name [options]; body; end
	fd := &FuncDecl{Pos: start, Name: name, Fish: true}
	for {
		p.skipBlanks()
		if p.atCommandEnd() {
			break
		}
		w := p.word(ctxNormal)
		if w == nil {
			p.fail(p.pos, false, "unexpected %s", p.describe())
		}
		fd.Opts = append(fd.Opts, w)
	}
	// a bash-style body on the next line
	save := p.pos
	p.skipSeparators()
	if p.isKeyword("{") && len(fd.Opts) == 0 {
		body := p.command()
		return &FuncDecl{Pos: start, End: p.pos, Name: name, BodyCmd: body, Src: p.src[start:p.pos]}
	}
	p.pos = save
	fd.Body = p.stmtList(func() bool { return p.isKeyword("end") })
	p.expectKeyword("end", "'function'")
	fd.End = p.pos
	fd.Src = p.src[start:p.pos]
	return fd
}

// condBody parses a condition followed by a body, in either sh or fish
// style. posixKw is "then" or "do". It returns the condition, the body,
// whether sh style was used, and leaves the parser before the closing
// keyword.
func (p *Parser) condBody(posixKw string, posixStop, fishStop []string) (cond, body []*Stmt, posix bool) {
	cond = []*Stmt{p.stmt()}
	for {
		p.skipBlanks()
		if p.peek() == ';' && p.peekAt(1) != ';' {
			p.pos++
		}
		p.skipSeparators()
		if p.eof() {
			p.fail(p.pos, true, "unterminated block")
		}
		kw := p.peekKeyword()
		if kw == posixKw {
			p.pos += len(kw)
			body = p.stmtList(func() bool { return p.isKeyword(posixStop...) })
			return cond, body, true
		}
		if kw == "and" || kw == "or" {
			cond = append(cond, p.stmt())
			continue
		}
		break
	}
	stops := append(append([]string{}, fishStop...), posixKw)
	body = p.stmtList(func() bool { return p.isKeyword(stops...) })
	if p.isKeyword(posixKw) {
		// `if a; b; then` - everything so far was condition.
		p.pos += len(posixKw)
		cond = append(cond, body...)
		body = p.stmtList(func() bool { return p.isKeyword(posixStop...) })
		return cond, body, true
	}
	return cond, body, false
}

func (p *Parser) ifClause() Command {
	p.pos += 2
	p.skipBlanks()
	ic := &IfClause{}
	cond, body, posix := p.condBody("then", []string{"elif", "else", "fi"}, []string{"else", "end"})
	ic.Cond, ic.Then, ic.Fish = cond, body, !posix
	if posix {
		for {
			p.skipSeparators()
			switch p.peekKeyword() {
			case "elif":
				p.pos += 4
				p.skipBlanks()
				c, b, ok := p.condBody("then", []string{"elif", "else", "fi"}, []string{"else", "end"})
				if !ok {
					p.fail(p.pos, p.eof(), "expected 'then' after 'elif'")
				}
				ic.Elifs = append(ic.Elifs, &ElifBranch{Cond: c, Then: b})
			case "else":
				p.pos += 4
				ic.Else = p.stmtList(func() bool { return p.isKeyword("fi") })
				if ic.Else == nil {
					ic.Else = []*Stmt{}
				}
			case "fi":
				p.pos += 2
				ic.Redirs = p.trailingRedirs()
				return ic
			default:
				p.fail(p.pos, p.eof(), "expected 'fi' to close 'if', found %s", p.describe())
			}
		}
	}
	for {
		p.skipSeparators()
		switch p.peekKeyword() {
		case "else":
			p.pos += 4
			p.skipBlanks()
			if p.isKeyword("if") {
				p.pos += 2
				p.skipBlanks()
				c, b, ok := p.condBody("then", nil, []string{"else", "end"})
				if ok {
					p.fail(p.pos, false, "'then' is not used with 'else if'")
				}
				ic.Elifs = append(ic.Elifs, &ElifBranch{Cond: c, Then: b})
				continue
			}
			ic.Else = p.stmtList(func() bool { return p.isKeyword("end") })
			if ic.Else == nil {
				ic.Else = []*Stmt{}
			}
		case "end":
			p.pos += 3
			ic.Redirs = p.trailingRedirs()
			return ic
		default:
			p.fail(p.pos, p.eof(), "expected 'end' to close 'if', found %s", p.describe())
		}
	}
}

func (p *Parser) whileClause(until bool) Command {
	if until {
		p.pos += 5
	} else {
		p.pos += 5
	}
	p.skipBlanks()
	wc := &WhileClause{Until: until}
	cond, body, posix := p.condBody("do", []string{"done"}, []string{"end"})
	wc.Cond, wc.Body, wc.Fish = cond, body, !posix
	if posix {
		p.expectKeyword("done", "'while'")
	} else {
		p.expectKeyword("end", "'while'")
	}
	wc.Redirs = p.trailingRedirs()
	return wc
}

func (p *Parser) forClause() Command {
	p.pos += 3
	p.skipBlanks()
	if p.has("((") {
		return p.cforClause()
	}
	i := p.pos
	for i < len(p.src) && isNameChar(p.src[i]) {
		i++
	}
	if i == p.pos {
		p.fail(p.pos, p.eof(), "expected a variable name after 'for'")
	}
	fc := &ForClause{Var: p.src[p.pos:i]}
	p.pos = i
	p.skipBlanks()
	if p.isKeyword("in") {
		p.pos += 2
		fc.HasIn = true
		for {
			p.skipBlanks()
			if p.atCommandEnd() {
				break
			}
			w := p.word(ctxNormal)
			if w == nil {
				p.fail(p.pos, false, "unexpected %s", p.describe())
			}
			fc.Items = append(fc.Items, w)
		}
	}
	p.skipSeparators()
	if p.eof() {
		p.fail(p.pos, true, "unterminated 'for'")
	}
	if p.isKeyword("do") {
		p.pos += 2
		fc.Body = p.stmtList(func() bool { return p.isKeyword("done") })
		p.expectKeyword("done", "'for'")
	} else if p.isKeyword("{") {
		p.pos++
		fc.Body = p.stmtList(func() bool { return p.isKeyword("}") })
		p.expectKeyword("}", "'for'")
	} else {
		fc.Fish = true
		fc.Body = p.stmtList(func() bool { return p.isKeyword("end") })
		p.expectKeyword("end", "'for'")
	}
	fc.Redirs = p.trailingRedirs()
	return fc
}

func (p *Parser) cforClause() Command {
	p.pos += 2
	cf := &CForClause{}
	parts := []**Word{&cf.Init, &cf.Cond, &cf.Post}
	for i, dst := range parts {
		last := i == 2
		w := p.arithWord(func(depth int) bool {
			if depth != 0 {
				return false
			}
			if last {
				return p.has("))")
			}
			return p.peek() == ';'
		})
		*dst = w
		if p.eof() {
			p.fail(p.pos, true, "unterminated 'for (('")
		}
		if last {
			p.pos += 2
		} else {
			p.pos++
		}
	}
	p.skipSeparators()
	if p.isKeyword("do") {
		p.pos += 2
		cf.Body = p.stmtList(func() bool { return p.isKeyword("done") })
		p.expectKeyword("done", "'for'")
	} else if p.isKeyword("{") {
		p.pos++
		cf.Body = p.stmtList(func() bool { return p.isKeyword("}") })
		p.expectKeyword("}", "'for'")
	} else {
		p.fail(p.pos, p.eof(), "expected 'do' after 'for ((...))'")
	}
	cf.Redirs = p.trailingRedirs()
	return cf
}

func (p *Parser) caseClause() Command {
	p.pos += 4
	p.skipBlanks()
	w := p.word(ctxNormal)
	if w == nil {
		p.fail(p.pos, p.eof(), "expected a word after 'case'")
	}
	cc := &CaseClause{Word: w}
	p.skipSpace()
	if !p.isKeyword("in") {
		p.fail(p.pos, p.eof(), "expected 'in' after 'case WORD'")
	}
	p.pos += 2
	for {
		p.skipSeparators()
		if p.eof() {
			p.fail(p.pos, true, "unterminated 'case'")
		}
		if p.isKeyword("esac") {
			p.pos += 4
			cc.Redirs = p.trailingRedirs()
			return cc
		}
		item := &CaseItem{}
		if p.peek() == '(' {
			p.pos++
		}
		for {
			p.skipBlanks()
			pw := p.word(ctxNormal)
			if pw == nil {
				p.fail(p.pos, p.eof(), "expected a pattern, found %s", p.describe())
			}
			item.Patterns = append(item.Patterns, pw)
			p.skipBlanks()
			if p.peek() == '|' {
				p.pos++
				continue
			}
			if p.peek() == ')' {
				p.pos++
				break
			}
			p.fail(p.pos, p.eof(), "expected ')' after case pattern, found %s", p.describe())
		}
		item.Body = p.stmtList(func() bool { return p.isKeyword("esac") })
		p.skipSpace()
		switch {
		case p.has(";;&"):
			p.pos += 3
			item.Op = CaseContinue
		case p.has(";;"):
			p.pos += 2
		case p.has(";&"):
			p.pos += 2
			item.Op = CaseFall
		}
		cc.Items = append(cc.Items, item)
	}
}

func (p *Parser) switchClause() Command {
	p.pos += 6
	p.skipBlanks()
	w := p.word(ctxNormal)
	if w == nil {
		p.fail(p.pos, p.eof(), "expected a word after 'switch'")
	}
	cc := &CaseClause{Word: w, Fish: true}
	for {
		p.skipSeparators()
		if p.eof() {
			p.fail(p.pos, true, "unterminated 'switch'")
		}
		if p.isKeyword("end") {
			p.pos += 3
			cc.Redirs = p.trailingRedirs()
			return cc
		}
		if !p.isKeyword("case") {
			p.fail(p.pos, false, "expected 'case' inside 'switch', found %s", p.describe())
		}
		p.pos += 4
		item := &CaseItem{}
		for {
			p.skipBlanks()
			if p.atCommandEnd() {
				break
			}
			pw := p.word(ctxNormal)
			if pw == nil {
				p.fail(p.pos, false, "unexpected %s", p.describe())
			}
			item.Patterns = append(item.Patterns, pw)
		}
		item.Body = p.stmtList(func() bool { return p.isKeyword("case", "end") })
		cc.Items = append(cc.Items, item)
	}
}

func (p *Parser) arithCmd() Command {
	p.pos += 2
	w := p.arithWord(func(depth int) bool { return depth == 0 && p.has("))") })
	if p.eof() {
		p.fail(p.pos, true, "unterminated '(('")
	}
	p.pos += 2
	return &ArithCmd{Expr: w, Redirs: p.trailingRedirs()}
}

// matchArith returns the index just past the "))" matching an arithmetic
// expression whose body starts at i, or -1.
func (p *Parser) matchArith(i int) int {
	depth := 0
	for i < len(p.src) {
		switch p.src[i] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				if i+1 < len(p.src) && p.src[i+1] == ')' {
					return i + 2
				}
				return -1
			}
			depth--
		case '\n', ';':
			if depth == 0 {
				return -1
			}
		}
		i++
	}
	return -1
}

func (p *Parser) testClause() Command {
	p.pos += 2
	e := p.testOr()
	p.skipSpace()
	if !p.has("]]") {
		p.fail(p.pos, p.eof(), "expected ']]', found %s", p.describe())
	}
	p.pos += 2
	return &TestClause{Expr: e, Redirs: p.trailingRedirs()}
}

func (p *Parser) testOr() TestExpr {
	x := p.testAnd()
	for {
		p.skipSpace()
		if !p.has("||") {
			return x
		}
		p.pos += 2
		x = &TestBinary{Op: "||", X: x, Y: p.testAnd()}
	}
}

func (p *Parser) testAnd() TestExpr {
	x := p.testNot()
	for {
		p.skipSpace()
		if !p.has("&&") {
			return x
		}
		p.pos += 2
		x = &TestBinary{Op: "&&", X: x, Y: p.testNot()}
	}
}

func (p *Parser) testNot() TestExpr {
	p.skipSpace()
	if p.peek() == '!' && (p.peekAt(1) == ' ' || p.peekAt(1) == '\t') {
		p.pos++
		return &TestUnary{Op: "!", X: p.testNot()}
	}
	return p.testPrimary()
}

var testBinOps = map[string]bool{
	"==": true, "=": true, "!=": true, "=~": true, "<": true, ">": true,
	"-eq": true, "-ne": true, "-lt": true, "-le": true, "-gt": true, "-ge": true,
	"-nt": true, "-ot": true, "-ef": true,
}

var testUnOps = map[string]bool{
	"-a": true, "-b": true, "-c": true, "-d": true, "-e": true, "-f": true, "-g": true,
	"-h": true, "-k": true, "-p": true, "-r": true, "-s": true, "-t": true, "-u": true,
	"-w": true, "-x": true, "-G": true, "-L": true, "-N": true, "-O": true, "-S": true,
	"-z": true, "-n": true, "-o": true, "-v": true, "-R": true,
}

func (p *Parser) testAtEnd() bool {
	p.skipSpace()
	return p.eof() || p.has("]]") || p.has("&&") || p.has("||") || p.peek() == ')'
}

func (p *Parser) testOperator() string {
	p.skipSpace()
	for _, op := range []string{"==", "!=", "=~", "<", ">", "="} {
		if p.has(op) && (p.pos+len(op) >= len(p.src) || p.src[p.pos+len(op)] == ' ' || p.src[p.pos+len(op)] == '\t') {
			return op
		}
	}
	if p.peek() == '-' {
		i := p.pos + 1
		for i < len(p.src) && p.src[i] >= 'a' && p.src[i] <= 'z' {
			i++
		}
		op := p.src[p.pos:i]
		if testBinOps[op] && (i >= len(p.src) || p.src[i] == ' ' || p.src[i] == '\t') {
			return op
		}
	}
	return ""
}

func (p *Parser) testPrimary() TestExpr {
	p.skipSpace()
	if p.eof() {
		p.fail(p.pos, true, "unterminated '[['")
	}
	if p.peek() == '(' {
		p.pos++
		x := p.testOr()
		p.skipSpace()
		if p.peek() != ')' {
			p.fail(p.pos, p.eof(), "expected ')' in '[['")
		}
		p.pos++
		return &TestParen{X: x}
	}
	w := p.word(ctxTest)
	if w == nil {
		p.fail(p.pos, false, "unexpected %s in '[['", p.describe())
	}
	if lit, ok := w.IsUnquotedLit(); ok && testUnOps[lit] && !p.testAtEnd() && p.testOperator() == "" {
		p.skipSpace()
		arg := p.word(ctxTest)
		if arg == nil {
			p.fail(p.pos, false, "expected an argument for %s", lit)
		}
		return &TestUnary{Op: lit, X: &TestWord{W: arg}}
	}
	if op := p.testOperator(); op != "" {
		p.pos += len(op)
		p.skipBlanks()
		var rhs *Word
		if op == "=~" {
			rhs = p.regexWord()
		} else {
			rhs = p.word(ctxTest)
		}
		if rhs == nil {
			p.fail(p.pos, p.eof(), "expected an argument after %s", op)
		}
		return &TestBinary{Op: op, X: &TestWord{W: w}, Y: &TestWord{W: rhs, Regex: op == "=~"}}
	}
	return &TestWord{W: w}
}

// regexWord reads the right side of =~, where ( ) and | are part of the
// pattern.
func (p *Parser) regexWord() *Word {
	w := &Word{Pos: p.pos}
	var lit strings.Builder
	depth := 0
	flush := func() {
		if lit.Len() > 0 {
			w.Parts = append(w.Parts, &Lit{Value: lit.String()})
			lit.Reset()
		}
	}
loop:
	for !p.eof() {
		c := p.peek()
		switch c {
		case ' ', '\t', '\n':
			if depth == 0 {
				break loop
			}
			lit.WriteByte(c)
			p.pos++
		case '(':
			depth++
			lit.WriteByte(c)
			p.pos++
		case ')':
			if depth == 0 {
				break loop
			}
			depth--
			lit.WriteByte(c)
			p.pos++
		case '\'':
			flush()
			w.Parts = append(w.Parts, p.sglQuoted())
		case '"':
			flush()
			w.Parts = append(w.Parts, p.dblQuoted())
		case '\\':
			flush()
			if p.pos+1 < len(p.src) {
				_, size := utf8.DecodeRuneInString(p.src[p.pos+1:])
				w.Parts = append(w.Parts, &Quoted{Value: p.src[p.pos+1 : p.pos+1+size]})
				p.pos += 1 + size
			} else {
				p.pos++
			}
		case '$':
			flush()
			if part := p.dollar(false); part != nil {
				w.Parts = append(w.Parts, part)
			} else {
				lit.WriteByte('$')
				p.pos++
			}
		default:
			if p.has("]]") && depth == 0 {
				break loop
			}
			lit.WriteByte(c)
			p.pos++
		}
	}
	flush()
	if len(w.Parts) == 0 {
		return nil
	}
	w.End = p.pos
	return w
}
