package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// integScrub lists prefixes of variables from the developer's environment
// that would change what the integrations do.
var integScrub = []string{
	"FZF_", "_ZO_", "Z_", "_Z_", "MISE_", "__MISE_", "ATUIN_", "DIRENV_",
	"TMUX", "STARSHIP_", "VIRTUAL_ENV", "XDG_", "fish_private_mode",
}

// integTest is a shell for integration tests: HOME, the XDG directories
// and the tools' data directories are temp dirs, and PATH is a temp bin
// directory (for fake tools) followed by /usr/bin:/bin.
type integTest struct {
	t    *testing.T
	sh   *Shell
	home string
	bin  string
	log  string // fake tools append one line per call here
}

func newIntegTest(t *testing.T) *integTest {
	t.Helper()
	sh := New()
	sh.Init()
	for _, n := range sh.VarNames() {
		for _, p := range integScrub {
			if strings.HasPrefix(n, p) {
				sh.UnsetVar(n, setGlobal)
				break
			}
		}
	}
	home := t.TempDir()
	it := &integTest{t: t, sh: sh, home: home, bin: filepath.Join(home, "bin"), log: filepath.Join(home, "log")}
	if err := os.Mkdir(it.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	it.export("HOME", home)
	it.export("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	it.export("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	it.export("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	it.export("_ZO_DATA_DIR", filepath.Join(home, "zoxide"))
	it.export("PATH", it.bin+":/usr/bin:/bin")
	it.export("LOG", it.log)
	// Hooks write to the shell's stderr; keep it out of the test output.
	f, err := os.Create(filepath.Join(home, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	sh.Stderr = f
	return it
}

func (it *integTest) export(name, val string) {
	it.sh.SetVar(name, []string{val}, setOpts{scope: setGlobal, export: 1})
}

// tool installs a fake executable that logs "NAME [arg]..." and then runs
// body (sh code).
func (it *integTest) tool(name, body string) {
	it.t.Helper()
	script := "#!/bin/sh\n" +
		`{ printf '%s' "${0##*/}"; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >> "$LOG"` + "\n" +
		body + "\n"
	if err := os.WriteFile(filepath.Join(it.bin, name), []byte(script), 0o755); err != nil {
		it.t.Fatal(err)
	}
	it.sh.shared.paths.clear() // forget that it was missing
}

// run runs src in the test shell.
func (it *integTest) run(src string) (string, string, int) {
	it.t.Helper()
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	io := &IO{In: &Stream{R: strings.NewReader("")}, Out: &Stream{W: out}, Err: &Stream{W: errb}}
	st := it.sh.RunString(src, "test", io)
	return out.String(), errb.String(), st
}

// mustRun runs src and fails the test unless it succeeds.
func (it *integTest) mustRun(src string) string {
	it.t.Helper()
	out, errs, st := it.run(src)
	if st != 0 {
		it.t.Fatalf("%q: status %d, stderr %q", src, st, errs)
	}
	return out
}

// calls returns the logged tool invocations and clears the log.
func (it *integTest) calls() []string {
	it.t.Helper()
	if !it.sh.WaitIntegrations(10 * time.Second) {
		it.t.Fatal("background work did not finish")
	}
	data, err := os.ReadFile(it.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		it.t.Fatal(err)
	}
	os.Remove(it.log)
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// file reads a file the fake tools wrote ("" if missing).
func (it *integTest) file(name string) string {
	data, _ := os.ReadFile(filepath.Join(it.home, name))
	return string(data)
}

// write creates a file under the test's home.
func (it *integTest) write(name, content string) string {
	it.t.Helper()
	p := filepath.Join(it.home, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		it.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		it.t.Fatal(err)
	}
	return p
}

// mkdir creates directories under the test's home and returns the path
// of the first.
func (it *integTest) mkdir(names ...string) string {
	it.t.Helper()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(it.home, n), 0o755); err != nil {
			it.t.Fatal(err)
		}
	}
	return filepath.Join(it.home, names[0])
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// integEditor is a line editor stand-in that records what it is asked.
type integEditor struct {
	buf    string
	cursor int
	binds  []string // "mode key command"
	queued []string
}

func (e *integEditor) Buffer() (string, int)        { return e.buf, e.cursor }
func (e *integEditor) SetBuffer(b string, c int)    { e.buf, e.cursor = b, c }
func (e *integEditor) QueueFunctions(n ...string)   { e.queued = append(e.queued, n...) }
func (e *integEditor) Unbind(mode, key string) bool { return false }
func (e *integEditor) Bindings() []string           { return e.binds }
func (e *integEditor) SetKeymap(name string)        {}
func (e *integEditor) Keymap() string               { return "default" }
func (e *integEditor) Repaint()                     {}
func (e *integEditor) PagerActive() bool            { return false }
func (e *integEditor) Bind(mode, key, command, setsMode string) error {
	e.binds = append(e.binds, mode+" "+key+" "+command)
	return nil
}

// integHistory is a history stand-in, newest entry first.
type integHistory struct {
	entries []string
	deleted []string
}

func (h *integHistory) Recent(n int) []string {
	if n > 0 && n < len(h.entries) {
		return h.entries[:n]
	}
	return h.entries
}
func (h *integHistory) Search(q string, prefix bool) []string { return nil }
func (h *integHistory) Delete(cmd string) int {
	h.deleted = append(h.deleted, cmd)
	return 1
}
func (h *integHistory) Clear()                          {}
func (h *integHistory) Import(kind string) (int, error) { return 0, nil }

// realTool puts the directory of an installed tool on the test shell's
// PATH, or skips the test. Tests that use the real tools keep their data
// and configuration in temp dirs.
func (it *integTest) realTool(name string) {
	it.t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		it.t.Skipf("%s is not installed", name)
	}
	it.export("PATH", it.bin+":"+filepath.Dir(p)+":/usr/bin:/bin")
}
