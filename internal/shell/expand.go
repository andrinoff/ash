package shell

import (
	"fmt"
	"io"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/andrinoff/ash/internal/syntax"
)

// field is one output word under construction.
type field struct {
	text []byte
	pat  []byte // glob pattern: quoted metacharacters escaped
	glob bool
	open bool
}

func (f *field) add(s string, quoted bool) {
	f.text = append(f.text, s...)
	if quoted {
		for i := 0; i < len(s); i++ {
			if isPatternByte(s[i]) {
				f.pat = append(f.pat, '\\')
			}
			f.pat = append(f.pat, s[i])
		}
		return
	}
	f.pat = append(f.pat, s...)
	if !f.glob && isGlobPattern(s) {
		f.glob = true
	}
}

// isGlobPattern reports whether unquoted text would glob: it has * or ?,
// or a [ with a matching ]. A lone "[" (the test command) is just text.
func isGlobPattern(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*', '?':
			return true
		case '[':
			j := i + 1
			if j < len(s) && (s[j] == '!' || s[j] == '^') {
				j++
			}
			if j < len(s) && s[j] == ']' {
				j++
			}
			if k := strings.IndexByte(s[j:], ']'); k >= 0 {
				return true
			}
		}
	}
	return false
}

func (f *field) copy() *field {
	return &field{text: append([]byte(nil), f.text...), pat: append([]byte(nil), f.pat...), glob: f.glob, open: f.open}
}

type expander struct {
	sh      *Shell
	io      *IO
	fields  []*field
	content bool
	fish    bool
}

func (e *expander) appendAll(s string, quoted bool) {
	e.content = true
	for _, f := range e.fields {
		if f.open {
			f.add(s, quoted)
		}
	}
}

// cartesian multiplies the open fields by vals (fish list semantics).
func (e *expander) cartesian(vals []string) {
	if len(vals) == 0 {
		return
	}
	if len(vals) == 1 {
		e.appendAll(vals[0], true)
		return
	}
	e.content = true
	var nf []*field
	for _, f := range e.fields {
		if !f.open {
			nf = append(nf, f)
			continue
		}
		for _, v := range vals {
			g := f.copy()
			g.add(v, true)
			nf = append(nf, g)
		}
	}
	e.fields = nf
}

// split turns vals into separate fields, joining the first with what
// came before and the last with what comes after (sh "$@" semantics).
func (e *expander) split(vals []string) {
	if len(vals) == 0 {
		return
	}
	e.content = true
	var nf []*field
	for _, f := range e.fields {
		if !f.open {
			nf = append(nf, f)
			continue
		}
		f.add(vals[0], true)
		if len(vals) == 1 {
			nf = append(nf, f)
			continue
		}
		f.open = false
		nf = append(nf, f)
		for i := 1; i < len(vals); i++ {
			g := &field{open: i == len(vals)-1}
			g.add(vals[i], true)
			nf = append(nf, g)
		}
	}
	e.fields = nf
}

type expandFlags uint8

const (
	expNullGlob expandFlags = 1 << iota // unmatched globs vanish (for, set)
	expNoGlob
)

// expandFields fully expands words into argument fields.
func (sh *Shell) expandFields(words []*syntax.Word, io *IO, flags expandFlags) ([]string, error) {
	var out []string
	for _, w := range words {
		fs, err := sh.expandWord(w, io, flags)
		if err != nil {
			return nil, err
		}
		out = append(out, fs...)
	}
	return out, nil
}

func (sh *Shell) expandWord(w *syntax.Word, io *IO, flags expandFlags) ([]string, error) {
	// Fast path: a plain literal with no glob characters.
	if len(w.Parts) == 1 {
		if l, ok := w.Parts[0].(*syntax.Lit); ok && !isGlobPattern(l.Value) {
			return []string{l.Value}, nil
		}
		if q, ok := w.Parts[0].(*syntax.Quoted); ok {
			return []string{q.Value}, nil
		}
	}
	var out []string
	for _, parts := range expandBraces(w.Parts) {
		e := &expander{sh: sh, io: io, fields: []*field{{open: true}}}
		if err := e.parts(parts, false); err != nil {
			return nil, err
		}
		if !e.content {
			continue
		}
		for _, f := range e.fields {
			if f.glob && flags&expNoGlob == 0 && !sh.opts.noglob {
				matches := sh.glob(string(f.pat))
				if len(matches) > 0 {
					out = append(out, matches...)
					continue
				}
				if flags&expNullGlob != 0 {
					continue
				}
			}
			out = append(out, string(f.text))
		}
	}
	return out, nil
}

// expandString expands a word to a single string: no splitting, no globs.
func (sh *Shell) expandString(w *syntax.Word, io *IO) (string, error) {
	if w == nil {
		return "", nil
	}
	if s, ok := w.LitString(); ok {
		return s, nil
	}
	var b strings.Builder
	if err := sh.expandToString(&b, w.Parts, io, false); err != nil {
		return "", err
	}
	return b.String(), nil
}

func (sh *Shell) expandToString(b *strings.Builder, parts []syntax.WordPart, io *IO, inDQ bool) error {
	for _, p := range parts {
		switch p := p.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.Quoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			if err := sh.expandToString(b, p.Parts, io, true); err != nil {
				return err
			}
		case *syntax.BraceExp:
			// No brace expansion in string context: rebuild the text.
			err := braceString(b, p, func(alt []syntax.WordPart) error {
				return sh.expandToString(b, alt, io, inDQ)
			})
			if err != nil {
				return err
			}
		default:
			vals, kind, err := sh.expandPart(p, io, true)
			if err != nil {
				return err
			}
			sep := " "
			if kind == kindPathList {
				sep = ":"
			}
			b.WriteString(strings.Join(vals, sep))
		}
	}
	return nil
}

// braceString writes a brace group where braces don't expand (an
// assignment, a case pattern, [[ ]]). The alternatives still expand like
// the rest of the word, so x={a,$y} is "{a,Z}" when y is Z, as in bash.
// alt writes one alternative.
func braceString(b *strings.Builder, be *syntax.BraceExp, alt func([]syntax.WordPart) error) error {
	if be.Seq != nil {
		b.WriteString(braceText(be))
		return nil
	}
	b.WriteByte('{')
	for i, parts := range be.Alts {
		if i > 0 {
			b.WriteByte(',')
		}
		if len(parts) > 0 {
			// Not at the start of the word: {a,~} keeps its ~.
			if t, ok := parts[0].(*syntax.Tilde); ok {
				parts = append([]syntax.WordPart{&syntax.Lit{Value: "~" + t.User}}, parts[1:]...)
			}
		}
		if err := alt(parts); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

// expandPattern expands a word into a glob pattern. Quoted text is
// escaped unless fish is set (fish case patterns ignore quoting).
func (sh *Shell) expandPattern(w *syntax.Word, io *IO, fish bool) (string, error) {
	var b strings.Builder
	err := sh.patternParts(&b, w.Parts, io, false, fish)
	return b.String(), err
}

func (sh *Shell) patternParts(b *strings.Builder, parts []syntax.WordPart, io *IO, quoted, fish bool) error {
	for _, p := range parts {
		switch p := p.(type) {
		case *syntax.Lit:
			if quoted && !fish {
				b.WriteString(escapeGlob(p.Value))
			} else {
				b.WriteString(p.Value)
			}
		case *syntax.Quoted:
			if fish {
				b.WriteString(p.Value)
			} else {
				b.WriteString(escapeGlob(p.Value))
			}
		case *syntax.DblQuoted:
			if err := sh.patternParts(b, p.Parts, io, true, fish); err != nil {
				return err
			}
		case *syntax.BraceExp:
			if p.Seq != nil {
				b.WriteString(escapeGlob(braceText(p)))
				continue
			}
			err := braceString(b, p, func(alt []syntax.WordPart) error {
				return sh.patternParts(b, alt, io, quoted, fish)
			})
			if err != nil {
				return err
			}
		default:
			vals, kind, err := sh.expandPart(p, io, quoted)
			if err != nil {
				return err
			}
			sep := " "
			if kind == kindPathList {
				sep = ":"
			}
			s := strings.Join(vals, sep)
			if quoted && !fish {
				s = escapeGlob(s)
			}
			b.WriteString(s)
		}
	}
	return nil
}

// expandRegex expands the right side of =~: quoted parts match literally.
func (sh *Shell) expandRegex(w *syntax.Word, io *IO) (string, error) {
	var b strings.Builder
	var walk func(parts []syntax.WordPart, quoted bool) error
	walk = func(parts []syntax.WordPart, quoted bool) error {
		for _, p := range parts {
			switch p := p.(type) {
			case *syntax.Lit:
				if quoted {
					b.WriteString(regexp.QuoteMeta(p.Value))
				} else {
					b.WriteString(p.Value)
				}
			case *syntax.Quoted:
				b.WriteString(regexp.QuoteMeta(p.Value))
			case *syntax.DblQuoted:
				if err := walk(p.Parts, true); err != nil {
					return err
				}
			case *syntax.BraceExp:
				err := braceString(&b, p, func(alt []syntax.WordPart) error {
					return walk(alt, quoted)
				})
				if err != nil {
					return err
				}
			default:
				vals, _, err := sh.expandPart(p, io, quoted)
				if err != nil {
					return err
				}
				s := strings.Join(vals, " ")
				if quoted {
					s = regexp.QuoteMeta(s)
				}
				b.WriteString(s)
			}
		}
		return nil
	}
	err := walk(w.Parts, false)
	return b.String(), err
}

// isPatternByte reports whether c means something in a glob pattern,
// so that quoted text has to escape it. ! ^ and - only matter inside
// brackets, but [\!a] and [a"-"z] must not turn into a negation or a
// range.
func isPatternByte(c byte) bool {
	switch c {
	case '*', '?', '[', ']', '\\', '!', '^', '-':
		return true
	}
	return false
}

func escapeGlob(s string) string {
	if !strings.ContainsAny(s, "*?[]\\!^-") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if isPatternByte(s[i]) {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

type valKind uint8

const (
	kindScalar   valKind = iota
	kindList             // fish list: cartesian product when unquoted
	kindSplit            // "$@"-style: separate words, joined at the edges
	kindPathList         // like kindList but joined with ':' in quotes
	kindIFS              // unquoted $(...): split on IFS
)

func (e *expander) parts(parts []syntax.WordPart, inDQ bool) error {
	sh := e.sh
	for _, p := range parts {
		switch p := p.(type) {
		case *syntax.Lit:
			e.appendAll(p.Value, inDQ)
		case *syntax.Quoted:
			e.appendAll(p.Value, true)
		case *syntax.DblQuoted:
			// "$@" with no arguments produces no word at all, and so
			// do "${@:2}", "${arr[@]/x/y}" and the like when they come
			// out empty. Anything else in quotes makes a word.
			if len(p.Parts) == 1 {
				if pe, ok := p.Parts[0].(*syntax.ParamExp); ok {
					vals, kind, err := sh.expandPart(pe, e.io, true)
					if err != nil {
						return err
					}
					if kind == kindSplit {
						e.split(vals)
						continue
					}
					sep := " "
					if kind == kindPathList {
						sep = ":"
					}
					e.appendAll(strings.Join(vals, sep), true)
					continue
				}
			}
			e.content = true
			if err := e.parts(p.Parts, true); err != nil {
				return err
			}
		case *syntax.Tilde:
			e.appendAll(sh.expandTilde(p.User), true)
		default:
			vals, kind, err := sh.expandPart(p, e.io, inDQ)
			if err != nil {
				return err
			}
			if inDQ {
				if kind == kindSplit {
					e.split(vals)
					continue
				}
				sep := " "
				if kind == kindPathList {
					sep = ":"
				}
				e.appendAll(strings.Join(vals, sep), true)
				continue
			}
			switch kind {
			case kindScalar:
				if len(vals) == 0 {
					continue
				}
				e.appendAll(strings.Join(vals, " "), true)
			case kindList, kindPathList:
				e.cartesian(vals)
			case kindSplit:
				e.split(vals)
			case kindIFS:
				e.split(sh.ifsSplit(strings.Join(vals, "")))
			}
		}
	}
	return nil
}

func (sh *Shell) expandTilde(user string) string {
	switch user {
	case "":
		return sh.HomeDir()
	case "+":
		return sh.dir
	case "-":
		if v := sh.Get("OLDPWD"); v != "" {
			return v
		}
		return "~-"
	}
	if u, err := lookupUser(user); err == nil {
		return u
	}
	return "~" + user
}

func lookupUser(name string) (string, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}

// ifsFirst returns the first character of $IFS, which joins "$*" and
// "${arr[*]}": a space when IFS is unset, nothing when it is empty.
func (sh *Shell) ifsFirst() string {
	v, ok := sh.getVar("IFS")
	if !ok {
		return " "
	}
	s := strings.Join(v, "")
	_, n := utf8.DecodeRuneInString(s)
	return s[:n]
}

// ifsSplit splits s on $IFS the way sh does for unquoted $(...).
func (sh *Shell) ifsSplit(s string) []string {
	ifs := " \t\n"
	if v, ok := sh.getVar("IFS"); ok {
		ifs = strings.Join(v, "")
	}
	if ifs == "" {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	isWS := func(r rune) bool { return (r == ' ' || r == '\t' || r == '\n') && strings.ContainsRune(ifs, r) }
	isIFS := func(r rune) bool { return strings.ContainsRune(ifs, r) }
	rs := []rune(s)
	i, n := 0, len(rs)
	for i < n && isWS(rs[i]) {
		i++
	}
	for n > i && isWS(rs[n-1]) {
		n--
	}
	if i >= n {
		return nil
	}
	var out []string
	start := i
	for i < n {
		if !isIFS(rs[i]) {
			i++
			continue
		}
		out = append(out, string(rs[start:i]))
		if isWS(rs[i]) {
			for i < n && isWS(rs[i]) {
				i++
			}
			if i < n && !isWS(rs[i]) && isIFS(rs[i]) {
				i++
				for i < n && isWS(rs[i]) {
					i++
				}
			}
		} else {
			i++
			for i < n && isWS(rs[i]) {
				i++
			}
		}
		start = i
	}
	if start < n {
		out = append(out, string(rs[start:n]))
	}
	return out
}

// expandPart expands a single non-literal part.
func (sh *Shell) expandPart(p syntax.WordPart, io *IO, quoted bool) ([]string, valKind, error) {
	switch p := p.(type) {
	case *syntax.ParamExp:
		return sh.expandParam(p, io, quoted)
	case *syntax.CmdSubst:
		out, err := sh.cmdSubst(p.Stmts, io)
		if err != nil {
			return nil, kindScalar, err
		}
		if quoted {
			return []string{strings.TrimRight(out, "\n")}, kindScalar, nil
		}
		if p.Fish {
			lines := splitLines(out)
			if p.Index != nil {
				var err error
				lines, err = sh.applyFishIndex(lines, p.Index, io)
				if err != nil {
					return nil, kindScalar, err
				}
			}
			return lines, kindList, nil
		}
		// Trailing newlines go before splitting, as in sh: with IFS=:
		// they would otherwise end up in the last field, and in a
		// pattern (case $(...), ${x#$(...)}) they would have to match.
		return []string{strings.TrimRight(out, "\n")}, kindIFS, nil
	case *syntax.ArithExp:
		s, err := sh.expandString(p.Expr, io)
		if err != nil {
			return nil, kindScalar, err
		}
		n, err := sh.arith(s)
		if err != nil {
			return nil, kindScalar, err
		}
		return []string{strconv.FormatInt(n, 10)}, kindScalar, nil
	case *syntax.ProcSubst:
		path, err := sh.procSubst(p, io)
		if err != nil {
			return nil, kindScalar, err
		}
		return []string{path}, kindScalar, nil
	case *syntax.Tilde:
		return []string{sh.expandTilde(p.User)}, kindScalar, nil
	case *syntax.Lit:
		return []string{p.Value}, kindScalar, nil
	case *syntax.Quoted:
		return []string{p.Value}, kindScalar, nil
	case *syntax.DblQuoted:
		var b strings.Builder
		if err := sh.expandToString(&b, p.Parts, io, true); err != nil {
			return nil, kindScalar, err
		}
		return []string{b.String()}, kindScalar, nil
	case *syntax.BraceExp:
		var b strings.Builder
		if err := sh.expandToString(&b, []syntax.WordPart{p}, io, quoted); err != nil {
			return nil, kindScalar, err
		}
		return []string{b.String()}, kindScalar, nil
	}
	return nil, kindScalar, fmt.Errorf("unsupported expansion")
}

// splitLines splits command output on newlines the fish way: the final
// newline is dropped, empty output is an empty list.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	// one trailing newline ends the last line; "\n" alone is one empty line
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// cmdSubst runs statements in a subshell and returns their output.
func (sh *Shell) cmdSubst(stmts []*syntax.Stmt, io *IO) (string, error) {
	sh.substCount++
	sub := sh.clone()
	sub.inSubst++
	buf := &lockedBuffer{}
	subIO := &IO{In: io.In, Out: &Stream{W: buf}, Err: io.Err}
	if c := inputOnly(stmts); c != nil {
		sub.status = sub.readRedirected(c, subIO, buf)
	} else {
		sub.runStmts(stmts, subIO)
	}
	sub.finishSubshell(subIO)
	sh.status = sub.status
	if sub.ctrl == ctrlExit {
		sh.status = sub.exitStatus
	}
	if sh.interrupted() {
		return "", errInterrupted
	}
	return buf.String(), nil
}

// inputOnly returns the command if stmts is nothing but an input
// redirection: $(< file) is the contents of file, as in bash, ksh and
// zsh, where running it as a command would print nothing.
func inputOnly(stmts []*syntax.Stmt) *syntax.CallExpr {
	if len(stmts) != 1 || stmts[0].Background || stmts[0].Cond != syntax.CondNone {
		return nil
	}
	c, ok := stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(c.Args) != 0 || len(c.Assigns) != 0 || len(c.Redirs) != 1 {
		return nil
	}
	if r := c.Redirs[0]; r.Op != syntax.RedirIn || r.Fd > 0 {
		return nil
	}
	return c
}

// readRedirected copies the input that c redirects into out.
func (sh *Shell) readRedirected(c *syntax.CallExpr, sio *IO, out *lockedBuffer) int {
	rio, cleanup, err := sh.redirect(c.Redirs, sio)
	defer closeAll(sh.takeProcFds())
	defer cleanup()
	if err != nil {
		sh.errorMsg(sio.Err, "%v", err)
		return 1
	}
	b := make([]byte, 32*1024)
	for {
		n, err := rio.In.Read(b)
		out.Write(b[:n])
		if err == io.EOF {
			return 0
		}
		if err != nil {
			sh.errorMsg(sio.Err, "%s", errText(err))
			return 1
		}
	}
}

var errInterrupted = errorf("interrupted")
