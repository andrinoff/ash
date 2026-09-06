package shell

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/andrinoff/ash/internal/syntax"
)

// Completion files are loaded the first time a command is completed. They
// are searched, in order, in:
//
//	$ash_complete_path (default ~/.config/ash/completions)
//	~/.config/fish/completions
//	fish's vendor_completions.d directories
//	fish's own completions (embedded in the fish 4 binary, extracted on
//	    demand into ~/.cache/ash/fish-VERSION/completions)
//	~/.cache/fish/generated_completions (made by fish_update_completions)
//
// and are named CMD.ash (ash syntax) or CMD.fish (fish quoting rules).

// load sources the completion file for cmd, the first time only.
func (r *compRun) load(cmd string) {
	if cmd == "" || cmd == "." || cmd == ".." || strings.ContainsAny(cmd, "/\x00") {
		return
	}
	c := r.c
	c.mu.Lock()
	if c.loaded[cmd] {
		c.mu.Unlock()
		return
	}
	c.loaded[cmd] = true
	c.mu.Unlock()
	if p := r.findCompletion(cmd); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			r.source(p, b)
		}
		return
	}
	if p, src := c.fish.completion(r.sh, cmd); src != nil {
		r.source(p, src)
		return
	}
	for _, d := range r.generatedDirs() {
		if p := findCompletionIn(d, cmd); p != "" {
			if b, err := os.ReadFile(p); err == nil {
				r.source(p, b)
			}
			return
		}
	}
}

// completionDirs returns the directories searched before fish's own
// completions.
func (r *compRun) completionDirs() []string {
	sh := r.sh
	var dirs []string
	if v, ok := sh.getVar("ash_complete_path"); ok {
		for _, d := range v {
			dirs = append(dirs, strings.Split(d, ":")...)
		}
	} else {
		dirs = append(dirs, filepath.Join(sh.ConfigDir(), "completions"))
	}
	home := sh.HomeDir()
	cfg := sh.Get("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	dirs = append(dirs, filepath.Join(cfg, "fish", "completions"))
	data := sh.Get("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	dirs = append(dirs, filepath.Join(data, "fish", "vendor_completions.d"))
	if dd := sh.Get("XDG_DATA_DIRS"); dd != "" {
		for _, d := range strings.Split(dd, ":") {
			if d != "" {
				dirs = append(dirs, filepath.Join(d, "fish", "vendor_completions.d"))
			}
		}
	}
	dirs = append(dirs,
		"/opt/homebrew/share/fish/vendor_completions.d",
		"/usr/local/share/fish/vendor_completions.d",
		"/usr/share/fish/vendor_completions.d",
	)
	return dirs
}

// generatedDirs returns the directories searched after fish's own
// completions.
func (r *compRun) generatedDirs() []string {
	cache := r.sh.Get("XDG_CACHE_HOME")
	if cache == "" {
		cache = filepath.Join(r.sh.HomeDir(), ".cache")
	}
	return []string{filepath.Join(cache, "fish", "generated_completions")}
}

// findCompletion returns the first completion file for cmd in the
// directories searched before fish's own completions.
func (r *compRun) findCompletion(cmd string) string {
	seen := map[string]bool{}
	for _, d := range r.completionDirs() {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		if p := findCompletionIn(d, cmd); p != "" {
			return p
		}
	}
	return ""
}

func findCompletionIn(dir, cmd string) string {
	for _, ext := range [...]string{".ash", ".fish"} {
		p := filepath.Join(dir, cmd+ext)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// source runs a completion file in the completion shell, quietly. Its
// functions and specs are defined globally; `set -l` stays in the file.
func (r *compRun) source(file string, b []byte) {
	src := string(b)
	f, err := parse(src, file)
	if err != nil {
		return
	}
	if strings.HasSuffix(file, ".fish") {
		// fish completions use fish's helper functions
		r.c.fish.ensureFunctions(r.sh)
	}
	sh := r.sh
	for _, st := range f.Stmts {
		if _, def := st.Cmd.(*syntax.FuncDecl); !def {
			walkCommand(st.Cmd, r.preloadFunc)
		}
	}
	saved := sh.scope
	sh.scope = newScope(sh.global, scopeBlock)
	sh.srcStack = append(sh.srcStack, file)
	prevSrc := sh.curSrc
	sh.curSrc = src
	ctrl, exit := sh.ctrl, sh.exitStatus
	sh.noErrexit++
	sh.runStmts(f.Stmts, r.io)
	sh.noErrexit--
	sh.ctrl, sh.exitStatus = ctrl, exit
	sh.curSrc = prevSrc
	sh.srcStack = sh.srcStack[:len(sh.srcStack)-1]
	sh.scope = saved
}
