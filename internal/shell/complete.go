package shell

import (
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/syntax"
)

// completions holds the completion specs defined with `complete` and the
// caches of the completion engine. It is shared by a shell and all of its
// subshells.
type completions struct {
	mu        sync.RWMutex
	entries   map[string]*compEntry // specs by command name (complete -c)
	patterns  []*compEntry          // -p specs and -c names with wildcards
	order     []*compEntry          // every entry, in creation order
	wraps     map[string][]string   // complete -w targets by command
	wrapOrder []string
	loaded    map[string]bool // commands whose completion file was looked up
	loading   []string        // completion files being sourced (innermost last)

	active atomic.Pointer[compRun] // the completion being computed, if any

	dirs   dirCache
	fish   fishLib
	walked sync.Map // *Function -> struct{}: bodies already scanned for autoloads
	users  struct {
		once sync.Once
		list []userHome
	}
}

func newCompletions() *completions {
	return &completions{
		entries: map[string]*compEntry{},
		wraps:   map[string][]string{},
		loaded:  map[string]bool{},
	}
}

// compRun is one completion request.
type compRun struct {
	c     *completions
	sh    *Shell // the shell completion code runs in
	line  *compLine
	io    *IO
	conds map[string]bool // condition results for the current command line
	seen  map[string]bool // command names checked for autoloading
	ed    *compEditor     // what `commandline` sees
	prev  *compRun        // enclosing completion (complete -C in a script)
	depth int

	closeQuote bool       // the closing quote after the cursor is replaced too
	restore    savedState // shell state to put back when done
}

const maxCompleteDepth = 8

var devNull struct {
	once sync.Once
	f    *os.File
}

func nullFile() *os.File {
	devNull.once.Do(func() {
		f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err == nil {
			devNull.f = f
		}
	})
	return devNull.f
}

// quietIO is the I/O completion code runs with: no input, and output and
// errors thrown away so nothing reaches the terminal.
func quietIO() *IO {
	f := nullFile()
	if f == nil {
		return &IO{In: &Stream{R: strings.NewReader("")}, Out: &Stream{}, Err: &Stream{}}
	}
	return &IO{In: &Stream{File: f}, Out: &Stream{File: f}, Err: &Stream{File: f}}
}

// Complete returns the completions for the command line buf with the
// cursor at byte offset cursor. Items replace buf[Start:End]; their Text
// is already escaped for the token's quoting context.
//
// Completion may run shell code (completion files, -n conditions, -a
// command substitutions), so call it from the goroutine that runs the
// shell, while the shell is idle (e.g. from the line editor).
func (sh *Shell) Complete(buf string, cursor int) editor.Completion {
	comp, _ := sh.completeLine(buf, cursor)
	return comp
}

// completeLine completes buf at cursor. It also returns the scanned line.
func (sh *Shell) completeLine(buf string, cursor int) (res editor.Completion, line *compLine) {
	line = scanCompLine(buf, cursor)
	res = editor.Completion{Start: line.tok.start, End: line.cursor}
	if line.skip {
		return res, line
	}
	r := sh.beginComplete(line)
	if r == nil {
		return res, line
	}
	defer r.finish()
	defer func() {
		// a bug in completion code must not take the shell down with it
		if p := recover(); p != nil {
			res.Items = nil
		}
	}()
	closeQuote := false
	if q := closer(line.quote); q != 0 && line.cursor < len(buf) && buf[line.cursor] == q {
		// "my fi|" with the closing quote after the cursor: replace it too
		closeQuote = true
		res.End++
	}
	col := newCollector()
	r.dispatch(col, closeQuote)
	res.Items = col.result()
	return res, line
}

// beginComplete sets up a completion run, or returns nil when completions
// nest too deeply.
func (sh *Shell) beginComplete(line *compLine) *compRun {
	c := sh.shared.complete
	prev := c.active.Load()
	r := &compRun{c: c, sh: sh, line: line, prev: prev, conds: map[string]bool{}, seen: map[string]bool{}}
	if prev != nil {
		if prev.depth >= maxCompleteDepth {
			return nil
		}
		r.depth = prev.depth + 1
		// Run nested completions (complete -C from a completion script)
		// in the same shell as the outer one, so functions and specs it
		// loads are visible to the main shell rather than to a subshell.
		r.sh = prev.sh
		r.seen = prev.seen
	}
	r.io = quietIO()
	var inner Editor
	if prev != nil {
		inner = prev.ed.inner
	} else {
		inner = sh.Editor
	}
	r.ed = &compEditor{inner: inner, buf: line.buf[:line.cursor], pos: line.cursor, tok: line.tok.start}
	s := r.sh
	saved := savedState{status: s.status, pipe: s.pipestatus, lastArg: s.lastArg, ctrl: s.ctrl, exit: s.exitStatus,
		stdin: s.Stdin, stdout: s.Stdout, stderr: s.Stderr, interactive: s.Interactive, jobControl: s.JobControl,
		scope: s.scope, callStack: s.callStack, srcStack: s.srcStack, aliasStack: s.aliasStack,
		depth: s.depth, inBlock: s.inBlock, noErrexit: s.noErrexit, curSrc: s.curSrc}
	if f := nullFile(); f != nil {
		// autoloaded files and anything else using the shell's own
		// streams must stay quiet too
		s.Stdin, s.Stdout, s.Stderr = f, f, f
	}
	// Like fish, completion code doesn't run interactively: programs it
	// starts must not take the terminal away from the line editor.
	s.Interactive, s.JobControl = false, false
	r.restore = saved
	c.active.Store(r)
	return r
}

type savedState struct {
	status      int
	pipe        []int
	lastArg     string
	ctrl        ctrlKind
	exit        int
	stdin       *os.File
	stdout      *os.File
	stderr      *os.File
	interactive bool
	jobControl  bool

	// balanced by the code that runs, unless it panics
	scope                     *scope
	callStack                 []frame
	srcStack, aliasStack      []string
	depth, inBlock, noErrexit int
	curSrc                    string
}

// finish ends a completion run and puts the shell back as it was.
func (r *compRun) finish() {
	s := r.sh
	st := &r.restore
	s.status, s.pipestatus, s.lastArg = st.status, st.pipe, st.lastArg
	s.ctrl, s.exitStatus = st.ctrl, st.exit
	s.Stdin, s.Stdout, s.Stderr = st.stdin, st.stdout, st.stderr
	s.Interactive, s.JobControl = st.interactive, st.jobControl
	s.scope, s.callStack, s.srcStack, s.aliasStack = st.scope, st.callStack, st.srcStack, st.aliasStack
	s.depth, s.inBlock, s.noErrexit, s.curSrc = st.depth, st.inBlock, st.noErrexit, st.curSrc
	r.c.active.Store(r.prev)
}

// dispatch picks what to complete.
func (r *compRun) dispatch(col *collector, closeQuote bool) {
	l := r.line
	r.closeQuote = closeQuote
	if l.varPos >= 0 {
		r.completeVars(col)
		return
	}
	if l.redirect {
		r.completeFileToken(col, filesAll, true)
		return
	}
	idx := l.commandIndex()
	if idx < 0 {
		r.completeCommands(col)
		return
	}
	r.completeArgs(col, idx)
}

// insertion describes how a candidate's value turns into the text that
// replaces the token.
type insertion struct {
	prefix     string // raw text kept in front of the completed part
	raw        string // raw text of the completed part, as typed
	needle     string // its value, which candidates are matched against
	quote      byte   // quote open at the cursor
	startQuote byte   // quote open where the completed part starts
	escape     bool   // raw ends with an unfinished backslash
	atStart    bool   // the completed part starts the word
	closeQuote bool   // the closing quote after the cursor is replaced too

	app struct { // appendPoint, computed once
		done    bool
		raw     string
		quote   byte
		reenter bool
	}
}

// tokenInsertion is the insertion for the whole token.
func (r *compRun) tokenInsertion() *insertion {
	l := r.line
	return &insertion{
		raw:        l.raw(),
		needle:     l.tok.value,
		quote:      l.quote,
		escape:     l.escape,
		atStart:    true,
		closeQuote: r.closeQuote,
	}
}

// closer returns the character that closes a quote.
func closer(q byte) byte {
	switch q {
	case '\'', '$':
		return '\''
	case '"':
		return '"'
	}
	return 0
}

// text returns the replacement for the token for a candidate value that
// matched with the given tier. final says the candidate is complete (not
// a directory or "--opt="), so an open quote gets closed.
func (in *insertion) text(value string, tier int, final bool) string {
	if tier == tierPrefix && strings.HasPrefix(value, in.needle) {
		raw, q, reenter := in.appendPoint()
		s := in.prefix + raw + escapeFor(value[len(in.needle):], q, in.atStart && raw == "")
		if q != 0 && (final || in.closeQuote || reenter) {
			s += string(closer(q))
		}
		return s
	}
	q := in.startQuote
	s := in.prefix + escapeFor(value, q, in.atStart)
	if q != 0 && (final || in.closeQuote) {
		s += string(closer(q))
	}
	return s
}

// appendPoint returns the typed text that the rest of a candidate is
// appended to, the quote open at its end, and whether that quote was
// closed by the user ("my f"<tab> goes back inside the quotes, and closes
// them again).
func (in *insertion) appendPoint() (string, byte, bool) {
	if in.app.done {
		return in.app.raw, in.app.quote, in.app.reenter
	}
	raw, q, reenter := in.raw, in.quote, false
	if in.escape && raw != "" {
		raw = raw[:len(raw)-1]
	}
	if q == 0 && !in.escape && raw != "" {
		if last := raw[len(raw)-1]; (last == '\'' || last == '"') && quoteAt(raw[:len(raw)-1]) == last {
			raw, q, reenter = raw[:len(raw)-1], last, true
		}
	}
	in.app.raw, in.app.quote, in.app.reenter, in.app.done = raw, q, reenter, true
	return raw, q, reenter
}

// quoteAt returns the quote open at the end of raw.
func quoteAt(raw string) byte {
	return scanCompLine(raw, len(raw)).quote
}

// collector gathers candidates, keeping only those of the best tier.
type collector struct {
	best  int
	items []compCand
	index map[string]struct{}
}

type compCand struct {
	key  string // sort key: the candidate's value
	item editor.Candidate
	keep bool // complete -k: keep the definition order
}

func newCollector() *collector {
	return &collector{best: tierNone - 1, index: map[string]struct{}{}}
}

// max is the worst tier still worth computing.
func (c *collector) max() int { return c.best }

func (c *collector) add(tier int, key string, item editor.Candidate, keep bool) {
	if tier >= tierNone || tier > c.best {
		return
	}
	if tier < c.best {
		c.best = tier
		c.items = c.items[:0]
		clear(c.index)
	}
	if _, dup := c.index[item.Text]; dup {
		return
	}
	c.index[item.Text] = struct{}{}
	if item.Display == item.Text {
		item.Display = ""
	}
	c.items = append(c.items, compCand{key: key, item: item, keep: keep})
}

// result returns the candidates: -k ones first in definition order, then
// the rest sorted naturally.
func (c *collector) result() []editor.Candidate {
	if len(c.items) == 0 {
		return nil
	}
	sort.SliceStable(c.items, func(i, j int) bool {
		a, b := &c.items[i], &c.items[j]
		if a.keep || b.keep {
			return a.keep && !b.keep
		}
		return compLess(a.key, b.key)
	})
	out := make([]editor.Candidate, len(c.items))
	for i := range c.items {
		out[i] = c.items[i].item
	}
	return out
}

// completeVars completes a variable name after $ or ${.
func (r *compRun) completeVars(col *collector) {
	l := r.line
	prefix := l.buf[l.tok.start:l.varPos]
	typed := l.buf[l.varPos:l.cursor]
	m := newMatcher(typed, true)
	sh := r.sh
	for _, name := range sh.VarNames() {
		tier := m.tier(name, col.max())
		if tier == tierNone {
			continue
		}
		text := prefix + name
		if l.varBrace {
			text += "}"
		}
		if q := closer(l.quote); q != 0 {
			text += string(q)
		}
		desc := ""
		if name != "history" { // (the whole history: too big to show)
			if vals, ok := sh.getVar(name); ok {
				desc = truncateDesc(strings.Join(vals[:min(len(vals), 16)], " "), 64)
			}
		}
		col.add(tier, name, editor.Candidate{Text: text, Display: "$" + name, Description: desc, Kind: "variable"}, false)
	}
}

// shellKeywords are offered in command position.
var shellKeywords = []string{
	"if", "then", "else", "elif", "fi", "for", "while", "until", "do", "done",
	"case", "esac", "switch", "function", "begin", "end", "and", "or", "not", "time",
}

// completeCommands completes a command name.
func (r *compRun) completeCommands(col *collector) {
	l := r.line
	v := l.tok.value
	raw := l.raw()
	if strings.Contains(v, "/") {
		// a path: executables, and directories to cd into or through
		r.completeFileToken(col, filesExec, false)
		return
	}
	if strings.HasPrefix(v, "~") && !l.tok.glob {
		r.completeUsers(col)
		return
	}
	if isAssignWord(raw) {
		// VAR=value before a command: complete the value
		r.completeFileToken(col, filesAll, true)
		return
	}
	if strings.HasPrefix(v, ".") {
		// implicit cd
		r.completeFileToken(col, filesDirs, false)
	}
	in := r.tokenInsertion()
	m := newMatcher(v, true)
	sh := r.sh
	add := func(name, desc, kind string) {
		tier := m.tier(name, col.max())
		if tier == tierNone {
			return
		}
		col.add(tier, name, editor.Candidate{Text: in.text(name, tier, true), Display: name, Description: desc, Kind: kind}, false)
	}
	for _, a := range sh.Abbrs() {
		if a.Regex == nil && a.Command == "" {
			add(a.Name, abbrDesc(a), "abbr")
		}
	}
	names := make([]string, 0, len(sh.aliases))
	for n := range sh.aliases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		add(n, truncateDesc(sh.aliases[n], 64), "alias")
	}
	for _, n := range sh.FunctionNames(strings.HasPrefix(v, "_")) {
		desc := ""
		if fn := sh.funcs[n]; fn != nil {
			desc = fn.Description
		}
		add(n, desc, "function")
	}
	for _, n := range BuiltinNames() {
		if strings.HasPrefix(n, "_") && !strings.HasPrefix(v, "_") {
			continue
		}
		add(n, BuiltinDescription(n), "builtin")
	}
	for _, n := range shellKeywords {
		add(n, "keyword", "keyword")
	}
	for _, n := range sh.PathCommands() {
		add(n, "", "command")
	}
}

// abbrDesc describes an abbreviation for the pager.
func abbrDesc(a *Abbr) string {
	if a.Function != "" {
		return "Abbreviation: " + a.Function + "()"
	}
	return truncateDesc("Abbreviation: "+a.Expansion, 64)
}

// completeArgs completes an argument of the command at l.words[idx].
func (r *compRun) completeArgs(col *collector, idx int) {
	l := r.line
	cw := l.words[idx]
	argv := make([]string, 0, len(l.words)-idx)
	for _, w := range l.words[idx:] {
		argv = append(argv, w.value)
	}
	transient := l.buf[cw.start:l.cursor]
	useFiles, force, cd := true, false, false
	visited := map[string]bool{}
	var walk func(name string, argv []string, transient string, cmdLen, tokOff, depth int)
	walk = func(name string, argv []string, transient string, cmdLen, tokOff, depth int) {
		if depth > 16 || visited[name] || name == "" {
			return
		}
		visited[name] = true
		if name == "cd" {
			cd = true
		}
		r.ed.buf, r.ed.pos, r.ed.tok = transient, len(transient), tokOff
		clear(r.conds)
		u, f := r.specComplete(col, name, argv)
		if !u {
			useFiles = false
		}
		if f {
			force = true
		}
		for _, target := range r.wrapTargets(name) {
			target = strings.TrimLeft(target, " \t")
			words := strings.Fields(target)
			if len(words) == 0 {
				continue
			}
			nargv := append(append(make([]string, 0, len(words)+len(argv)), words...), argv[1:]...)
			ntrans := target + transient[cmdLen:]
			walk(words[0], nargv, ntrans, len(words[0]), tokOff-cmdLen+len(target), depth+1)
		}
	}
	if !cw.expand && !cw.glob {
		// ($cmd args: we don't know which command it is)
		walk(cw.value, argv, transient, cw.end-cw.start, l.tok.start-cw.start, 0)
	}
	r.ed.buf, r.ed.pos, r.ed.tok = l.buf[:l.cursor], l.cursor, l.tok.start

	// abbreviations that only expand as arguments of this command
	cmd := cw.value
	in := r.tokenInsertion()
	m := newMatcher(l.tok.value, true)
	for _, a := range r.sh.Abbrs() {
		if a.Command == cmd && a.Regex == nil {
			if tier := m.tier(a.Name, col.max()); tier != tierNone {
				col.add(tier, a.Name, editor.Candidate{Text: in.text(a.Name, tier, true), Display: a.Name,
					Description: abbrDesc(a), Kind: "abbr"}, false)
			}
		}
	}
	switch {
	case cd:
		r.completeFileToken(col, filesDirs|filesCDPath, true)
	case force || useFiles:
		r.completeFileToken(col, filesAll, true)
	}
}

// wrapTargets returns what a command wraps: complete -w targets, function
// --wraps, and the command an alias runs.
func (r *compRun) wrapTargets(cmd string) []string {
	r.c.mu.RLock()
	out := append([]string(nil), r.c.wraps[cmd]...)
	r.c.mu.RUnlock()
	sh := r.sh
	if fn := sh.funcs[cmd]; fn != nil {
		out = append(out, fn.Wraps...)
	}
	if v, ok := sh.aliases[cmd]; ok {
		// only the first simple command of the alias
		if i := strings.IndexAny(v, "|;&<>()\n"); i >= 0 {
			v = v[:i]
		}
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// preload makes sure functions that code may call are loaded into the
// completion shell before the code runs. Functions autoloaded inside a
// subshell (a command substitution or pipeline) would otherwise only be
// defined there.
func (r *compRun) preload(stmts []*syntax.Stmt) {
	walkCommands(stmts, r.preloadFunc)
}

// preloadFunc loads the function name, if there is one, and what it calls.
func (r *compRun) preloadFunc(name string) {
	if r.seen[name] {
		return
	}
	r.seen[name] = true
	fn := r.sh.lookupFunc(name)
	if fn == nil && strings.HasPrefix(name, "__fish_") {
		// a helper from fish's function library
		r.c.fish.ensureFunctions(r.sh)
		fn = r.sh.lookupFunc(name)
	}
	if fn == nil {
		return
	}
	if _, done := r.c.walked.LoadOrStore(fn, struct{}{}); done {
		return
	}
	r.preload(fn.Body)
	if fn.BodyCmd != nil {
		walkCommand(fn.BodyCmd, r.preloadFunc)
	}
}

// walkCommands calls f with the literal command names used in stmts,
// including those in substitutions and nested blocks (but not in function
// definitions).
func walkCommands(stmts []*syntax.Stmt, f func(string)) {
	for _, st := range stmts {
		if st != nil {
			walkCommand(st.Cmd, f)
		}
	}
}

func walkCommand(c syntax.Command, f func(string)) {
	switch c := c.(type) {
	case *syntax.CallExpr:
		if len(c.Args) > 0 {
			if name, ok := c.Args[0].LitString(); ok && name != "" {
				f(name)
			}
		}
		for _, a := range c.Assigns {
			walkWord(a.Value, f)
			walkWords(a.Array, f)
		}
		walkWords(c.Args, f)
		walkRedirs(c.Redirs, f)
	case *syntax.BinaryCmd:
		walkCommand(c.X, f)
		walkCommand(c.Y, f)
	case *syntax.Pipeline:
		for _, x := range c.Cmds {
			walkCommand(x, f)
		}
	case *syntax.EnvCmd:
		for _, a := range c.Assigns {
			walkWord(a.Value, f)
		}
		walkCommand(c.Cmd, f)
	case *syntax.Block:
		walkCommands(c.Stmts, f)
		walkRedirs(c.Redirs, f)
	case *syntax.Subshell:
		walkCommands(c.Stmts, f)
	case *syntax.IfClause:
		walkCommands(c.Cond, f)
		walkCommands(c.Then, f)
		for _, e := range c.Elifs {
			walkCommands(e.Cond, f)
			walkCommands(e.Then, f)
		}
		walkCommands(c.Else, f)
	case *syntax.WhileClause:
		walkCommands(c.Cond, f)
		walkCommands(c.Body, f)
	case *syntax.ForClause:
		walkWords(c.Items, f)
		walkCommands(c.Body, f)
	case *syntax.CaseClause:
		walkWord(c.Word, f)
		for _, it := range c.Items {
			walkWords(it.Patterns, f)
			walkCommands(it.Body, f)
		}
	case *syntax.TestClause:
		walkTest(c.Expr, f)
	}
}

func walkTest(e syntax.TestExpr, f func(string)) {
	switch e := e.(type) {
	case *syntax.TestBinary:
		walkTest(e.X, f)
		walkTest(e.Y, f)
	case *syntax.TestUnary:
		walkTest(e.X, f)
	case *syntax.TestWord:
		walkWord(e.W, f)
	case *syntax.TestParen:
		walkTest(e.X, f)
	}
}

func walkRedirs(rs []*syntax.Redirect, f func(string)) {
	for _, rd := range rs {
		walkWord(rd.Target, f)
	}
}

func walkWords(ws []*syntax.Word, f func(string)) {
	for _, w := range ws {
		walkWord(w, f)
	}
}

func walkWord(w *syntax.Word, f func(string)) {
	if w != nil {
		walkParts(w.Parts, f)
	}
}

func walkParts(parts []syntax.WordPart, f func(string)) {
	for _, p := range parts {
		switch p := p.(type) {
		case *syntax.DblQuoted:
			walkParts(p.Parts, f)
		case *syntax.CmdSubst:
			walkCommands(p.Stmts, f)
		case *syntax.ProcSubst:
			walkCommands(p.Stmts, f)
		case *syntax.ParamExp:
			walkWord(p.Arg, f)
			walkWord(p.Arg2, f)
		case *syntax.BraceExp:
			for _, alt := range p.Alts {
				walkParts(alt, f)
			}
		}
	}
}

// compEditor is what the commandline builtin sees while completion code
// runs: the command line being completed rather than the editor's buffer
// (fish's "transient command line").
type compEditor struct {
	inner Editor
	buf   string
	pos   int
	tok   int // where the token being completed starts in buf
}

func (e *compEditor) Buffer() (string, int) { return e.buf, e.pos }
func (e *compEditor) SetBuffer(string, int) {}
func (e *compEditor) Repaint()              {}
func (e *compEditor) PagerActive() bool     { return false }
func (e *compEditor) Reading() bool         { return true }
func (e *compEditor) QueueFunctions(n ...string) {
	if e.inner != nil {
		e.inner.QueueFunctions(n...)
	}
}

func (e *compEditor) Bind(mode, key, command, setsMode string) error {
	if e.inner != nil {
		return e.inner.Bind(mode, key, command, setsMode)
	}
	return nil
}

func (e *compEditor) Unbind(mode, key string) bool {
	return e.inner != nil && e.inner.Unbind(mode, key)
}

func (e *compEditor) Bindings() []string {
	if e.inner != nil {
		return e.inner.Bindings()
	}
	return nil
}

func (e *compEditor) SetKeymap(name string) {
	if e.inner != nil {
		e.inner.SetKeymap(name)
	}
}

func (e *compEditor) Keymap() string {
	if e.inner != nil {
		return e.inner.Keymap()
	}
	return "default"
}

func init() {
	// While a completion runs, `commandline` reads the command line being
	// completed, and `commandline -c` with tokens (-o/-x) leaves out the
	// token at the cursor, as fish does: completion scripts rely on
	// `commandline -opc` listing only the finished words. This is set up
	// here, after builtin_fish.go registered the builtin.
	orig := builtins["commandline"]
	if orig == nil {
		orig = bCommandline
	}
	builtins["commandline"] = func(sh *Shell, io *IO, args []string) int {
		r := sh.shared.complete.active.Load()
		if r == nil {
			return orig(sh, io, args)
		}
		view := *r.ed
		if cutTokens(args) && view.tok <= len(view.buf) {
			view.buf, view.pos = view.buf[:view.tok], view.tok
		}
		saved := sh.Editor
		sh.Editor = &view
		defer func() { sh.Editor = saved }()
		return orig(sh, io, args)
	}
}

// cutTokens reports whether commandline's arguments ask for the tokens
// before the cursor (-c together with -o, -x or --tokenize).
func cutTokens(args []string) bool {
	cut, tok := false, false
	for _, a := range args[1:] {
		if a == "--" || !strings.HasPrefix(a, "-") || len(a) < 2 {
			break
		}
		if strings.HasPrefix(a, "--") {
			switch a {
			case "--cut-at-cursor":
				cut = true
			case "--tokenize", "--tokens-expanded", "--tokens-raw":
				tok = true
			}
			continue
		}
		if strings.ContainsRune(a, 'c') {
			cut = true
		}
		if strings.ContainsAny(a, "ox") {
			tok = true
		}
	}
	return cut && tok
}
