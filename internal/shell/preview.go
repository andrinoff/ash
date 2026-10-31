package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/andrinoff/ash/internal/syntax"
)

// The expansion preview: the line editor shows, under the command line,
// what the word at the cursor expands to. It runs while the user types, so
// it must be cheap, it must never run code or change the shell, and
// everything that could take long is capped.
const (
	previewMaxMatches = 200                  // glob matches to collect
	previewGlobTime   = 5 * time.Millisecond // time allowed for globbing
	previewMaxBraces  = 50                   // brace expansion alternatives
	previewMaxFields  = 1000                 // fields, e.g. from $a$b with long lists
	previewMaxText    = 300                  // bytes of preview text
	previewMaxWork    = 2_000_000            // estimated steps for ${x//pat/rep} and friends
	previewMaxDepth   = 64                   // quotes and brackets the word scanner follows
)

// PreviewWord describes what the word under (or just before) the cursor
// expands to: "3 matches: a.go b.go c.go", "2 values: a b", a single value,
// "(unset)". cursor is a byte offset into buf. It returns "" for words
// with nothing to expand and for words that would run code (command and
// process substitution).
//
// PreviewWord only reads the shell, so it may run in the background while
// nothing else changes the shell. The line editor calls it on a fork (see
// Fork) taken on the main goroutine.
func (sh *Shell) PreviewWord(buf string, cursor int) (out string) {
	defer func() {
		// a bug here must not take the interactive shell down
		if recover() != nil {
			out = ""
		}
	}()
	return sh.previewWord(buf, cursor)
}

func (sh *Shell) previewWord(buf string, cursor int) string {
	if cursor < 0 || cursor > len(buf) {
		return ""
	}
	sc := &pvScanner{s: buf, cursor: cursor}
	sp, ok := sc.list(0, len(buf))
	if !ok || sp.ctx == pvSkip {
		return ""
	}
	text := buf[sp.start:sp.end]
	if sp.trim && sp.end == len(buf) && strings.HasSuffix(text, "\\") {
		text = text[:len(text)-1]
	}
	if text == "!!" || text == "!$" {
		return sh.previewHistory(text)
	}
	text += sp.closers
	if !strings.ContainsAny(text, "$*?[{~") {
		return "" // nothing to expand: don't bother parsing
	}
	return sh.previewText(text, sp.ctx)
}

// previewHistory previews !! and !$, which the editor expands when you
// press space or enter.
func (sh *Shell) previewHistory(tok string) string {
	if sh.History == nil {
		return ""
	}
	recent := sh.History.Recent(1)
	if len(recent) == 0 {
		return ""
	}
	if tok == "!!" {
		return previewOne(recent[0])
	}
	words := CommandTokens(recent[0])
	if len(words) == 0 {
		return ""
	}
	return previewOne(Quote(words[len(words)-1]))
}

// previewText parses one word and previews its expansion.
func (sh *Shell) previewText(text string, ctx pvCtx) string {
	var w *syntax.Word
	if ctx == pvAssign {
		f, err := parse(text, "")
		if err != nil || len(f.Stmts) != 1 {
			return ""
		}
		call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
		if !ok || len(call.Assigns) != 1 || len(call.Args) != 0 || len(call.Redirs) != 0 {
			return ""
		}
		a := call.Assigns[0]
		if a.IsArray || a.Value == nil {
			return ""
		}
		w = a.Value
	} else {
		f, err := parse(": "+text, "")
		if err != nil || len(f.Stmts) != 1 {
			return ""
		}
		call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) != 2 || len(call.Assigns) != 0 || len(call.Redirs) != 0 {
			return ""
		}
		w = call.Args[1]
	}
	arg := ctx == pvArg
	glob := arg && !sh.opts.noglob
	expands, runs := pvInspect(w.Parts, arg, glob, false)
	if runs || !expands {
		return ""
	}
	pv := &previewer{sh: sh, io: &IO{In: &Stream{}, Out: &Stream{}, Err: &Stream{}}, ok: true}
	pv.clean(w.Parts)
	if !pv.ok {
		return ""
	}
	if pe := loneParam(w); pe != nil && !pe.Indirect && !pe.Length && !handlesUnset(pe.Op) {
		if _, set := sh.getVar(pe.Name); !set {
			return "(unset)"
		}
	}
	if ctx != pvArg {
		s, err := sh.expandString(w, pv.io)
		if err != nil {
			return ""
		}
		return previewOne(s)
	}
	return pv.fields(w, glob)
}

// pvInspect reports whether parts have anything to expand and whether
// expanding them would run code. arg says braces expand (the word is an
// argument), glob that globs do.
func pvInspect(parts []syntax.WordPart, arg, glob, quoted bool) (expands, runs bool) {
	add := func(e, r bool) {
		expands = expands || e
		runs = runs || r
	}
	word := func(w *syntax.Word) {
		if w != nil {
			_, r := pvInspect(w.Parts, false, false, false)
			add(false, r)
		}
	}
	for _, p := range parts {
		switch p := p.(type) {
		case *syntax.Lit:
			if glob && !quoted && isGlobPattern(p.Value) {
				expands = true
			}
		case *syntax.DblQuoted:
			add(pvInspect(p.Parts, arg, glob, true))
		case *syntax.ParamExp:
			expands = true
			word(p.Arg)
			word(p.Arg2)
			if p.Index != nil {
				word(p.Index.Expr)
				for _, w := range p.Index.Words {
					word(w)
				}
			}
		case *syntax.ArithExp:
			expands = true
			word(p.Expr)
		case *syntax.BraceExp:
			if arg {
				expands = true
			}
			for _, a := range p.Alts {
				_, r := pvInspect(a, arg, glob, quoted)
				add(false, r)
			}
		case *syntax.Tilde:
			expands = true
		case *syntax.CmdSubst, *syntax.ProcSubst:
			runs = true
		}
	}
	return expands, runs
}

// loneParam returns the parameter expansion a word consists of ($x, ${x},
// "$x"), or nil.
func loneParam(w *syntax.Word) *syntax.ParamExp {
	parts := w.Parts
	if len(parts) == 1 {
		if dq, ok := parts[0].(*syntax.DblQuoted); ok {
			parts = dq.Parts
		}
	}
	if len(parts) != 1 {
		return nil
	}
	pe, _ := parts[0].(*syntax.ParamExp)
	return pe
}

// handlesUnset reports whether op gives an unset variable a value of its
// own (defaults, alternatives, errors) or doesn't read the value at all.
func handlesUnset(op syntax.ParamOp) bool {
	switch op {
	case syntax.OpDefault, syntax.OpDefaultUnset, syntax.OpAssign, syntax.OpAssignUnset,
		syntax.OpError, syntax.OpErrorUnset, syntax.OpAlt, syntax.OpAltUnset,
		syntax.OpPrefixNames, syntax.OpIndices:
		return true
	}
	return false
}

// previewer prepares a parsed word for a preview and expands it.
type previewer struct {
	sh *Shell
	io *IO
	ok bool // false once the word turned out unsafe or too costly
}

// clean makes parts safe to expand: ${x:=y} becomes ${x:-y}, arithmetic is
// evaluated without assigning, ~user goes through a cache, and anything
// that would run code, change the shell or take too long is refused.
func (pv *previewer) clean(parts []syntax.WordPart) {
	for i := 0; i < len(parts) && pv.ok; i++ {
		switch p := parts[i].(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			pv.ok = false
		case *syntax.DblQuoted:
			pv.clean(p.Parts)
		case *syntax.BraceExp:
			for _, a := range p.Alts {
				pv.clean(a)
			}
		case *syntax.Tilde:
			parts[i] = &syntax.Quoted{Value: pv.tilde(p.User)}
		case *syntax.ArithExp:
			if v, ok := pv.arithWord(p.Expr); ok {
				parts[i] = &syntax.Quoted{Value: v}
			} else {
				pv.ok = false
			}
		case *syntax.ParamExp:
			pv.param(p)
		}
	}
}

func (pv *previewer) param(pe *syntax.ParamExp) {
	sh := pv.sh
	name := pe.Name
	if pe.Indirect {
		ref := sh.Get(name)
		name = ref
		if i := strings.IndexByte(ref, '['); i > 0 && strings.HasSuffix(ref, "]") {
			// ${!ref} with ref="arr[i]": the subscript is evaluated for real,
			// so it must not assign
			sub := ref[i+1 : len(ref)-1]
			name = ref[:i]
			if sub != "@" && sub != "*" {
				if strings.Contains(sub, "=") || strings.Contains(sub, "++") || strings.Contains(sub, "--") {
					pv.ok = false
					return
				}
				if _, ok := pv.arith(sub); !ok {
					pv.ok = false
					return
				}
			}
		}
	}
	if name == "RANDOM" {
		// reading it changes the random state, and the number shown would
		// not be the one the command gets
		pv.ok = false
		return
	}
	switch pe.Op {
	case syntax.OpAssign:
		pe.Op = syntax.OpDefault
	case syntax.OpAssignUnset:
		pe.Op = syntax.OpDefaultUnset
	}
	if idx := pe.Index; idx != nil {
		if idx.Fish {
			for _, w := range idx.Words {
				for _, p := range w.Parts {
					if _, ok := p.(*syntax.BraceExp); ok {
						pv.ok = false
						return
					}
				}
				pv.clean(w.Parts)
			}
		} else if !idx.All && idx.Expr != nil {
			idx.Expr = pv.arithLit(idx.Expr)
		}
	}
	if pe.Op == syntax.OpSubstr {
		if pe.Arg != nil {
			pe.Arg = pv.arithLit(pe.Arg)
		}
		if pe.HasArg2 && pe.Arg2 != nil {
			pe.Arg2 = pv.arithLit(pe.Arg2)
		}
		return
	}
	if pe.Arg != nil {
		pv.clean(pe.Arg.Parts)
	}
	if pe.Arg2 != nil {
		pv.clean(pe.Arg2.Parts)
	}
	if !pv.ok {
		return
	}
	switch pe.Op {
	case syntax.OpTrimPrefix, syntax.OpTrimPrefixLong, syntax.OpTrimSuffix, syntax.OpTrimSuffixLong,
		syntax.OpReplace, syntax.OpReplaceAll, syntax.OpReplacePrefix, syntax.OpReplaceSuffix:
		if pv.patternWork(pe, name) > previewMaxWork {
			pv.ok = false
		}
	}
}

// patternWork estimates the steps ${x#pat}, ${x/pat/rep} and friends take:
// they try the pattern at every position (and ${x/...} against every
// substring), so long values with wildcard patterns get expensive fast.
func (pv *previewer) patternWork(pe *syntax.ParamExp, name string) int64 {
	vals, _ := pv.sh.getVar(name)
	var n int64
	for _, v := range vals {
		n += int64(len(v)) + 1
	}
	if pe.Arg == nil {
		return n
	}
	pat, err := pv.sh.expandPattern(pe.Arg, pv.io, false)
	if err != nil {
		return previewMaxWork + 1
	}
	wild := hasMeta(pat)
	if n > 1<<20 {
		return previewMaxWork + 1 // and don't overflow below
	}
	switch pe.Op {
	case syntax.OpReplace, syntax.OpReplaceAll:
		if wild {
			return n * n * n
		}
		return n * n
	}
	if wild {
		return n * n
	}
	return n
}

// arithLit evaluates an arithmetic word (a subscript or offset) and
// returns it as a literal number.
func (pv *previewer) arithLit(w *syntax.Word) *syntax.Word {
	v, ok := pv.arithWord(w)
	if !ok {
		pv.ok = false
		return w
	}
	return &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: v}}}
}

func (pv *previewer) arithWord(w *syntax.Word) (string, bool) {
	if w == nil {
		return "0", true
	}
	pv.clean(w.Parts)
	if !pv.ok {
		return "", false
	}
	s, err := pv.sh.expandString(w, pv.io)
	if err != nil {
		return "", false
	}
	n, ok := pv.arith(s)
	if !ok {
		return "", false
	}
	return strconv.FormatInt(n, 10), true
}

// arith evaluates an arithmetic expression without changing anything.
// Assignments (x=1, x++) show their value but don't assign. It refuses
// "**" (a large exponent loops for a long time) and variables that hold
// expressions rather than numbers, which would be evaluated in turn.
func (pv *previewer) arith(expr string) (int64, bool) {
	if strings.Contains(expr, "**") {
		return 0, false
	}
	for i := 0; i < len(expr); {
		c := expr[i]
		switch {
		case c >= '0' && c <= '9':
			for i < len(expr) && (isNameByte(expr[i]) || expr[i] == '#') {
				i++
			}
		case isNameStartByte(c):
			j := i
			for j < len(expr) && isNameByte(expr[j]) {
				j++
			}
			if !pv.numericVar(expr[i:j]) {
				return 0, false
			}
			i = j
		default:
			i++
		}
	}
	if strings.TrimSpace(expr) == "" {
		return 0, true
	}
	p := &arithParser{sh: pv.sh, s: expr}
	if strings.Contains(expr, "=") || strings.Contains(expr, "++") || strings.Contains(expr, "--") {
		p.noEval = 1
	}
	v, err := p.parse()
	return v, err == nil
}

// numericVar reports whether every element of a variable is empty or a
// number, so arithmetic can read it without evaluating an expression.
func (pv *previewer) numericVar(name string) bool {
	if name == "RANDOM" {
		return false
	}
	vals, _ := pv.sh.getVar(name)
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := parseArithNumber(v); !ok {
			return false
		}
	}
	return true
}

// tildeCache remembers ~user lookups, which can be slow (they may ask a
// directory service).
var tildeCache struct {
	sync.Mutex
	m map[string]string
}

func (pv *previewer) tilde(user string) string {
	switch user {
	case "", "+", "-":
		return pv.sh.expandTilde(user)
	}
	tildeCache.Lock()
	home, ok := tildeCache.m[user]
	tildeCache.Unlock()
	if ok {
		return home
	}
	home = pv.sh.expandTilde(user)
	tildeCache.Lock()
	if tildeCache.m == nil || len(tildeCache.m) >= 64 {
		tildeCache.m = map[string]string{}
	}
	tildeCache.m[user] = home
	tildeCache.Unlock()
	return home
}

// fields expands an argument word: braces, then variables, then globs,
// each capped.
func (pv *previewer) fields(w *syntax.Word, glob bool) string {
	sh := pv.sh
	alts, more := braceExpandN(w.Parts, previewMaxBraces)
	budget := globBudget{deadline: time.Now().Add(previewGlobTime), max: previewMaxMatches}
	// vals is what the command gets, matches only what globs matched
	var vals, matches []string
	plain := 0
expand:
	for _, parts := range alts {
		if pv.estimate(parts) > previewMaxFields {
			return ""
		}
		e := &expander{sh: sh, io: pv.io, fields: []*field{{open: true}}}
		if err := e.parts(parts, false); err != nil {
			return ""
		}
		if !e.content {
			continue
		}
		for _, f := range e.fields {
			if len(vals) >= previewMaxFields || budget.stopped {
				more = true
				break expand
			}
			if f.glob && glob {
				ms := sh.previewGlob(string(f.pat), &budget)
				if len(ms) > 0 {
					matches = append(matches, ms...)
					vals = append(vals, ms...)
					continue
				}
				if budget.stopped {
					more = true
					break expand
				}
				// an unmatched glob stays as it is
			} else {
				plain++
			}
			vals = append(vals, string(f.text))
		}
	}
	if plain == 0 && len(vals) > 0 {
		// every field was a glob
		switch n := len(matches); {
		case n == 0 && more:
			return "" // gave up before finding anything
		case n == 0:
			return "no matches"
		case n > previewMaxMatches:
			return previewList(previewMaxMatches, true, "match", "matches", matches)
		default:
			return previewList(n, more, "match", "matches", matches)
		}
	}
	switch {
	case len(vals) == 0:
		return "(empty)"
	case len(vals) == 1 && !more:
		return previewOne(vals[0])
	}
	return previewList(len(vals), more, "value", "values", vals)
}

// estimate guesses how many fields parts expand to: unquoted lists
// multiply ($a$b with two long lists is their cartesian product).
func (pv *previewer) estimate(parts []syntax.WordPart) int {
	n := 1
	for _, p := range parts {
		pe, ok := p.(*syntax.ParamExp)
		if !ok {
			continue
		}
		vals, _ := pv.sh.getVar(pe.Name)
		k := len(vals)
		if pe.Index != nil && pe.Index.Fish && len(pe.Index.Words) > 1 {
			k *= len(pe.Index.Words)
		}
		if k > 1 {
			n *= k
			if n > previewMaxFields {
				return n
			}
		}
	}
	return n
}

// previewOne formats a single value.
func previewOne(s string) string {
	if s == "" {
		return "(empty)"
	}
	return previewClip(strings.ReplaceAll(s, "\n", "⏎"))
}

func previewClip(s string) string {
	if len(s) <= previewMaxText {
		return s
	}
	cut := previewMaxText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// previewList formats "12 matches: a b c …". plus means n is a lower
// bound.
func previewList(n int, plus bool, one, many string, vals []string) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(n))
	if plus {
		b.WriteByte('+')
	}
	b.WriteByte(' ')
	if n == 1 && !plus {
		b.WriteString(one)
	} else {
		b.WriteString(many)
	}
	b.WriteByte(':')
	for i, v := range vals {
		q := strings.ReplaceAll(Quote(v), "\n", "⏎")
		if i == 0 {
			q = previewClip(q)
		} else if b.Len()+1+len(q) > previewMaxText {
			b.WriteString(" …")
			return b.String()
		}
		b.WriteByte(' ')
		b.WriteString(q)
	}
	if plus || len(vals) < n {
		b.WriteString(" …")
	}
	return b.String()
}

// braceExpandN is braceExpand that stops after limit alternatives; more
// reports whether some were left out.
func braceExpandN(parts []syntax.WordPart, limit int) (out [][]syntax.WordPart, more bool) {
	idx := -1
	for i, p := range parts {
		if _, ok := p.(*syntax.BraceExp); ok {
			idx = i
			break
		}
	}
	if idx < 0 {
		return [][]syntax.WordPart{parts}, false
	}
	be := parts[idx].(*syntax.BraceExp)
	prefix := parts[:idx]
	suffixes, more := braceExpandN(parts[idx+1:], limit)
	var alts [][]syntax.WordPart
	if be.Seq != nil {
		seq, m := expandSeqN(be.Seq, limit)
		more = more || m
		for _, s := range seq {
			alts = append(alts, []syntax.WordPart{&syntax.Lit{Value: s}})
		}
	} else {
		for _, a := range be.Alts {
			if len(alts) >= limit {
				more = true
				break
			}
			sub, m := braceExpandN(a, limit-len(alts))
			alts = append(alts, sub...)
			more = more || m
		}
	}
	for _, a := range alts {
		for _, s := range suffixes {
			if len(out) == limit {
				return out, true
			}
			np := make([]syntax.WordPart, 0, len(prefix)+len(a)+len(s))
			np = append(np, prefix...)
			np = append(np, a...)
			np = append(np, s...)
			out = append(out, np)
		}
	}
	return out, more
}

// expandSeqN is expandSeq that stops after limit items.
func expandSeqN(seq *syntax.BraceSeq, limit int) ([]string, bool) {
	step := 1
	if seq.Step != "" {
		step, _ = strconv.Atoi(seq.Step)
		if step < 0 {
			step = -step
		}
		if step == 0 {
			step = 1
		}
	}
	a, errA := strconv.Atoi(seq.Start)
	b, errB := strconv.Atoi(seq.End)
	var out []string
	if errA == nil && errB == nil {
		width := 0
		if (len(seq.Start) > 1 && strings.TrimLeft(seq.Start, "-")[0] == '0') || (len(seq.End) > 1 && strings.TrimLeft(seq.End, "-")[0] == '0') {
			width = max(len(seq.Start), len(seq.End))
		}
		format := func(n int) string {
			if width > 0 {
				return fmt.Sprintf("%0*d", width, n)
			}
			return strconv.Itoa(n)
		}
		if a <= b {
			for i := a; i <= b; {
				if len(out) == limit {
					return out, true
				}
				out = append(out, format(i))
				next := i + step
				if next < i {
					break // overflow
				}
				i = next
			}
		} else {
			for i := a; i >= b; {
				if len(out) == limit {
					return out, true
				}
				out = append(out, format(i))
				next := i - step
				if next > i {
					break
				}
				i = next
			}
		}
		return out, false
	}
	ca, cb := int(seq.Start[0]), int(seq.End[0])
	if ca <= cb {
		for c := ca; c <= cb; c += step {
			if len(out) == limit {
				return out, true
			}
			out = append(out, string(rune(c)))
		}
	} else {
		for c := ca; c >= cb; c -= step {
			if len(out) == limit {
				return out, true
			}
			out = append(out, string(rune(c)))
		}
	}
	return out, false
}

// globBudget caps the glob walks of one preview.
type globBudget struct {
	deadline time.Time
	max      int // matches wanted; one more is collected to know there are more
	found    int
	ticks    int
	stopped  bool
}

// done reports whether the walk should stop. It reads the clock every 32
// calls, so it can be called for every directory entry.
func (b *globBudget) done() bool {
	if !b.stopped {
		b.ticks++
		if b.found > b.max || b.ticks%32 == 0 && time.Now().After(b.deadline) {
			b.stopped = true
		}
	}
	return b.stopped
}

// late is done, reading the clock now.
func (b *globBudget) late() bool {
	if !b.stopped && (b.found > b.max || time.Now().After(b.deadline)) {
		b.stopped = true
	}
	return b.stopped
}

// previewGlob is glob with a budget: it stops when the budget runs out,
// reads big directories in chunks so it can stop in the middle, and never
// opens anything but a directory (opening a FIFO would block).
func (sh *Shell) previewGlob(pat string, b *globBudget) []string {
	if pat == "" || b.late() {
		return nil
	}
	abs := strings.HasPrefix(pat, "/")
	segs := strings.Split(strings.TrimPrefix(pat, "/"), "/")
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
	add := func(r string) {
		results = append(results, r)
		b.found++
	}
	var walk func(dir, rel string, i int)
	walk = func(dir, rel string, i int) {
		if b.done() {
			return
		}
		if i == len(segs) {
			add(rel)
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
			if i == len(segs)-1 {
				if rel != "" {
					add(rel + "/")
				}
				return
			}
			walk(dir, rel, i+1)
			return
		}
		if seg == "**" {
			// like glob: a final ** matches everything below dir,
			// otherwise it stands for zero or more directories
			last := i == len(segs)-1
			if !last {
				walk(dir, rel, i+1)
			}
			var rec func(d, r string, depth int)
			rec = func(d, r string, depth int) {
				if depth > 64 || b.done() {
					return
				}
				for _, e := range readDirBudget(d, b) {
					if b.done() {
						return
					}
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
						add(nr)
					}
					// like glob: symlinks to directories are not
					// descended into
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
			d, r := join(unescapePattern(seg))
			if i == len(segs)-1 {
				if _, err := os.Lstat(d); err == nil {
					add(r)
				}
				return
			}
			walk(d, r, i+1)
			return
		}
		dotOK := strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "\\.")
		for _, e := range readDirBudget(dir, b) {
			if b.done() {
				return
			}
			name := e.Name()
			if name[0] == '.' && !dotOK {
				continue
			}
			if !matchPattern(seg, name) {
				continue
			}
			d, r := join(name)
			if i < len(segs)-1 && !e.IsDir() {
				// only a symlink can still lead to a directory
				if e.Type()&os.ModeSymlink == 0 {
					continue
				}
				if st, err := os.Stat(d); err != nil || !st.IsDir() {
					continue
				}
			}
			walk(d, r, i+1)
		}
	}
	walk(base, "", 0)
	if len(results) > 1 {
		seen := make(map[string]bool, len(results))
		out := results[:0]
		for _, r := range results {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
		results = out
		sort.Slice(results, func(x, y int) bool { return naturalLess(results[x], results[y]) })
	}
	return results
}

// readDirBudget lists a directory, sorted by name like os.ReadDir, a
// chunk at a time so a huge directory can't hold up the preview.
func readDirBudget(dir string, b *globBudget) []os.DirEntry {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	var all []os.DirEntry
	for !b.late() {
		ents, err := f.ReadDir(256)
		all = append(all, ents...)
		if err != nil {
			break
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name() < all[j].Name() })
	return all
}

// pvCtx is how the shell expands the word at the cursor.
type pvCtx uint8

const (
	pvArg    pvCtx = iota // a command argument: braces, fields, globs
	pvAssign              // NAME=value before a command: one string
	pvString              // [[ ... ]], case and switch words: one string, no globs
	pvSkip                // a heredoc delimiter: not expanded
)

// pvSpan is the word at the cursor.
type pvSpan struct {
	start, end int    // the word is buf[start:end]
	closers    string // closes quotes and brackets still open at the end of buf
	trim       bool   // a lone backslash ends buf
	ctx        pvCtx
}

// pvGroup is a command list inside a word: $(...), `...`, (...), <(...).
// s[start:end] is its text.
type pvGroup struct{ start, end int }

// pvScanner finds the shell word at the cursor. It is tolerant: it never
// fails, unterminated quotes and brackets run to the end of the input, and
// it goes into command substitutions to find the word inside them.
type pvScanner struct {
	s      string
	cursor int
	depth  int
	trim   bool
}

// keepsCommand lists words after which a new command starts.
var keepsCommand = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true,
	"until": true, "!": true, "{": true, "and": true, "or": true, "not": true,
	"time": true, "begin": true,
}

// list scans commands in s[i:to] and returns the word at the cursor.
func (sc *pvScanner) list(i, to int) (pvSpan, bool) {
	s := sc.s
	atCmd := true // the next word starts a command (or assigns before one)
	cmd := ""     // the current command's name
	redir := false
	redirCtx := pvArg
	for i < to {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			i++
			continue
		case c == '\n' || c == ';' || c == '&' || c == '|' || c == ')':
			atCmd, cmd, redir = true, "", false
			i++
			continue
		case c == '#':
			j := strings.IndexByte(s[i:to], '\n')
			if j < 0 {
				j = to
			} else {
				j += i
			}
			if sc.cursor > i && sc.cursor <= j {
				return pvSpan{}, false // in a comment
			}
			i = j
			continue
		case (c == '<' || c == '>') && !(i+1 < to && s[i+1] == '('):
			j := i + 1
			for j < to && strings.IndexByte("<>&|-", s[j]) >= 0 {
				j++
			}
			op := s[i:j]
			redir, redirCtx = true, pvArg
			if strings.HasPrefix(op, "<<") && !strings.HasPrefix(op, "<<<") {
				redirCtx = pvSkip
			}
			i = j
			continue
		}
		end, closers, groups := sc.word(i, to)
		if end <= i {
			end = i + 1
		}
		w := s[i:end]
		if sc.cursor >= i && sc.cursor <= end {
			for _, g := range groups {
				if sc.cursor >= g.start && sc.cursor <= g.end && sc.depth < previewMaxDepth {
					sc.depth++
					return sc.list(g.start, g.end)
				}
			}
			ctx := pvArg
			switch {
			case redir:
				ctx = redirCtx
			case atCmd && isAssignWord(w):
				ctx = pvAssign
			case !atCmd && (cmd == "[[" || cmd == "case" || cmd == "switch"):
				ctx = pvString
			}
			return pvSpan{start: i, end: end, closers: closers, trim: sc.trim, ctx: ctx}, true
		}
		switch {
		case redir:
			redir = false
		case atCmd && isAssignWord(w):
		case atCmd:
			cmd = w
			atCmd = keepsCommand[w]
		}
		i = end
	}
	return pvSpan{}, false
}

// word scans the word starting at i. It returns where the word ends, what
// closes the quotes and brackets left open (when it runs to the end), and
// the command lists inside it.
func (sc *pvScanner) word(i, to int) (int, string, []pvGroup) {
	s := sc.s
	var groups []pvGroup
	j := i
	for j < to {
		switch s[j] {
		case ' ', '\t', '\r', '\n', ';', '&', '|', ')':
			return j, "", groups
		case '<', '>':
			if j == i && j+1 < to && s[j+1] == '(' {
				k, cl := sc.parens(j+2, to)
				groups = append(groups, pvGroup{j + 2, groupEnd(k, cl)})
				if cl != "" {
					return to, cl, groups
				}
				j = k
				continue
			}
			return j, "", groups
		case '\\':
			if j+1 >= to {
				sc.trim = j+1 >= len(s)
				return to, "", groups
			}
			j += 2
		case '\'':
			k := strings.IndexByte(s[j+1:to], '\'')
			if k < 0 {
				return to, "'", groups
			}
			j += k + 2
		case '"':
			k, cl := sc.dquote(j+1, to, &groups)
			if cl != "" {
				return to, cl, groups
			}
			j = k
		case '`':
			k, cl := sc.backquote(j+1, to)
			groups = append(groups, pvGroup{j + 1, groupEnd(k, cl)})
			if cl != "" {
				return to, cl, groups
			}
			j = k
		case '$':
			k, cl := sc.dollar(j, to, false, &groups)
			if cl != "" {
				return to, cl, groups
			}
			j = k
		case '(':
			k, cl := sc.parens(j+1, to)
			groups = append(groups, pvGroup{j + 1, groupEnd(k, cl)})
			if cl != "" {
				return to, cl, groups
			}
			j = k
		default:
			j++
		}
	}
	return j, "", groups
}

// groupEnd is where a group's text ends: before its closing delimiter at
// k-1, or at the end of the input when it is unterminated.
func groupEnd(k int, closers string) int {
	if closers != "" {
		return k
	}
	return k - 1
}

// parens scans to the ')' matching a '(' just before j. It returns the
// index after it, or the end of the input and what would close it.
func (sc *pvScanner) parens(j, to int) (int, string) {
	if sc.depth >= previewMaxDepth {
		return to, ""
	}
	sc.depth++
	defer func() { sc.depth-- }()
	s := sc.s
	depth := 1
	open := func(cl string) string { return cl + strings.Repeat(")", depth) }
	for j < to {
		switch s[j] {
		case '(':
			depth++
			j++
		case ')':
			depth--
			j++
			if depth == 0 {
				return j, ""
			}
		case '\\':
			if j+1 >= to {
				sc.trim = j+1 >= len(s)
				return to, open("")
			}
			j += 2
		case '\'':
			k := strings.IndexByte(s[j+1:to], '\'')
			if k < 0 {
				return to, open("'")
			}
			j += k + 2
		case '"':
			k, cl := sc.dquote(j+1, to, nil)
			if cl != "" {
				return to, open(cl)
			}
			j = k
		case '`':
			k, cl := sc.backquote(j+1, to)
			if cl != "" {
				return to, open(cl)
			}
			j = k
		case '$':
			k, cl := sc.dollar(j, to, false, nil)
			if cl != "" {
				return to, open(cl)
			}
			j = k
		default:
			j++
		}
	}
	return to, open("")
}

// dquote scans the inside of "..." from j.
func (sc *pvScanner) dquote(j, to int, groups *[]pvGroup) (int, string) {
	s := sc.s
	for j < to {
		switch s[j] {
		case '"':
			return j + 1, ""
		case '\\':
			if j+1 >= to {
				sc.trim = j+1 >= len(s)
				return to, "\""
			}
			j += 2
		case '`':
			k, cl := sc.backquote(j+1, to)
			if groups != nil {
				*groups = append(*groups, pvGroup{j + 1, groupEnd(k, cl)})
			}
			if cl != "" {
				return to, cl + "\""
			}
			j = k
		case '$':
			k, cl := sc.dollar(j, to, true, groups)
			if cl != "" {
				return to, cl + "\""
			}
			j = k
		default:
			j++
		}
	}
	return to, "\""
}

// backquote scans the inside of `...` from j.
func (sc *pvScanner) backquote(j, to int) (int, string) {
	s := sc.s
	for j < to {
		switch s[j] {
		case '`':
			return j + 1, ""
		case '\\':
			if j+1 >= to {
				sc.trim = j+1 >= len(s)
				return to, "`"
			}
			j += 2
		default:
			j++
		}
	}
	return to, "`"
}

// dollar scans an expansion starting with the '$' at j.
func (sc *pvScanner) dollar(j, to int, inDQ bool, groups *[]pvGroup) (int, string) {
	s := sc.s
	if j+1 >= to {
		return to, ""
	}
	switch c := s[j+1]; {
	case c == '(':
		if j+2 < to && s[j+2] == '(' {
			// $(( arithmetic )): not a command list
			return sc.parens(j+2, to)
		}
		k, cl := sc.parens(j+2, to)
		if groups != nil {
			*groups = append(*groups, pvGroup{j + 2, groupEnd(k, cl)})
		}
		return k, cl
	case c == '{':
		return sc.braceParam(j+2, to, groups)
	case c == '\'' && !inDQ:
		k := j + 2
		for k < to {
			switch s[k] {
			case '\\':
				if k+1 >= to {
					sc.trim = k+1 >= len(s)
					return to, "'"
				}
				k += 2
				continue
			case '\'':
				return k + 1, ""
			}
			k++
		}
		return to, "'"
	case c == '"' && !inDQ:
		return sc.dquote(j+2, to, groups)
	case isNameStartByte(c):
		k := j + 2
		for k < to && isNameByte(s[k]) {
			k++
		}
		if k < to && s[k] == '[' {
			if m, cl, ok := sc.index(k+1, to); ok {
				return m, cl
			}
		}
		return k, ""
	case strings.IndexByte("?$!#@*-0123456789", c) >= 0:
		return j + 2, ""
	}
	return j + 1, ""
}

// index scans a fish index ($x[1 2..3]) from j, which may hold spaces. It
// reports false if a command separator comes first: then the bracket is
// just a character.
func (sc *pvScanner) index(j, to int) (int, string, bool) {
	if j >= to {
		return 0, "", false // "$x[" just typed: nothing to close yet
	}
	s := sc.s
	depth := 1
	for j < to {
		switch s[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return j + 1, "", true
			}
		case '\n', ';', '|', '&':
			return 0, "", false
		case '(':
			k, cl := sc.parens(j+1, to)
			if cl != "" {
				return to, cl + strings.Repeat("]", depth), true
			}
			j = k
			continue
		}
		j++
	}
	return to, strings.Repeat("]", depth), true
}

// braceParam scans the inside of ${...} from j.
func (sc *pvScanner) braceParam(j, to int, groups *[]pvGroup) (int, string) {
	if sc.depth >= previewMaxDepth {
		return to, ""
	}
	sc.depth++
	defer func() { sc.depth-- }()
	s := sc.s
	for j < to {
		switch s[j] {
		case '}':
			return j + 1, ""
		case '\\':
			if j+1 >= to {
				sc.trim = j+1 >= len(s)
				return to, "}"
			}
			j += 2
		case '\'':
			k := strings.IndexByte(s[j+1:to], '\'')
			if k < 0 {
				return to, "'}"
			}
			j += k + 2
		case '"':
			k, cl := sc.dquote(j+1, to, groups)
			if cl != "" {
				return to, cl + "}"
			}
			j = k
		case '`':
			k, cl := sc.backquote(j+1, to)
			if groups != nil {
				*groups = append(*groups, pvGroup{j + 1, groupEnd(k, cl)})
			}
			if cl != "" {
				return to, cl + "}"
			}
			j = k
		case '$':
			k, cl := sc.dollar(j, to, false, groups)
			if cl != "" {
				return to, cl + "}"
			}
			j = k
		default:
			j++
		}
	}
	return to, "}"
}
