package shell

import (
	"fmt"
	"os/user"
	"regexp"
	"strconv"
	"strings"

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
			switch s[i] {
			case '*', '?', '[', ']', '\\':
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
	for _, parts := range braceExpand(w.Parts) {
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
			b.WriteString(braceText(p))
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
			b.WriteString(escapeGlob(braceText(p)))
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

func escapeGlob(s string) string {
	if !strings.ContainsAny(s, "*?[]\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*', '?', '[', ']', '\\':
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
			// "$@" with no arguments produces no word at all.
			if len(p.Parts) == 1 {
				if pe, ok := p.Parts[0].(*syntax.ParamExp); ok && isSplitParam(pe) {
					vals, _, err := sh.expandPart(pe, e.io, true)
					if err != nil {
						return err
					}
					e.split(vals)
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

func isSplitParam(pe *syntax.ParamExp) bool {
	if pe.Op != syntax.OpNone || pe.Length {
		return false
	}
	if pe.Name == "@" {
		return true
	}
	return pe.Index != nil && pe.Index.All && !pe.Index.Star
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
		return []string{out}, kindIFS, nil
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
		return []string{braceText(p)}, kindScalar, nil
	}
	return nil, kindScalar, fmt.Errorf("unsupported expansion")
}

// splitLines splits command output on newlines the fish way: the final
// newline is dropped, empty output is an empty list.
func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// cmdSubst runs statements in a subshell and returns their output.
func (sh *Shell) cmdSubst(stmts []*syntax.Stmt, io *IO) (string, error) {
	sh.substCount++
	sub := sh.clone()
	sub.inSubst++
	buf := &lockedBuffer{}
	subIO := &IO{In: io.In, Out: &Stream{W: buf}, Err: io.Err}
	sub.runStmts(stmts, subIO)
	sh.status = sub.status
	if sub.ctrl == ctrlExit {
		sh.status = sub.exitStatus
	}
	if sh.interrupted() {
		return "", errInterrupted
	}
	return buf.String(), nil
}

var errInterrupted = errorf("interrupted")
