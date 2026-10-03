package shell

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// LoginSetup does what a login shell is expected to do before reading
// the user's config. On macOS that means building PATH and MANPATH from
// /etc/paths and /etc/paths.d the way path_helper(8) does, since nothing
// else will when ash is the login shell.
func (sh *Shell) LoginSetup() {
	if runtime.GOOS != "darwin" {
		return
	}
	sh.pathHelper("PATH", "/etc/paths", "/etc/paths.d")
	if sh.IsSet("MANPATH") {
		sh.pathHelper("MANPATH", "/etc/manpaths", "/etc/manpaths.d")
	}
}

func (sh *Shell) pathHelper(name, file, dir string) {
	var sys []string
	read := func(p string) {
		b, err := os.ReadFile(p)
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				sys = append(sys, line)
			}
		}
	}
	read(file)
	ents, _ := os.ReadDir(dir)
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		read(filepath.Join(dir, n))
	}
	if len(sys) == 0 {
		return
	}
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	for _, d := range sys {
		add(d)
	}
	for _, d := range sh.GetList(name) {
		add(d)
	}
	sh.SetVar(name, out, setOpts{scope: setGlobal, export: 1})
}
