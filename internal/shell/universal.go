package shell

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Universal variables persist across sessions in ~/.config/ash/variables.
type universalStore struct {
	mu   sync.Mutex
	path string
	vars map[string]*Var // the universal variables this shell knows
}

func (sh *Shell) universalPath() string {
	return filepath.Join(sh.ConfigDir(), "variables")
}

// LoadUniversal reads persisted universal variables into the global scope.
func (sh *Shell) LoadUniversal() {
	u := sh.shared.universal
	u.mu.Lock()
	defer u.mu.Unlock()
	u.path = sh.universalPath()
	u.vars = readUniversal(u.path)
	for name, v := range u.vars {
		if sh.global.get(name) == nil {
			sh.global.put(name, v)
		}
	}
}

// universalVar returns the universal variable name, or nil.
func (sh *Shell) universalVar(name string) *Var {
	u := sh.shared.universal
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.vars[name]
}

// universalNames lists the universal variables this shell knows, sorted.
func (sh *Shell) universalNames() []string {
	u := sh.shared.universal
	u.mu.Lock()
	defer u.mu.Unlock()
	names := make([]string, 0, len(u.vars))
	for n := range u.vars {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// readUniversal parses the variables file. Each line is "FLAGS NAME"
// followed by one \x1e-separated field per element.
func readUniversal(path string) map[string]*Var {
	vars := map[string]*Var{}
	b, err := os.ReadFile(path)
	if err != nil {
		return vars
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\x1e")
		head := strings.SplitN(fields[0], " ", 2)
		if len(head) != 2 || !validVarName(head[1]) {
			continue
		}
		flags, name := head[0], head[1]
		vars[name] = &Var{Vals: unescapeUniversal(fields[1:]), Export: strings.Contains(flags, "x"), Path: IsPathVar(name), universal: true}
	}
	return vars
}

func unescapeUniversal(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = strings.NewReplacer(`\n`, "\n", `\\`, `\`).Replace(v)
	}
	return out
}

// saveUniversal records a change to a universal variable (v == nil
// erases it) and reports whether it was saved before. The file is read
// again first, so variables other shells saved since this one started
// (or that a non-interactive shell never loaded) are kept.
func (sh *Shell) saveUniversal(name string, v *Var) bool {
	u := sh.shared.universal
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.path == "" {
		u.path = sh.universalPath()
	}
	if u.vars == nil {
		u.vars = map[string]*Var{}
	}
	vars := readUniversal(u.path)
	_, existed := vars[name]
	if v == nil {
		delete(u.vars, name)
		if !existed {
			return false
		}
		delete(vars, name)
	} else {
		u.vars[name] = v
		vars[name] = v
	}
	names := make([]string, 0, len(vars))
	for n := range vars {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# ash universal variables. Edit with `set -U` rather than by hand.\n")
	esc := strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	for _, n := range names {
		x := vars[n]
		flags := "-"
		if x.Export {
			flags = "x"
		}
		b.WriteString(flags + " " + n)
		for _, val := range x.Vals {
			b.WriteString("\x1e" + esc.Replace(val))
		}
		b.WriteByte('\n')
	}
	dir := filepath.Dir(u.path)
	os.MkdirAll(dir, 0o755)
	f, err := os.CreateTemp(dir, ".variables-*")
	if err != nil {
		return existed
	}
	_, err = f.WriteString(b.String())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(f.Name(), u.path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return existed
}
