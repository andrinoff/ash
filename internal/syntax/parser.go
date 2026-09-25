package syntax

import (
	"fmt"
	"strconv"
	"strings"
)

// Error is a parse error. Incomplete errors mean the input ended too early
// (an open quote, block or heredoc), which the line editor uses to decide
// whether Enter should run the command or start a new line. Running out of
// input where the next line couldn't help either, as in `ls >` or `case`,
// is not Incomplete.
type Error struct {
	Pos        int
	Msg        string
	Incomplete bool
}

func (e *Error) Error() string { return e.Msg }

// Options tweak the parser.
type Options struct {
	// Lenient accepts a heredoc that runs to the end of the input, like
	// bash does for scripts.
	Lenient bool
	// Fish selects fish quoting rules: \' and \\ work inside single
	// quotes and backticks are ordinary characters. Used for .fish files.
	Fish bool
}

// Parse parses a complete program.
func Parse(src string) (*File, error) { return ParseOpts(src, Options{}) }

func ParseOpts(src string, opts Options) (f *File, err error) {
	p := &Parser{src: src, lenient: opts.Lenient, fish: opts.Fish}
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(*Error)
			if !ok {
				panic(r)
			}
			f, err = nil, e
		}
	}()
	stmts := p.stmtList(nil)
	p.skipSeparators()
	if !p.eof() {
		p.fail(p.pos, false, "unexpected %s", p.describe())
	}
	if len(p.heredocs) > 0 {
		if !p.lenient {
			p.fail(p.pos, true, "unterminated heredoc")
		}
		for _, h := range p.heredocs {
			h.r.Heredoc = &Word{}
		}
	}
	return &File{Stmts: stmts}, nil
}

// IsIncomplete reports whether err means "keep reading".
func IsIncomplete(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Incomplete
}

type Parser struct {
	src      string
	pos      int
	heredocs []*pendingHeredoc
	lenient  bool
	fish     bool
	depth    int
	braces   int // open { } blocks, for fish's "{ cmd }"
}

type pendingHeredoc struct {
	r      *Redirect
	delim  string
	strip  bool
	quoted bool
}

func (p *Parser) fail(pos int, incomplete bool, format string, args ...any) {
	panic(&Error{Pos: pos, Msg: fmt.Sprintf(format, args...), Incomplete: incomplete})
}

func (p *Parser) describe() string {
	if p.eof() {
		return "end of input"
	}
	c := p.src[p.pos]
	if c == '\n' {
		return "newline"
	}
	end := p.pos + 1
	for end < len(p.src) && end-p.pos < 12 && !isMeta(p.src[end]) {
		end++
	}
	return fmt.Sprintf("'%s'", p.src[p.pos:end])
}

func (p *Parser) eof() bool { return p.pos >= len(p.src) }

func (p *Parser) peek() byte {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

func (p *Parser) peekAt(off int) byte {
	if p.pos+off < len(p.src) {
		return p.src[p.pos+off]
	}
	return 0
}

func (p *Parser) has(s string) bool { return strings.HasPrefix(p.src[p.pos:], s) }

func isMeta(c byte) bool {
	switch c {
	case ' ', '\t', '\n', ';', '&', '|', '<', '>', '(', ')':
		return true
	}
	return false
}

func isNameStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isNameChar(c byte) bool { return isNameStart(c) || c >= '0' && c <= '9' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// skipBlanks skips spaces, tabs and line continuations.
func (p *Parser) skipBlanks() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\r':
			p.pos++
		case '\\':
			if p.peekAt(1) == '\n' {
				p.pos += 2
				if p.fish {
					p.skipContinuationComments()
				}
				continue
			}
			if p.pos+1 == len(p.src) {
				// the line goes on: `cmd & \`, `{ ...; } \`
				p.fail(p.pos, true, "trailing backslash")
			}
			return
		default:
			return
		}
	}
}

// skipContinuationComments lets fish code put comment lines inside a
// command continued with a backslash.
func (p *Parser) skipContinuationComments() {
	for {
		i := p.pos
		for i < len(p.src) && (p.src[i] == ' ' || p.src[i] == '\t') {
			i++
		}
		if i >= len(p.src) || p.src[i] != '#' {
			return
		}
		for i < len(p.src) && p.src[i] != '\n' {
			i++
		}
		if i < len(p.src) {
			i++
		}
		p.pos = i
	}
}

func (p *Parser) skipComment() {
	if p.peek() == '#' {
		for p.pos < len(p.src) && p.src[p.pos] != '\n' {
			p.pos++
		}
	}
}

// skipSpace skips blanks, comments and newlines (reading heredoc bodies).
func (p *Parser) skipSpace() {
	for {
		p.skipBlanks()
		p.skipComment()
		if p.peek() == '\n' {
			p.newline()
			continue
		}
		return
	}
}

// skipSeparators is skipSpace that also eats lone semicolons.
func (p *Parser) skipSeparators() {
	for {
		p.skipSpace()
		if p.peek() == ';' && p.peekAt(1) != ';' && p.peekAt(1) != '&' {
			p.pos++
			continue
		}
		return
	}
}

// newline consumes a '\n' and reads any pending heredoc bodies.
func (p *Parser) newline() {
	p.pos++
	if len(p.heredocs) == 0 {
		return
	}
	pending := p.heredocs
	p.heredocs = nil
	for _, h := range pending {
		p.readHeredoc(h)
	}
}

func (p *Parser) readHeredoc(h *pendingHeredoc) {
	var body strings.Builder
	var starts []int // offset of each body line in the body, then in p.src
	for {
		if p.eof() {
			if !p.lenient {
				p.heredocs = append(p.heredocs, h)
				p.fail(p.pos, true, "heredoc delimited by end of input (wanted %q)", h.delim)
			}
			break
		}
		start := p.pos
		end := strings.IndexByte(p.src[p.pos:], '\n')
		var line string
		if end < 0 {
			line = p.src[p.pos:]
			p.pos = len(p.src)
		} else {
			line = p.src[p.pos : p.pos+end]
			p.pos += end + 1
		}
		if h.strip {
			trimmed := strings.TrimLeft(line, "\t")
			start += len(line) - len(trimmed)
			line = trimmed
		}
		if line == h.delim {
			break
		}
		starts = append(starts, body.Len(), start)
		body.WriteString(line)
		body.WriteByte('\n')
	}
	text := body.String()
	if h.quoted {
		h.r.Heredoc = &Word{Parts: []WordPart{&Quoted{Value: text}}}
		return
	}
	sub := &Parser{src: text, lenient: true}
	srcPos := func(pos int) int {
		if len(starts) == 0 {
			return 0
		}
		i := 0
		for i+2 < len(starts) && starts[i+2] <= pos {
			i += 2
		}
		return starts[i+1] + pos - starts[i]
	}
	p.nested(srcPos, func() { h.r.Heredoc = sub.heredocBody() })
}

// nested runs parse, which parses text cut out of the source: a heredoc
// body, the inside of `...` or of a subscript. That text has already been
// read to its end, so an error in it can't be fixed by reading more input.
// srcPos maps a position in the text to one in the source.
func (p *Parser) nested(srcPos func(int) int, parse func()) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*Error); ok {
				e.Pos = min(max(srcPos(e.Pos), 0), len(p.src))
				e.Incomplete = false
			}
			panic(r)
		}
	}()
	parse()
}

func (p *Parser) heredocBody() *Word {
	w := &Word{}
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			w.Parts = append(w.Parts, &Lit{Value: lit.String()})
			lit.Reset()
		}
	}
	for !p.eof() {
		c := p.src[p.pos]
		switch c {
		case '\\':
			n := p.peekAt(1)
			switch n {
			case '$', '`', '\\':
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
				w.Parts = append(w.Parts, part)
			} else {
				lit.WriteByte('$')
				p.pos++
			}
		case '`':
			flush()
			w.Parts = append(w.Parts, p.backquote())
		default:
			lit.WriteByte(c)
			p.pos++
		}
	}
	flush()
	// Mark everything as quoted: heredoc text never globs or splits.
	return &Word{Parts: []WordPart{&DblQuoted{Parts: w.Parts}}}
}

var keywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"while": true, "until": true, "for": true, "in": true, "do": true, "done": true,
	"case": true, "esac": true, "function": true, "time": true,
	"{": true, "}": true, "!": true, "[[": true,
	"begin": true, "end": true, "switch": true, "and": true, "or": true, "not": true,
}

// IsKeyword reports whether s is reserved at the start of a command.
func IsKeyword(s string) bool { return keywords[s] }

// peekKeyword returns the reserved word at the cursor, or "".
func (p *Parser) peekKeyword() string {
	i := p.pos
	for i < len(p.src) {
		c := p.src[i]
		if c >= 'a' && c <= 'z' || c == '{' || c == '}' || c == '!' || c == '[' {
			i++
			continue
		}
		break
	}
	if i == p.pos {
		return ""
	}
	if i < len(p.src) && !isMeta(p.src[i]) {
		return ""
	}
	w := p.src[p.pos:i]
	if keywords[w] {
		return w
	}
	return ""
}

func (p *Parser) isKeyword(kws ...string) bool {
	k := p.peekKeyword()
	if k == "" {
		return false
	}
	for _, w := range kws {
		if k == w {
			return true
		}
	}
	return false
}

func (p *Parser) expectKeyword(kw, what string) {
	p.skipSeparators()
	if p.peekKeyword() != kw {
		p.fail(p.pos, p.eof(), "expected '%s' to close %s, found %s", kw, what, p.describe())
	}
	p.pos += len(kw)
}

func (p *Parser) enter() {
	p.depth++
	if p.depth > 1000 {
		p.fail(p.pos, false, "nesting too deep")
	}
}

func (p *Parser) leave() { p.depth-- }

// stmtList parses statements until EOF, ')' or stop() says so.
func (p *Parser) stmtList(stop func() bool) []*Stmt {
	p.enter()
	defer p.leave()
	var list []*Stmt
	for {
		p.skipSeparators()
		if p.eof() || p.peek() == ')' || p.has(";;") || p.has(";&") {
			return list
		}
		if stop != nil && stop() {
			return list
		}
		st := p.stmt()
		list = append(list, st)
		p.skipBlanks()
		switch {
		case p.eof(), p.peek() == '\n', p.peek() == '#', p.peek() == ')':
		case p.has(";;"), p.has(";&"):
			return list
		case p.peek() == ';':
			p.pos++
		case st.Background:
			// `a & b`: the & ends the statement
		default:
			if stop != nil && stop() {
				return list
			}
			p.fail(p.pos, false, "unexpected %s", p.describe())
		}
	}
}

func (p *Parser) stmt() *Stmt {
	st := &Stmt{Pos: p.pos}
	switch kw := p.peekKeyword(); kw {
	case "and", "or":
		p.pos += len(kw)
		st.Cond = CondAnd
		if kw == "or" {
			st.Cond = CondOr
		}
		p.skipBlanks()
		if p.eof() || p.peek() == '\n' || p.peek() == ';' || p.peek() == '#' {
			p.fail(p.pos, false, "expected a command after '%s'", kw)
		}
	}
	p.skipBlanks()
	st.Cmd = p.andOr()
	p.skipBlanks()
	if p.peek() == '&' && !p.has("&&") && !p.has("&>") && !p.has("&|") {
		p.pos++
		st.Background = true
		if p.peek() == '!' {
			p.pos++
			st.Disown = true
		}
	}
	st.End = p.pos
	return st
}

func (p *Parser) andOr() Command {
	x := p.pipeline()
	for {
		p.skipBlanks()
		var op BinOp
		switch {
		case p.has("&&"):
			op = AndOp
		case p.has("||"):
			op = OrOp
		default:
			return x
		}
		p.pos += 2
		p.skipSpace()
		if p.eof() {
			p.fail(p.pos, true, "expected a command after '%s'", map[BinOp]string{AndOp: "&&", OrOp: "||"}[op])
		}
		y := p.pipeline()
		x = &BinaryCmd{Op: op, X: x, Y: y}
	}
}

func (p *Parser) pipeline() Command {
	pos := p.pos
	negate, timed, bang := false, false, false
	for {
		switch p.peekKeyword() {
		case "!":
			p.pos++
			negate, bang = !negate, true
			p.skipBlanks()
			continue
		case "not":
			p.pos += 3
			negate, bang = !negate, true
			p.skipBlanks()
			continue
		case "time":
			p.pos += 4
			timed = true
			p.skipBlanks()
			if p.has("-p") && (p.pos+2 >= len(p.src) || isMeta(p.src[p.pos+2])) {
				p.pos += 2
				p.skipBlanks()
			}
			continue
		}
		break
	}
	if p.eof() || p.peek() == '\n' || p.peek() == ';' {
		if bang || timed {
			if timed && !bang {
				// bare `time` is valid in bash; treat it as `time true`
				return &Pipeline{Pos: pos, Time: true, Cmds: []Command{&CallExpr{Pos: pos, Args: []*Word{{Parts: []WordPart{&Lit{Value: "true"}}}}}}, PipeErr: []bool{false}}
			}
			p.fail(p.pos, false, "expected a command")
		}
	}
	first := p.command()
	cmds := []Command{first}
	pipeErr := []bool{false}
	errOnly := map[int]bool{}
	for {
		p.skipBlanks()
		if p.has("||") {
			break
		}
		if p.has("|&") || p.has("&|") {
			pipeErr[len(pipeErr)-1] = true
			p.pos += 2
		} else if fd, n := p.fishPipe(); p.fish && n > 0 {
			if fd == 2 {
				errOnly[len(cmds)-1] = true
			}
			p.pos += n
		} else if p.peek() == '|' {
			p.pos++
		} else {
			break
		}
		p.skipSpace()
		if p.eof() {
			p.fail(p.pos, true, "expected a command after '|'")
		}
		cmds = append(cmds, p.command())
		pipeErr = append(pipeErr, false)
	}
	if len(cmds) == 1 && !negate && !timed {
		return first
	}
	pl := &Pipeline{Pos: pos, Negate: negate, Time: timed, Cmds: cmds, PipeErr: pipeErr}
	for i := range errOnly {
		if pl.ErrOnly == nil {
			pl.ErrOnly = make([]bool, len(cmds))
		}
		pl.ErrOnly[i] = true
	}
	return pl
}

func (p *Parser) command() Command {
	p.enter()
	defer p.leave()
	p.skipBlanks()
	if p.eof() {
		p.fail(p.pos, true, "expected a command")
	}
	switch c := p.peek(); c {
	case '(':
		if p.has("((") {
			if end := p.matchArith(p.pos + 2); end >= 0 {
				return p.arithCmd()
			}
		}
		p.pos++
		stmts := p.stmtList(nil)
		p.skipSpace()
		if p.eof() {
			p.fail(p.pos, true, "unterminated subshell")
		}
		if p.peek() != ')' {
			p.fail(p.pos, false, "expected ')', found %s", p.describe())
		}
		p.pos++
		return &Subshell{Stmts: stmts, Redirs: p.trailingRedirs()}
	case ')', ';', '|', '&':
		if c == '&' && p.has("&>") {
			break
		}
		p.fail(p.pos, false, "unexpected %s", p.describe())
	}
	switch kw := p.peekKeyword(); kw {
	case "{":
		p.pos++
		p.braces++
		stmts := p.stmtList(func() bool { return p.isKeyword("}") })
		p.braces--
		p.expectKeyword("}", "'{'")
		return &Block{Stmts: stmts, Redirs: p.trailingRedirs()}
	case "begin":
		p.pos += 5
		stmts := p.stmtList(func() bool { return p.isKeyword("end") })
		p.expectKeyword("end", "'begin'")
		return &Block{Stmts: stmts, Redirs: p.trailingRedirs(), Fish: true}
	case "if":
		return p.ifClause()
	case "while", "until":
		return p.whileClause(kw == "until")
	case "for":
		return p.forClause()
	case "case":
		return p.caseClause()
	case "switch":
		return p.switchClause()
	case "function":
		return p.funcDecl()
	case "[[":
		return p.testClause()
	case "then", "else", "elif", "fi", "do", "done", "esac", "end", "}", "in":
		p.fail(p.pos, false, "unexpected '%s'", kw)
	}
	if fd := p.shFuncDef(); fd != nil {
		return fd
	}
	return p.simpleCommand()
}

func (p *Parser) trailingRedirs() []*Redirect {
	var rs []*Redirect
	for {
		p.skipBlanks()
		r := p.redirect()
		if r == nil {
			return rs
		}
		rs = append(rs, r)
	}
}

func (p *Parser) atCommandEnd() bool {
	if p.eof() {
		return true
	}
	switch p.peek() {
	case '\n', ';', '|', ')', '#':
		return true
	case '&':
		return !p.has("&>")
	}
	if p.fish {
		if _, n := p.fishPipe(); n > 0 {
			return true
		}
		if p.braces > 0 && p.isKeyword("}") {
			return true
		}
	}
	return false
}

// fishPipe recognizes fish's "2>|" (pipe a specific fd). It returns the fd
// and the length of the operator.
func (p *Parser) fishPipe() (int, int) {
	i := p.pos
	for i < len(p.src) && isDigit(p.src[i]) {
		i++
	}
	if !strings.HasPrefix(p.src[i:], ">|") {
		return 0, 0
	}
	fd := 1
	if i > p.pos {
		fd, _ = strconv.Atoi(p.src[p.pos:i])
	}
	return fd, i + 2 - p.pos
}

func (p *Parser) simpleCommand() Command {
	call := &CallExpr{Pos: p.pos}
	decl := ""
	for {
		p.skipBlanks()
		if r := p.redirect(); r != nil {
			call.Redirs = append(call.Redirs, r)
			continue
		}
		if p.atCommandEnd() {
			break
		}
		if len(call.Args) == 0 {
			if a := p.assign(); a != nil {
				call.Assigns = append(call.Assigns, a)
				continue
			}
			if len(call.Assigns) > 0 {
				switch p.peekKeyword() {
				case "if", "while", "until", "for", "begin", "switch", "case", "{":
					return &EnvCmd{Assigns: call.Assigns, Cmd: p.command()}
				}
			}
		}
		if decl != "" {
			start := p.pos
			if a := p.declAssign(decl); a != nil {
				for len(call.DeclArgs) < len(call.Args) {
					call.DeclArgs = append(call.DeclArgs, nil)
				}
				call.DeclArgs = append(call.DeclArgs, a)
				op := "="
				if a.Append {
					op = "+="
				}
				w := &Word{Pos: start, End: p.pos, Parts: []WordPart{&Lit{Value: a.Name + op}}}
				if !a.IsArray {
					w.Parts = append(w.Parts, a.Value.Parts...)
				}
				call.Args = append(call.Args, w)
				continue
			}
		}
		w := p.word(ctxNormal)
		if w == nil {
			p.fail(p.pos, false, "unexpected %s", p.describe())
		}
		call.Args = append(call.Args, w)
		if len(call.Args) == 1 {
			decl = declBuiltin(w, p.fish)
		}
	}
	if len(call.Args) == 0 && len(call.Assigns) == 0 && len(call.Redirs) == 0 {
		p.fail(p.pos, p.eof(), "expected a command, found %s", p.describe())
	}
	for call.DeclArgs != nil && len(call.DeclArgs) < len(call.Args) {
		call.DeclArgs = append(call.DeclArgs, nil)
	}
	call.End = p.pos
	return call
}

// declBuiltin returns the name of the builtin that w names if that
// builtin's name=value arguments are assignments, as in bash, and ""
// otherwise.
func declBuiltin(w *Word, fish bool) string {
	switch name, _ := w.IsUnquotedLit(); name {
	case "local", "declare", "typeset", "readonly":
		return name
	case "export":
		if !fish { // fish's export takes ordinary words
			return name
		}
	}
	return ""
}

// declAssign parses an argument of a declaration builtin that is an
// assignment (local x=~/a, declare -a arr=(1 2)), or returns nil. For
// export, X=(cmd) keeps its fish meaning, a command substitution.
func (p *Parser) declAssign(builtin string) *Assign {
	i := p.pos
	if i >= len(p.src) || !isNameStart(p.src[i]) {
		return nil
	}
	for i < len(p.src) && isNameChar(p.src[i]) {
		i++
	}
	switch {
	case strings.HasPrefix(p.src[i:], "+="):
		i += 2
	case strings.HasPrefix(p.src[i:], "="):
		i++
	default:
		return nil
	}
	if builtin == "export" && i < len(p.src) && p.src[i] == '(' {
		return nil
	}
	return p.assign()
}

func litWord(s string) *Word { return &Word{Parts: []WordPart{&Lit{Value: s}}} }

func (p *Parser) redirect() *Redirect {
	if p.fish {
		if _, n := p.fishPipe(); n > 0 {
			return nil
		}
	}
	start := p.pos
	i := p.pos
	fd := -1
	for i < len(p.src) && isDigit(p.src[i]) {
		i++
	}
	if i > p.pos && i < len(p.src) && (p.src[i] == '<' || p.src[i] == '>') {
		n, err := strconv.Atoi(p.src[p.pos:i])
		if err == nil {
			fd = n
		}
	} else {
		i = p.pos
	}
	rest := p.src[i:]
	var op RedirOp
	n := 0
	switch {
	case strings.HasPrefix(rest, "&>>"):
		op, n = RedirAllAppend, 3
	case strings.HasPrefix(rest, "&>"):
		op, n = RedirAll, 2
	case strings.HasPrefix(rest, "<<<"):
		op, n = RedirHerestring, 3
	case strings.HasPrefix(rest, "<<-"):
		op, n = RedirHeredocTabs, 3
	case strings.HasPrefix(rest, "<<"):
		op, n = RedirHeredoc, 2
	case strings.HasPrefix(rest, "<&"):
		op, n = RedirDupIn, 2
	case strings.HasPrefix(rest, "<>"):
		op, n = RedirInOut, 2
	case strings.HasPrefix(rest, "<("), strings.HasPrefix(rest, ">("):
		return nil
	case strings.HasPrefix(rest, "<"):
		op, n = RedirIn, 1
	case strings.HasPrefix(rest, ">>"):
		op, n = RedirAppend, 2
	case strings.HasPrefix(rest, ">&"):
		op, n = RedirDupOut, 2
	case strings.HasPrefix(rest, ">|"):
		op, n = RedirClobber, 2
	case strings.HasPrefix(rest, ">?"):
		op, n = RedirNoClobber, 2
	case strings.HasPrefix(rest, ">"):
		op, n = RedirOut, 1
	default:
		return nil
	}
	if (op == RedirAll || op == RedirAllAppend) && fd != -1 {
		return nil
	}
	p.pos = i + n
	p.skipBlanks()
	r := &Redirect{Pos: start, Fd: fd, Op: op}
	// the file can be a process substitution: done < <(cmd)
	if p.atCommandEnd() || (p.peek() == '<' || p.peek() == '>') && p.peekAt(1) != '(' {
		p.fail(p.pos, false, "expected a file name after '%s'", op)
	}
	w := p.word(ctxNormal)
	if w == nil {
		p.fail(p.pos, false, "expected a file name after '%s'", op)
	}
	if op == RedirHeredoc || op == RedirHeredocTabs {
		delim, quoted := heredocDelim(w)
		p.heredocs = append(p.heredocs, &pendingHeredoc{r: r, delim: delim, strip: op == RedirHeredocTabs, quoted: quoted})
	}
	r.Target = w
	return r
}

func heredocDelim(w *Word) (string, bool) {
	var b strings.Builder
	quoted := false
	var walk func(parts []WordPart)
	walk = func(parts []WordPart) {
		for _, part := range parts {
			switch part := part.(type) {
			case *Lit:
				b.WriteString(part.Value)
			case *Quoted:
				quoted = true
				b.WriteString(part.Value)
			case *DblQuoted:
				quoted = true
				walk(part.Parts)
			case *ParamExp:
				b.WriteString("$" + part.Name)
			}
		}
	}
	walk(w.Parts)
	return b.String(), quoted
}

// assign recognizes name=value, name+=value, name[i]=value and name=(...).
func (p *Parser) assign() *Assign {
	i := p.pos
	if i >= len(p.src) || !isNameStart(p.src[i]) {
		return nil
	}
	for i < len(p.src) && isNameChar(p.src[i]) {
		i++
	}
	name := p.src[p.pos:i]
	idxStart, idxEnd := -1, -1
	if i < len(p.src) && p.src[i] == '[' {
		depth := 0
		j := i
		for j < len(p.src) {
			if p.src[j] == '[' {
				depth++
			} else if p.src[j] == ']' {
				depth--
				if depth == 0 {
					break
				}
			} else if p.src[j] == '\n' {
				return nil
			}
			j++
		}
		if j >= len(p.src) {
			return nil
		}
		idxStart, idxEnd = i+1, j
		i = j + 1
	}
	app := false
	if i+1 < len(p.src) && p.src[i] == '+' && p.src[i+1] == '=' {
		app = true
		i++
	}
	if i >= len(p.src) || p.src[i] != '=' {
		return nil
	}
	a := &Assign{Pos: p.pos, Name: name, Append: app}
	if idxStart >= 0 {
		sub := &Parser{src: p.src[idxStart:idxEnd]}
		p.nested(func(pos int) int { return idxStart + pos }, func() {
			a.Index = sub.arithWord(func(int) bool { return false })
		})
	}
	p.pos = i + 1
	if p.peek() == '(' {
		p.pos++
		a.IsArray = true
		for {
			p.skipSpace()
			if p.eof() {
				p.fail(p.pos, true, "unterminated array")
			}
			if p.peek() == ')' {
				p.pos++
				break
			}
			w := p.word(ctxNormal)
			if w == nil {
				p.fail(p.pos, false, "unexpected %s in array", p.describe())
			}
			a.Array = append(a.Array, w)
		}
		return a
	}
	if p.eof() || isMeta(p.peek()) {
		a.Value = &Word{Pos: p.pos, End: p.pos}
		return a
	}
	a.Value = p.word(ctxAssign)
	if a.Value == nil {
		a.Value = &Word{Pos: p.pos, End: p.pos}
	}
	return a
}

// LineCol converts a byte offset to 1-based line and column.
func LineCol(src string, pos int) (int, int) {
	if pos > len(src) {
		pos = len(src)
	}
	line := 1 + strings.Count(src[:pos], "\n")
	col := pos - strings.LastIndexByte(src[:pos], '\n')
	return line, col
}
