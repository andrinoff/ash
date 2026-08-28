package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/andrinoff/ash/internal/syntax"
)

// Function is a shell function.
type Function struct {
	Name        string
	Body        []*syntax.Stmt
	BodyCmd     syntax.Command
	Fish        bool
	Description string
	Wraps       []string
	ArgNames    []string
	NoShadow    bool
	Inherit     map[string]*Var
	OnVariable  []string
	OnEvent     []string
	OnSignal    []string
	Src         string
	File        string
}

func (sh *Shell) lookupFunc(name string) *Function {
	if fn, ok := sh.funcs[name]; ok {
		return fn
	}
	if fn := sh.shared.autoload.load(sh, name); fn != nil {
		return fn
	}
	return nil
}

// FunctionNames lists defined functions (including autoloadable ones).
func (sh *Shell) FunctionNames(includeHidden bool) []string {
	seen := map[string]bool{}
	var out []string
	for n := range sh.funcs {
		if !includeHidden && strings.HasPrefix(n, "_") {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	for _, n := range sh.shared.autoload.names(sh) {
		if seen[n] || !includeHidden && strings.HasPrefix(n, "_") {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Function returns a function definition, or nil.
func (sh *Shell) Function(name string) *Function { return sh.lookupFunc(name) }

func (sh *Shell) defineFunc(d *syntax.FuncDecl, io *IO) int {
	name, err := sh.expandString(d.Name, io)
	if err != nil {
		sh.errorMsg(io.Err, "%v", err)
		return 1
	}
	if name == "" || strings.ContainsAny(name, "/\x00") {
		sh.errorMsg(io.Err, "function: invalid name %q", name)
		return 2
	}
	if _, isBuiltin := builtins[name]; isBuiltin && reservedBuiltin(name) {
		sh.errorMsg(io.Err, "function: '%s' is a reserved builtin and cannot be redefined", name)
		return 2
	}
	fn := &Function{Name: name, Body: d.Body, BodyCmd: d.BodyCmd, Fish: d.Fish, Src: d.Src}
	if len(sh.srcStack) > 0 {
		fn.File = sh.srcStack[len(sh.srcStack)-1]
	}
	if d.Fish {
		opts, err := sh.expandFields(d.Opts, io, expNoGlob)
		if err != nil {
			sh.errorMsg(io.Err, "%v", err)
			return 1
		}
		if err := sh.parseFuncOpts(fn, opts); err != nil {
			sh.errorMsg(io.Err, "function %s: %v", name, err)
			return 2
		}
	}
	sh.registerFunc(fn)
	return 0
}

func reservedBuiltin(name string) bool {
	switch name {
	case "builtin", "command", "set", "exec", "eval", "source", ".", "return", "break", "continue", "exit", "function", "functions", "argparse", "status", "and", "or", "not", "begin", "end", "if", "while", "for", "switch", "case":
		return true
	}
	return false
}

func (sh *Shell) registerFunc(fn *Function) {
	sh.removeHandlers(fn.Name)
	sh.setFunc(fn.Name, fn)
	ev := sh.shared.events
	ev.mu.Lock()
	for _, v := range fn.OnVariable {
		ev.vars[v] = append(ev.vars[v], fn.Name)
	}
	for _, e := range fn.OnEvent {
		ev.events[normalizeEvent(e)] = append(ev.events[normalizeEvent(e)], fn.Name)
	}
	ev.mu.Unlock()
	for _, s := range fn.OnSignal {
		sh.shared.traps.setFuncHandler(sh, s, fn.Name)
	}
}

func (sh *Shell) removeHandlers(name string) {
	ev := sh.shared.events
	ev.mu.Lock()
	defer ev.mu.Unlock()
	for k, l := range ev.vars {
		ev.vars[k] = removeString(l, name)
	}
	for k, l := range ev.events {
		ev.events[k] = removeString(l, name)
	}
}

func removeString(l []string, s string) []string {
	out := l[:0]
	for _, x := range l {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// EraseFunction removes a function.
func (sh *Shell) EraseFunction(name string) bool {
	if _, ok := sh.funcs[name]; !ok {
		return false
	}
	sh.removeHandlers(name)
	sh.setFunc(name, nil)
	sh.shared.autoload.forget(name)
	return true
}

func (sh *Shell) parseFuncOpts(fn *Function, opts []string) error {
	for i := 0; i < len(opts); i++ {
		o := opts[i]
		val := func() (string, error) {
			if j := strings.IndexByte(o, '='); j > 0 && strings.HasPrefix(o, "--") {
				return o[j+1:], nil
			}
			if len(o) > 2 && o[0] == '-' && o[1] != '-' {
				return o[2:], nil
			}
			if i+1 >= len(opts) {
				return "", errorf("%s: option requires an argument", o)
			}
			i++
			return opts[i], nil
		}
		name := o
		if j := strings.IndexByte(o, '='); j > 0 && strings.HasPrefix(o, "--") {
			name = o[:j]
		} else if len(o) > 2 && o[0] == '-' && o[1] != '-' {
			name = o[:2]
		}
		switch name {
		case "-d", "--description":
			v, err := val()
			if err != nil {
				return err
			}
			fn.Description = v
		case "-w", "--wraps":
			v, err := val()
			if err != nil {
				return err
			}
			fn.Wraps = append(fn.Wraps, v)
		case "-a", "--argument-names", "--argument":
			if len(o) > 2 && o[1] != '-' {
				fn.ArgNames = append(fn.ArgNames, o[2:])
			}
			for i+1 < len(opts) && !strings.HasPrefix(opts[i+1], "-") {
				i++
				fn.ArgNames = append(fn.ArgNames, opts[i])
			}
		case "-v", "--on-variable":
			v, err := val()
			if err != nil {
				return err
			}
			fn.OnVariable = append(fn.OnVariable, v)
		case "-e", "--on-event":
			v, err := val()
			if err != nil {
				return err
			}
			fn.OnEvent = append(fn.OnEvent, v)
		case "-s", "--on-signal":
			v, err := val()
			if err != nil {
				return err
			}
			fn.OnSignal = append(fn.OnSignal, v)
		case "-j", "--on-job-exit", "-p", "--on-process-exit":
			if _, err := val(); err != nil {
				return err
			}
		case "-S", "--no-scope-shadowing":
			fn.NoShadow = true
		case "-V", "--inherit-variable":
			v, err := val()
			if err != nil {
				return err
			}
			if x, _ := sh.lookupVar(v); x != nil {
				if fn.Inherit == nil {
					fn.Inherit = map[string]*Var{}
				}
				fn.Inherit[v] = x
			}
		case "-k", "--keep":
		default:
			if strings.HasPrefix(o, "-") {
				return errorf("unknown option %s", o)
			}
			// fish allows argument names after the options without -a
			fn.ArgNames = append(fn.ArgNames, o)
		}
	}
	return nil
}

func (sh *Shell) callFunc(fn *Function, args []string, io *IO) int {
	if sh.depth >= maxDepth || len(sh.callStack) > 512 {
		sh.errorMsg(io.Err, "%s: maximum recursion depth reached (a function calling itself?)", fn.Name)
		sh.ctrl = ctrlNone
		return 1
	}
	sh.depth++
	saved := sh.scope
	parent := sh.scope
	if fn.Fish && !fn.NoShadow {
		parent = sh.global
	}
	sc := newScope(parent, scopeFunc)
	sc.vars = make(map[string]*Var, 4+len(fn.ArgNames))
	sc.vars["argv"] = &Var{Vals: append([]string(nil), args[1:]...)}
	for i, n := range fn.ArgNames {
		if i+1 < len(args) {
			sc.vars[n] = &Var{Vals: []string{args[i+1]}}
		} else {
			sc.vars[n] = &Var{Vals: []string{}}
		}
	}
	for k, v := range fn.Inherit {
		sc.vars[k] = v
	}
	sh.scope = sc
	sh.callStack = append(sh.callStack, frame{name: fn.Name, file: fn.File})
	var status int
	if fn.Body != nil || fn.BodyCmd == nil {
		status = sh.runStmts(fn.Body, io)
	} else {
		status = sh.runCommand(fn.BodyCmd, io)
	}
	sh.callStack = sh.callStack[:len(sh.callStack)-1]
	sh.scope = saved
	sh.depth--
	switch sh.ctrl {
	case ctrlReturn:
		sh.ctrl = ctrlNone
		status = sh.status
	case ctrlBreak, ctrlContinue:
		sh.ctrl = ctrlNone
	}
	sh.status = status
	return status
}

// CallFunction runs a function by name if it exists. It returns the
// status and whether the function was found.
func (sh *Shell) CallFunction(name string, args []string, io *IO) (int, bool) {
	fn := sh.lookupFunc(name)
	if fn == nil {
		return 0, false
	}
	return sh.callFunc(fn, append([]string{name}, args...), io), true
}

// autoloader finds functions in $ash_function_path (and fish's
// function directories) the first time they are called.
type autoloader struct {
	mu      sync.Mutex
	tried   map[string]bool
	loading map[string]bool
	listed  []string
	listKey string
}

func newAutoloader() *autoloader {
	return &autoloader{tried: map[string]bool{}, loading: map[string]bool{}}
}

func (sh *Shell) functionPath() []string {
	if v, ok := sh.getVar("ash_function_path"); ok {
		return v
	}
	if v, ok := sh.getVar("fish_function_path"); ok {
		return v
	}
	return []string{filepath.Join(sh.ConfigDir(), "functions")}
}

func (a *autoloader) find(sh *Shell, name string) string {
	for _, dir := range sh.functionPath() {
		for _, ext := range []string{".ash", ".fish", ".sh"} {
			p := filepath.Join(dir, name+ext)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

func (a *autoloader) has(sh *Shell, name string) bool {
	return a.find(sh, name) != ""
}

func (a *autoloader) forget(name string) {
	a.mu.Lock()
	delete(a.tried, name)
	a.mu.Unlock()
}

func (a *autoloader) load(sh *Shell, name string) *Function {
	if name == "" || strings.ContainsAny(name, "/") {
		return nil
	}
	a.mu.Lock()
	if a.tried[name] || a.loading[name] {
		a.mu.Unlock()
		return nil
	}
	a.tried[name] = true
	a.loading[name] = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.loading, name)
		a.mu.Unlock()
	}()
	path := a.find(sh, name)
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	f, err := parse(string(b), path)
	if err != nil {
		fmt.Fprint(sh.Stderr, "ash: "+FormatParseError(string(b), path, err))
		return nil
	}
	// Run the file in the global context so its definitions land globally.
	saved := sh.scope
	sh.scope = sh.global
	sh.srcStack = append(sh.srcStack, path)
	io := sh.StdIO()
	for _, st := range f.Stmts {
		if fd, ok := st.Cmd.(*syntax.FuncDecl); ok {
			sh.defineFunc(fd, io)
		} else {
			sh.runStmt(st, io)
		}
	}
	sh.srcStack = sh.srcStack[:len(sh.srcStack)-1]
	sh.scope = saved
	if fn, ok := sh.funcs[name]; ok {
		return fn
	}
	return nil
}

func (a *autoloader) names(sh *Shell) []string {
	dirs := sh.functionPath()
	key := strings.Join(dirs, "\x00")
	a.mu.Lock()
	if key == a.listKey && a.listed != nil {
		l := a.listed
		a.mu.Unlock()
		return l
	}
	a.mu.Unlock()
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			n := e.Name()
			for _, ext := range []string{".ash", ".fish", ".sh"} {
				if strings.HasSuffix(n, ext) {
					base := strings.TrimSuffix(n, ext)
					if !seen[base] {
						seen[base] = true
						out = append(out, base)
					}
				}
			}
		}
	}
	a.mu.Lock()
	a.listed, a.listKey = out, key
	a.mu.Unlock()
	return out
}

// events maps variable names and event names to handler functions.
type events struct {
	mu     sync.Mutex
	vars   map[string][]string
	events map[string][]string
	firing map[string]bool
}

func newEvents() *events {
	return &events{vars: map[string][]string{}, events: map[string][]string{}, firing: map[string]bool{}}
}

// normalizeEvent lets fish_prompt and ash_prompt (etc.) be the same event.
func normalizeEvent(e string) string {
	for _, p := range []string{"fish_", "ash_"} {
		if strings.HasPrefix(e, p) {
			switch rest := e[len(p):]; rest {
			case "prompt", "preexec", "postexec", "exit", "cancel", "posterror", "focus_in", "focus_out":
				return "ash_" + rest
			}
		}
	}
	return e
}

func (sh *Shell) varChanged(name string) {
	if name == "PATH" {
		sh.shared.paths.clear()
	}
	ev := sh.shared.events
	ev.mu.Lock()
	handlers := ev.vars[name]
	if len(handlers) == 0 || ev.firing["var:"+name] || sh.subshell {
		ev.mu.Unlock()
		return
	}
	handlers = append([]string(nil), handlers...)
	ev.firing["var:"+name] = true
	ev.mu.Unlock()
	defer func() {
		ev.mu.Lock()
		delete(ev.firing, "var:"+name)
		ev.mu.Unlock()
	}()
	saved := sh.status
	savedCtrl := sh.ctrl
	for _, h := range handlers {
		if fn := sh.lookupFunc(h); fn != nil {
			sh.callFunc(fn, []string{h, "VARIABLE", "SET", name}, sh.StdIO())
		}
	}
	sh.status = saved
	sh.ctrl = savedCtrl
}

// Emit fires an event: functions defined with --on-event NAME run.
func (sh *Shell) Emit(event string, args ...string) {
	event = normalizeEvent(event)
	ev := sh.shared.events
	ev.mu.Lock()
	handlers := append([]string(nil), ev.events[event]...)
	ev.mu.Unlock()
	if len(handlers) == 0 {
		return
	}
	saved := sh.status
	savedPipe := sh.pipestatus
	for _, h := range handlers {
		if fn := sh.lookupFunc(h); fn != nil {
			sh.callFunc(fn, append([]string{h, event}, args...), sh.StdIO())
		}
	}
	sh.status = saved
	sh.pipestatus = savedPipe
}

// HasEventHandlers reports whether anything listens for the event.
func (sh *Shell) HasEventHandlers(event string) bool {
	ev := sh.shared.events
	ev.mu.Lock()
	defer ev.mu.Unlock()
	return len(ev.events[normalizeEvent(event)]) > 0
}

// suggestCommand finds a close command name for typos.
func (sh *Shell) suggestCommand(name string) string {
	if len(name) < 2 {
		return ""
	}
	best, bestD := "", 3
	consider := func(c string) {
		if c == name || strings.HasPrefix(c, "_") {
			return
		}
		if abs(len(c)-len(name)) >= bestD {
			return
		}
		d := editDistance(name, c, bestD)
		if d < bestD || d == bestD && len(c) < len(best) {
			best, bestD = c, d
		}
	}
	for n := range builtins {
		consider(n)
	}
	for n := range sh.funcs {
		consider(n)
	}
	for n := range sh.aliases {
		consider(n)
	}
	for _, c := range sh.PathCommands() {
		consider(c)
	}
	if bestD > 2 || bestD > len(name)/2 {
		return ""
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// editDistance is Damerau-Levenshtein, stopping early past max.
func editDistance(a, b string, max int) int {
	ra, rb := []rune(a), []rune(b)
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			v := min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				v = min(v, prev2[j-2]+1)
			}
			cur[j] = v
			if v < rowMin {
				rowMin = v
			}
		}
		if rowMin > max {
			return max + 1
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}

// PathCommands returns executable names found in $PATH (cached by the
// directories' modification times).
func (sh *Shell) PathCommands() []string {
	return sh.shared.paths.commands(sh.GetList("PATH"))
}

type cmdListCache struct {
	key   string
	names []string
}

var pathListCache struct {
	mu sync.Mutex
	c  cmdListCache
}

func (c *pathCache) commands(path []string) []string {
	var key strings.Builder
	for _, d := range path {
		key.WriteString(d)
		if st, err := os.Stat(d); err == nil {
			key.WriteString(st.ModTime().String())
		}
		key.WriteByte(0)
	}
	pathListCache.mu.Lock()
	if pathListCache.c.key == key.String() {
		n := pathListCache.c.names
		pathListCache.mu.Unlock()
		return n
	}
	pathListCache.mu.Unlock()
	seen := map[string]bool{}
	var names []string
	for _, d := range path {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			n := e.Name()
			if seen[n] {
				continue
			}
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 {
				if st, err := os.Stat(filepath.Join(d, n)); err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
					continue
				}
			} else if info.Mode()&0o111 == 0 {
				continue
			}
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	pathListCache.mu.Lock()
	pathListCache.c = cmdListCache{key: key.String(), names: names}
	pathListCache.mu.Unlock()
	return names
}

// LookPath exposes command lookup for the frontend (e.g. integrations).
func (sh *Shell) LookPath(name string) (string, bool) {
	p, err := sh.lookPath(name)
	return p, err == nil
}

// Command builds an exec.Cmd that runs in the shell's directory and
// environment.
func (sh *Shell) Command(name string, args ...string) *exec.Cmd {
	path := name
	if p, err := sh.lookPath(name); err == nil {
		path = p
	}
	c := exec.Command(path, args...)
	c.Dir = sh.dir
	c.Env = sh.Environ()
	return c
}
