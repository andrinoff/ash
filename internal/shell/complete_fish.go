package shell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fishLib gives access to the completions and helper functions built into
// fish 4, which keeps them inside its binary. They are extracted with
// `status get-file` into a cache directory for the installed fish version
// the first time they are needed, and read from there afterwards.
type fishLib struct {
	mu        sync.Mutex
	inited    bool
	bin       string          // fish, if installed
	root      string          // cache directory for this fish version
	names     map[string]bool // completions built into fish
	funcsDone bool
}

// fishEnv is what the fish library needs from the shell. It is gathered
// on the shell's goroutine so the work itself can run on another one.
type fishEnv struct {
	bin     string // fish in $PATH, or ""
	cache   string // the shell's cache directory
	environ []string
}

func (sh *Shell) fishEnv() fishEnv {
	bin, _ := sh.LookPath("fish")
	return fishEnv{bin: bin, cache: sh.CacheDir(), environ: sh.Environ()}
}

const fishTimeout = 10 * time.Second

var errFishTimeout = errors.New("fish took too long")

// PrepareCompletions does, in the background, the one-time work that
// completing with fish's own completions needs (finding fish's version and
// extracting its helper functions, once per fish version), so the first
// Tab doesn't wait for it. It returns at once and does nothing if fish is
// not installed.
func (sh *Shell) PrepareCompletions() {
	f := &sh.shared.complete.fish
	f.mu.Lock()
	done := f.funcsDone
	f.mu.Unlock()
	if done {
		return
	}
	env := sh.fishEnv()
	if env.bin == "" {
		return
	}
	go f.functions(env, sh.AddFunctionPath)
}

// init finds fish's version and its list of completions. It reports
// whether fish can be used. Call with f.mu held.
func (f *fishLib) init(env fishEnv) bool {
	if f.inited {
		return f.bin != ""
	}
	f.inited = true
	if env.bin == "" {
		return false
	}
	f.bin = env.bin
	ver := f.version(env)
	if ver == "" {
		f.bin = ""
		return false
	}
	f.root = filepath.Join(env.cache, "fish-"+ver)
	f.names = f.listCompletions(env)
	return true
}

// version returns the version of fish, remembered in the cache directory
// as long as the binary doesn't change.
func (f *fishLib) version(env fishEnv) string {
	resolved, err := filepath.EvalSymlinks(f.bin)
	if err != nil {
		resolved = f.bin
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return ""
	}
	key := resolved + "\t" + strconv.FormatInt(st.Size(), 10) + "\t" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
	stamp := filepath.Join(env.cache, "fish-version")
	if b, err := os.ReadFile(stamp); err == nil {
		if k, v, ok := strings.Cut(string(b), "\n"); ok && k == key {
			if v = strings.TrimSpace(v); validVersion(v) {
				return v
			}
		}
	}
	out, err := f.run(env, "--version")
	if err != nil {
		return ""
	}
	// "fish, version 4.8.1"
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	v := fields[len(fields)-1]
	if !validVersion(v) {
		return ""
	}
	writeFileAtomic(stamp, []byte(key+"\n"+v+"\n"))
	return v
}

// validVersion reports whether v is safe to use in a directory name.
func validVersion(v string) bool {
	if v == "" || v == "." || v == ".." || len(v) > 64 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '-' || c == '_' || c == '+') {
			return false
		}
	}
	return true
}

// run runs fish with the given arguments and returns its output.
func (f *fishLib) run(env fishEnv, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fishTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bin, args...)
	cmd.Env = env.environ
	cmd.Dir = "/"
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, errFishTimeout
	}
	return out.Bytes(), err
}

// script runs a fish script, without the user's configuration.
func (f *fishLib) script(env fishEnv, src string, args ...string) ([]byte, error) {
	return f.run(env, append([]string{"--no-config", "-c", src}, args...)...)
}

// listCompletions returns the names of the completions built into fish.
func (f *fishLib) listCompletions(env fishEnv) map[string]bool {
	p := filepath.Join(f.root, "completions.list")
	b, err := os.ReadFile(p)
	if err != nil {
		out, err := f.script(env, "status list-files completions")
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			return map[string]bool{}
		}
		// (fish 3 has no built-in files and fails: remember that too)
		b = out
		writeFileAtomic(p, b)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if name, ok := strings.CutPrefix(line, "completions/"); ok {
			if name, ok = strings.CutSuffix(name, ".fish"); ok && name != "" && !strings.ContainsAny(name, "/\x00") {
				names[name] = true
			}
		}
	}
	return names
}

// completion returns fish's own completion file for cmd, extracting it
// if needed: its path and contents (nil if there is none).
func (f *fishLib) completion(sh *Shell, cmd string) (string, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inited && (f.bin == "" || !f.names[cmd]) {
		return "", nil
	}
	env := sh.fishEnv()
	if !f.init(env) || !f.names[cmd] {
		return "", nil
	}
	p := filepath.Join(f.root, "completions", cmd+".fish")
	if b, err := os.ReadFile(p); err == nil {
		return p, b
	}
	out, err := f.script(env, "status get-file $argv[1]", "completions/"+cmd+".fish")
	if err != nil || len(out) == 0 {
		return "", nil
	}
	writeFileAtomic(p, out)
	return p, out
}

// ensureFunctions makes fish's helper functions autoloadable.
func (f *fishLib) ensureFunctions(sh *Shell) {
	f.mu.Lock()
	done := f.funcsDone || f.inited && f.bin == ""
	f.mu.Unlock()
	if done {
		return
	}
	f.functions(sh.fishEnv(), sh.AddFunctionPath)
}

// functions extracts fish's helper functions (__fish_*), once per fish
// version, and passes their directory to register (for the autoloader),
// the first time only. Only the __fish_ helpers are used: fish's other
// functions (ls, cd, history...) would shadow ash's own commands.
func (f *fishLib) functions(env fishEnv, register func(dir string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.funcsDone || !f.init(env) {
		return
	}
	f.funcsDone = true
	dir := filepath.Join(f.root, "functions")
	marker := filepath.Join(dir, ".complete")
	if _, err := os.Stat(marker); err == nil {
		register(dir)
		return
	}
	out, err := f.script(env, `for f in (status list-files functions); printf '\x1eASHFILE %s\n' $f; status get-file $f; end`)
	n := 0
	for _, rec := range bytes.Split(out, []byte("\x1eASHFILE ")) {
		nl := bytes.IndexByte(rec, '\n')
		if nl < 0 {
			continue
		}
		base := path.Base(string(rec[:nl]))
		if !strings.HasPrefix(base, "__fish_") || !strings.HasSuffix(base, ".fish") || strings.ContainsAny(base, "/\x00") {
			continue
		}
		if writeFileAtomic(filepath.Join(dir, base), rec[nl+1:]) == nil {
			n++
		}
	}
	if n == 0 {
		return
	}
	if err == nil {
		// complete: no need to ask fish again for this version
		writeFileAtomic(marker, []byte(strconv.Itoa(n)+"\n"))
	}
	// registered with f.mu held, so whoever sees funcsDone can use them
	register(dir)
}

// writeFileAtomic writes a file through a temporary file and a rename.
func writeFileAtomic(p string, data []byte) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	os.Chmod(tmp.Name(), 0o644)
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
