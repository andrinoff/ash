package shell

import (
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Var is a shell variable. Every variable is a list of strings; a scalar
// is a list of one. Vars are immutable once stored so that subshells can
// share them without copying.
type Var struct {
	Vals     []string
	Export   bool
	ReadOnly bool
	Path     bool // PATH-like: split on ':' when set from a string, joined with ':' when exported
}

func (v *Var) String() string {
	if v == nil {
		return ""
	}
	sep := " "
	if v.Path {
		sep = ":"
	}
	return strings.Join(v.Vals, sep)
}

// IsPathVar reports whether name gets PATH-style list handling.
func IsPathVar(name string) bool {
	return strings.HasSuffix(name, "PATH")
}

type scopeKind uint8

const (
	scopeGlobal scopeKind = iota
	scopeFunc
	scopeBlock
)

type scope struct {
	vars   map[string]*Var
	parent *scope
	kind   scopeKind
	owned  bool // false: vars is shared with a clone, copy before writing
}

func newScope(parent *scope, kind scopeKind) *scope {
	return &scope{parent: parent, kind: kind, owned: true}
}

func (s *scope) get(name string) *Var {
	if s.vars == nil {
		return nil
	}
	return s.vars[name]
}

func (s *scope) put(name string, v *Var) {
	if !s.owned || s.vars == nil {
		m := make(map[string]*Var, len(s.vars)+1)
		for k, x := range s.vars {
			m[k] = x
		}
		s.vars = m
		s.owned = true
	}
	if v == nil {
		delete(s.vars, name)
		return
	}
	s.vars[name] = v
}

// cloneChain duplicates a scope chain for a subshell. Maps are shared and
// copied on first write by either side.
func cloneChain(s *scope, memo map[*scope]*scope) *scope {
	if s == nil {
		return nil
	}
	if c, ok := memo[s]; ok {
		return c
	}
	c := &scope{vars: s.vars, kind: s.kind}
	s.owned = false
	memo[s] = c
	c.parent = cloneChain(s.parent, memo)
	return c
}

// lookupVar finds a variable in the visible scopes.
func (sh *Shell) lookupVar(name string) (*Var, *scope) {
	for s := sh.scope; s != nil; s = s.parent {
		if v := s.get(name); v != nil {
			return v, s
		}
	}
	return nil, nil
}

// funcScope returns the innermost function scope, or the global scope.
func (sh *Shell) funcScope() *scope {
	for s := sh.scope; s != nil; s = s.parent {
		if s.kind != scopeBlock {
			return s
		}
	}
	return sh.global
}

// specialVar returns values for variables computed on the fly.
func (sh *Shell) specialVar(name string) ([]string, bool) {
	switch name {
	case "?", "status":
		return []string{strconv.Itoa(sh.status)}, true
	case "pipestatus", "PIPESTATUS":
		out := make([]string, len(sh.pipestatus))
		for i, s := range sh.pipestatus {
			out[i] = strconv.Itoa(s)
		}
		if len(out) == 0 {
			out = []string{strconv.Itoa(sh.status)}
		}
		return out, true
	case "$", "fish_pid", "ash_pid":
		return []string{strconv.Itoa(os.Getpid())}, true
	case "PPID": // POSIX
		return []string{strconv.Itoa(os.Getppid())}, true
	case "UID", "EUID": // bash, zsh: scripts test [ "$EUID" -ne 0 ]
		if name == "UID" {
			return []string{strconv.Itoa(os.Getuid())}, true
		}
		return []string{strconv.Itoa(os.Geteuid())}, true
	case "!", "last_pid":
		if sh.lastBg == 0 {
			return nil, name == "last_pid"
		}
		return []string{strconv.Itoa(sh.lastBg)}, true
	case "#":
		return []string{strconv.Itoa(len(sh.args()))}, true
	case "@", "*":
		return sh.args(), true
	case "0":
		return []string{sh.arg0}, true
	case "-":
		return []string{sh.optFlags()}, true
	case "_":
		return []string{sh.lastArg}, true
	case "RANDOM":
		return []string{strconv.Itoa(sh.rand.Intn(32768))}, true
	case "SECONDS":
		return []string{strconv.Itoa(int(time.Since(sh.startTime).Seconds()))}, true
	case "EPOCHSECONDS":
		return []string{strconv.FormatInt(time.Now().Unix(), 10)}, true
	case "EPOCHREALTIME":
		now := time.Now()
		return []string{strconv.FormatInt(now.Unix(), 10) + "." + strconv.FormatInt(int64(now.Nanosecond()/1000)+1000000, 10)[1:]}, true
	case "LINENO":
		return []string{strconv.Itoa(sh.lineno)}, true
	case "CMD_DURATION", "cmd_duration":
		return []string{strconv.FormatInt(sh.cmdDuration.Milliseconds(), 10)}, true
	case "COLUMNS", "LINES":
		if v, _ := sh.lookupVar(name); v != nil {
			return v.Vals, true
		}
		cols, rows := sh.termSize()
		if name == "COLUMNS" {
			return []string{strconv.Itoa(cols)}, true
		}
		return []string{strconv.Itoa(rows)}, true
	case "history":
		if sh.History == nil {
			return nil, true
		}
		return sh.History.Recent(0), true
	case "dirprev":
		return append([]string(nil), sh.dirPrev...), true
	case "dirnext":
		return append([]string(nil), sh.dirNext...), true
	case "fish_kill_signal":
		return []string{"0"}, true
	}
	if len(name) == 1 && name[0] >= '1' && name[0] <= '9' {
		i := int(name[0] - '0')
		args := sh.args()
		if i <= len(args) {
			return []string{args[i-1]}, true
		}
		return nil, true
	}
	if len(name) > 1 && isAllDigits(name) {
		i, _ := strconv.Atoi(name)
		args := sh.args()
		if i >= 1 && i <= len(args) {
			return []string{args[i-1]}, true
		}
		return nil, true
	}
	return nil, false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// getVar returns the values of a variable and whether it is set.
func (sh *Shell) getVar(name string) ([]string, bool) {
	if vals, ok := sh.specialVar(name); ok {
		return vals, vals != nil || name == "@" || name == "*" || name == "pipestatus" || name == "argv"
	}
	if v, _ := sh.lookupVar(name); v != nil {
		return v.Vals, true
	}
	return nil, false
}

// Get returns a variable joined into one string ("" if unset).
func (sh *Shell) Get(name string) string {
	if vals, ok := sh.specialVar(name); ok {
		return strings.Join(vals, " ")
	}
	v, _ := sh.lookupVar(name)
	return v.String()
}

// GetList returns the elements of a variable.
func (sh *Shell) GetList(name string) []string {
	vals, _ := sh.getVar(name)
	return vals
}

// IsSet reports whether a variable exists.
func (sh *Shell) IsSet(name string) bool {
	_, ok := sh.getVar(name)
	return ok
}

// args returns the positional parameters ($argv / $@).
func (sh *Shell) args() []string {
	if v, _ := sh.lookupVar("argv"); v != nil {
		return v.Vals
	}
	return nil
}

type setScope uint8

const (
	setAuto      setScope = iota // fish `set x`: existing scope, else function-local
	setShAuto                    // sh `x=1`: existing scope, else global
	setLocal                     // innermost block scope (fish set -l)
	setFunction                  // function scope (sh local, fish set -f)
	setGlobal                    // global (set -g)
	setUniversal                 // set -U
)

type setOpts struct {
	scope    setScope
	export   int8 // 1: export, -1: unexport, 0: keep
	readOnly bool
	path     int8 // 1: force path var, -1: force non-path, 0: by name
}

// SetVar assigns a variable. It is the single entry point for writes so
// that variable events and exports stay consistent.
func (sh *Shell) SetVar(name string, vals []string, o setOpts) error {
	if !validVarName(name) {
		return errorf("%s: invalid variable name", name)
	}
	if isReadOnlySpecial(name) {
		return errorf("%s: read-only variable", name)
	}
	switch name {
	case "SECONDS", "RANDOM":
		// As in bash, SECONDS=n restarts the count from n (the timing
		// idiom SECONDS=0) and RANDOM=n seeds the generator.
		n, _ := strconv.ParseInt(strings.TrimSpace(strings.Join(vals, "")), 10, 64)
		if name == "SECONDS" {
			sh.startTime = time.Now().Add(-time.Duration(n) * time.Second)
		} else {
			sh.rand = rand.New(rand.NewSource(n))
		}
		return nil
	}
	existing, where := sh.lookupVar(name)
	var target *scope
	switch o.scope {
	case setAuto:
		if where != nil {
			target = where
		} else if sh.inFunction() {
			target = sh.funcScope()
		} else {
			target = sh.global
		}
	case setShAuto:
		if where != nil {
			target = where
		} else {
			target = sh.global
		}
	case setLocal:
		target = sh.scope
	case setFunction:
		target = sh.funcScope()
	case setGlobal, setUniversal:
		target = sh.global
	}
	var prev *Var
	if target == where {
		prev = existing
	} else {
		prev = target.get(name)
	}
	if prev != nil && prev.ReadOnly {
		return errorf("%s: read-only variable", name)
	}
	nv := &Var{Vals: vals}
	if prev != nil {
		nv.Export = prev.Export
		nv.Path = prev.Path
	} else if existing != nil && existing.Export && target != where {
		// Shadowing an exported global keeps it exported (fish does this).
		nv.Export = true
	}
	if prev == nil {
		nv.Path = IsPathVar(name)
	}
	switch o.path {
	case 1:
		nv.Path = true
	case -1:
		nv.Path = false
	}
	switch o.export {
	case 1:
		nv.Export = true
	case -1:
		nv.Export = false
	}
	if o.readOnly {
		nv.ReadOnly = true
	}
	if nv.Path {
		nv.Vals = splitPathList(vals)
	}
	target.put(name, nv)
	if o.scope == setUniversal {
		sh.saveUniversal(name, nv)
	}
	sh.varChanged(name)
	return nil
}

// SetString sets a global string variable (exported if already exported).
func (sh *Shell) SetString(name, val string) {
	sh.SetVar(name, []string{val}, setOpts{scope: setShAuto})
}

func (sh *Shell) setExported(name, val string) {
	sh.SetVar(name, []string{val}, setOpts{scope: setGlobal, export: 1})
}

// UnsetVar removes a variable from the innermost scope that has it.
func (sh *Shell) UnsetVar(name string, sc setScope) error {
	var target *scope
	switch sc {
	case setGlobal, setUniversal:
		if sh.global.get(name) != nil {
			target = sh.global
		}
	case setLocal:
		if sh.scope.get(name) != nil {
			target = sh.scope
		}
	case setFunction:
		if fs := sh.funcScope(); fs.get(name) != nil {
			target = fs
		}
	default:
		_, target = sh.lookupVar(name)
	}
	if target == nil {
		return errNotSet
	}
	if v := target.get(name); v.ReadOnly {
		return errorf("%s: read-only variable", name)
	}
	target.put(name, nil)
	if sc == setUniversal {
		sh.saveUniversal(name, nil)
	}
	sh.varChanged(name)
	return nil
}

// splitPathList splits elements of a PATH-like list on colons.
func splitPathList(vals []string) []string {
	need := false
	for _, v := range vals {
		if strings.IndexByte(v, ':') >= 0 || v == "" {
			need = true
			break
		}
	}
	if !need {
		return vals
	}
	var out []string
	for _, v := range vals {
		if v == "" {
			if len(vals) == 1 {
				return nil
			}
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(v, ":")...)
	}
	return out
}

func validVarName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func isReadOnlySpecial(name string) bool {
	switch name {
	case "status", "pipestatus", "PIPESTATUS", "fish_pid", "ash_pid", "history", "last_pid", "EPOCHSECONDS", "EPOCHREALTIME", "CMD_DURATION", "PPID", "UID", "EUID":
		return true
	}
	return false
}

func (sh *Shell) inFunction() bool {
	for s := sh.scope; s != nil; s = s.parent {
		if s.kind == scopeFunc {
			return true
		}
	}
	return false
}

// Environ builds the environment for child processes.
func (sh *Shell) Environ() []string {
	seen := make(map[string]bool, 64)
	env := make([]string, 0, 64)
	for s := sh.scope; s != nil; s = s.parent {
		for name, v := range s.vars {
			if seen[name] {
				continue
			}
			seen[name] = true
			if v.Export {
				sep := " "
				if v.Path {
					sep = ":"
				}
				env = append(env, name+"="+strings.Join(v.Vals, sep))
			}
		}
	}
	return append(env, sh.foreignEnv...)
}

// VarNames lists visible variable names, sorted.
func (sh *Shell) VarNames() []string {
	seen := map[string]bool{}
	var names []string
	for s := sh.scope; s != nil; s = s.parent {
		for name := range s.vars {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	for _, n := range []string{"status", "pipestatus", "history", "CMD_DURATION", "fish_pid", "ash_pid", "COLUMNS", "LINES", "RANDOM", "SECONDS"} {
		if !seen[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// importEnv loads the process environment into the global scope.
func (sh *Shell) importEnv(environ []string) {
	for _, kv := range environ {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		name, val := kv[:i], kv[i+1:]
		if !validVarName(name) {
			sh.foreignEnv = append(sh.foreignEnv, kv)
			continue
		}
		v := &Var{Vals: []string{val}, Export: true}
		if IsPathVar(name) {
			v.Path = true
			v.Vals = splitPathList(v.Vals)
		}
		sh.global.put(name, v)
	}
}
