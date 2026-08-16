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
	vars map[string]*Var
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
	u.vars = map[string]*Var{}
	b, err := os.ReadFile(u.path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\x1e")
		if len(fields) < 2 {
			continue
		}
		head := strings.SplitN(fields[0], " ", 2)
		if len(head) != 2 {
			continue
		}
		flags, name := head[0], head[1]
		v := &Var{Vals: unescapeUniversal(fields[1:]), Export: strings.Contains(flags, "x"), Path: IsPathVar(name)}
		if len(fields) == 2 && fields[1] == "" {
			v.Vals = []string{}
		}
		u.vars[name] = v
		if sh.global.get(name) == nil {
			sh.global.put(name, v)
		}
	}
}

func unescapeUniversal(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = strings.NewReplacer(`\n`, "\n", `\\`, `\`).Replace(v)
	}
	return out
}

func (sh *Shell) saveUniversal(name string, v *Var) {
	u := sh.shared.universal
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.vars == nil {
		u.vars = map[string]*Var{}
		u.path = sh.universalPath()
	}
	if v == nil {
		delete(u.vars, name)
	} else {
		u.vars[name] = v
	}
	names := make([]string, 0, len(u.vars))
	for n := range u.vars {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# ash universal variables. Edit with `set -U` rather than by hand.\n")
	for _, n := range names {
		x := u.vars[n]
		flags := "-"
		if x.Export {
			flags = "x"
		}
		b.WriteString(flags + " " + n)
		if len(x.Vals) == 0 {
			b.WriteString("\x1e")
		}
		for _, val := range x.Vals {
			b.WriteString("\x1e" + strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(val))
		}
		b.WriteByte('\n')
	}
	os.MkdirAll(filepath.Dir(u.path), 0o755)
	tmp := u.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err == nil {
		os.Rename(tmp, u.path)
	}
}
