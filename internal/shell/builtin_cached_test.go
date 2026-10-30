package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// cacheTest is a shell whose HOME and XDG directories are temp dirs, with
// a bin directory first in $PATH for fake tools that count their runs.
type cacheTest struct {
	t    *testing.T
	sh   *Shell
	home string
	bin  string
	log  string
}

func newCacheTest(t *testing.T) *cacheTest {
	t.Helper()
	home := t.TempDir()
	t.Chdir(home) // cd changes the process directory; restore it after
	ct := &cacheTest{t: t, home: home, bin: filepath.Join(home, "bin"), log: filepath.Join(home, "log")}
	if err := os.Mkdir(ct.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	ct.sh = ct.shell()
	return ct
}

// shell makes another shell with the same home, like a second terminal.
func (ct *cacheTest) shell() *Shell {
	sh := New()
	sh.Init()
	for _, n := range sh.VarNames() {
		if strings.HasPrefix(n, "XDG_") {
			sh.UnsetVar(n, setGlobal)
		}
	}
	for k, v := range map[string]string{
		"HOME":            ct.home,
		"XDG_CACHE_HOME":  filepath.Join(ct.home, ".cache"),
		"XDG_CONFIG_HOME": filepath.Join(ct.home, ".config"),
		"XDG_DATA_HOME":   filepath.Join(ct.home, ".local", "share"),
		"PATH":            ct.bin + ":/usr/bin:/bin",
		"LOG":             ct.log,
	} {
		sh.SetVar(k, []string{v}, setOpts{scope: setGlobal, export: 1})
	}
	return sh
}

// tool installs a fake program that logs its name and runs body (sh).
func (ct *cacheTest) tool(name, body string) string {
	ct.t.Helper()
	p := filepath.Join(ct.bin, name)
	script := "#!/bin/sh\necho " + name + " >> \"$LOG\"\n" + body + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		ct.t.Fatal(err)
	}
	ct.sh.shared.paths.clear()
	return p
}

func runCachedIn(sh *Shell, src, stdin string) (string, string, int) {
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	// No reader unless there is input: commands would share it.
	in := &Stream{}
	if stdin != "" {
		in = &Stream{R: strings.NewReader(stdin)}
	}
	st := sh.RunString(src, "test", &IO{In: in, Out: &Stream{W: out}, Err: &Stream{W: errb}})
	return out.String(), errb.String(), st
}

func (ct *cacheTest) run(src string) (string, string, int) {
	ct.t.Helper()
	return runCachedIn(ct.sh, src, "")
}

// expect runs src and checks its output, status and the number of tool
// runs so far.
func (ct *cacheTest) expect(src, out string, status, runs int) {
	ct.t.Helper()
	got, errs, st := ct.run(src)
	if got != out || st != status {
		ct.t.Errorf("%s: got %q status %d (stderr %q), want %q status %d", src, got, st, errs, out, status)
	}
	if n := ct.runs(); n != runs {
		ct.t.Errorf("%s: the tool has run %d times, want %d", src, n, runs)
	}
}

func (ct *cacheTest) runs() int {
	data, err := os.ReadFile(ct.log)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		ct.t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func (ct *cacheTest) entries() []string {
	ct.t.Helper()
	des, err := os.ReadDir(filepath.Join(ct.home, ".cache", "ash", "cached"))
	if err != nil && !os.IsNotExist(err) {
		ct.t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	return names
}

func TestCachedHitAndMiss(t *testing.T) {
	ct := newCacheTest(t)
	ct.tool("greet", `echo "hello $1"`)
	ct.expect("cached greet a", "hello a\n", 0, 1)
	ct.expect("cached greet a", "hello a\n", 0, 1)
	ct.expect("cached -- greet a", "hello a\n", 0, 1)
	ct.expect("cached greet b", "hello b\n", 0, 2)
	ct.expect("echo (cached greet a)!", "hello a!\n", 0, 2)
	if n := len(ct.entries()); n != 2 {
		t.Errorf("%d entries, want 2: %q", n, ct.entries())
	}

	// the exit status is kept with the output
	ct.tool("fail", "echo partial; echo oops >&2; exit 3")
	_, errs, _ := ct.run("cached fail")
	if errs != "oops\n" {
		t.Errorf("stderr should pass through: %q", errs)
	}
	ct.expect("cached fail", "partial\n", 3, 3)
	ct.expect("cached fail; echo $status", "partial\n3\n", 0, 3)

	// empty output is output too
	ct.tool("quiet", "true")
	ct.expect("cached quiet", "", 0, 4)
	ct.expect("cached quiet", "", 0, 4)
}

func TestCachedExpiry(t *testing.T) {
	ct := newCacheTest(t)
	ct.tool("now", "echo tick")
	ct.expect("cached --ttl 1h now", "tick\n", 0, 1)
	ct.expect("cached --ttl=1h now", "tick\n", 0, 1)
	ct.expect("cached --ttl 7d now", "tick\n", 0, 1)
	// The TTL is checked when reading: anything is older than 1ns.
	ct.expect("cached --ttl 1ns now", "tick\n", 0, 2)
	ct.expect("cached --ttl 0s now", "tick\n", 0, 3)
	ct.expect("cached now", "tick\n", 0, 3)

	// an entry from the future (the clock was turned back) is stale
	es, err := cacheEntries(filepath.Join(ct.home, ".cache", "ash", "cached"), nil)
	if err != nil || len(es) != 1 {
		t.Fatalf("entries: %v %v", es, err)
	}
	es[0].created = time.Now().Add(time.Hour)
	if err := writeCacheEntry(filepath.Dir(es[0].path), es[0].path, es[0].cacheEntry); err != nil {
		t.Fatal(err)
	}
	ct.expect("cached now", "tick\n", 0, 4)
}

func TestCachedWatch(t *testing.T) {
	ct := newCacheTest(t)
	ct.tool("conf", "echo configured")
	w1 := filepath.Join(ct.home, "a.conf")
	w2 := filepath.Join(ct.home, "b.conf")
	os.WriteFile(w1, []byte("x=1\n"), 0o644)
	cmd := "cached --watch " + w1 + " --watch=" + w2 + " conf"
	ct.expect(cmd, "configured\n", 0, 1)
	ct.expect(cmd, "configured\n", 0, 1)
	// the same files in another order
	ct.expect("cached --watch "+w2+" --watch "+w1+" conf", "configured\n", 0, 1)
	// relative to the current directory
	ct.expect("cd "+ct.home+"; cached --watch a.conf --watch b.conf conf", "configured\n", 0, 1)

	// written: new size
	os.WriteFile(w1, []byte("x=12\n"), 0o644)
	ct.expect(cmd, "configured\n", 0, 2)
	ct.expect(cmd, "configured\n", 0, 2)
	// touched: same size, new mtime
	later := time.Now().Add(time.Minute)
	os.Chtimes(w1, later, later)
	ct.expect(cmd, "configured\n", 0, 3)
	// a missing file that appears
	os.WriteFile(w2, nil, 0o644)
	ct.expect(cmd, "configured\n", 0, 4)
	// and goes away
	os.Remove(w2)
	ct.expect(cmd, "configured\n", 0, 5)
	ct.expect(cmd, "configured\n", 0, 5)
	// watching fewer files is a different question
	ct.expect("cached --watch "+w1+" conf", "configured\n", 0, 6)
	ct.expect("cached conf", "configured\n", 0, 7)
}

func TestCachedExecutableChanges(t *testing.T) {
	ct := newCacheTest(t)
	p := ct.tool("ver", "echo v1")
	ct.expect("cached ver", "v1\n", 0, 1)
	ct.expect("cached ver", "v1\n", 0, 1)
	ct.tool("ver", "echo v2.0")
	ct.expect("cached ver", "v2.0\n", 0, 2)
	// reinstalled with the same size: the mtime tells
	ct.tool("ver", "echo v3.0")
	later := time.Now().Add(time.Minute)
	os.Chtimes(p, later, later)
	ct.expect("cached ver", "v3.0\n", 0, 3)
	ct.expect("cached ver", "v3.0\n", 0, 3)

	// a program elsewhere in a new $PATH is a new entry
	other := filepath.Join(ct.home, "other")
	os.Mkdir(other, 0o755)
	ct.expect("set -gx PATH "+other+" $PATH; cached ver", "v3.0\n", 0, 4)
	ct.expect("cached ver", "v3.0\n", 0, 4)
	if err := os.WriteFile(filepath.Join(other, "ver"), []byte("#!/bin/sh\necho ver >> \"$LOG\"\necho other\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ct.sh.shared.paths.clear()
	ct.expect("cached ver", "other\n", 0, 5)
}

func TestCachedDir(t *testing.T) {
	ct := newCacheTest(t)
	ct.tool("here", "pwd")
	d1, d2 := filepath.Join(ct.home, "d1"), filepath.Join(ct.home, "d2")
	os.Mkdir(d1, 0o755)
	os.Mkdir(d2, 0o755)
	ct.expect("cd "+d1+"; cached --dir here", d1+"\n", 0, 1)
	ct.expect("cd "+d2+"; cached --dir here", d2+"\n", 0, 2)
	ct.expect("cd "+d1+"; cached --dir here", d1+"\n", 0, 2)
	// without --dir one entry serves every directory
	ct.expect("cd "+d1+"; cached here", d1+"\n", 0, 3)
	ct.expect("cd "+d2+"; cached here", d1+"\n", 0, 3)
}

func TestCachedListAndClear(t *testing.T) {
	ct := newCacheTest(t)
	ct.tool("one", "echo 1")
	ct.tool("two", "echo 22; exit 1")
	ct.run("cached one; cached two x; cached --dir --ttl 1ns one")
	out, _, st := ct.run("cached --list")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if st != 0 || len(lines) != 3 {
		t.Fatalf("list: status %d\n%s", st, out)
	}
	for _, want := range []string{
		`^0s +2B  one$`,
		`^0s +3B  two x  \(exit 1\)$`,
		`^0s +2B  one  \(in ` + strings.ReplaceAll(ct.sh.Dir(), ".", `\.`) + `, expired\)$`,
	} {
		if !matchesAny(t, want, lines) {
			t.Errorf("no line matches %s in\n%s", want, out)
		}
	}
	out, _, _ = ct.run("cached --list two x")
	if !strings.HasSuffix(out, "two x  (exit 1)\n") || strings.Count(out, "\n") != 1 {
		t.Errorf("list two x:\n%s", out)
	}

	// clearing one command removes it in every directory
	ct.expect("cached --clear one", "", 0, 3)
	out, _, _ = ct.run("cached --list")
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "two x") {
		t.Errorf("after clearing one:\n%s", out)
	}
	ct.expect("cached one", "1\n", 0, 4)
	ct.expect("cached --clear", "", 0, 4)
	if es := ct.entries(); len(es) != 0 {
		t.Errorf("left after --clear: %q", es)
	}
	ct.expect("cached --list", "", 0, 4)
	ct.expect("cached one", "1\n", 0, 5)
	// clearing nothing is fine
	ct.expect("cached --clear nothing here", "", 0, 5)
}

func matchesAny(t *testing.T, re string, lines []string) bool {
	t.Helper()
	r := regexp.MustCompile(re)
	for _, l := range lines {
		if r.MatchString(l) {
			return true
		}
	}
	return false
}

func TestCachedStdin(t *testing.T) {
	ct := newCacheTest(t)
	ct.tool("upper", "tr a-z A-Z")
	ct.expect("echo hi | cached upper", "HI\n", 0, 1)
	// a hit doesn't read stdin, and gives the kept output
	ct.expect("echo other | cached upper", "HI\n", 0, 1)
	out, _, _ := runCachedIn(ct.sh, "cached upper; read -l rest; echo $rest", "unread\n")
	if out != "HI\nunread\n" {
		t.Errorf("stdin was read on a hit: %q", out)
	}
}

func TestCachedNotKept(t *testing.T) {
	ct := newCacheTest(t)
	_, errs, st := ct.run("cached no-such-tool")
	if st != 127 || !strings.Contains(errs, "no-such-tool: command not found") {
		t.Errorf("missing command: status %d, %q", st, errs)
	}
	// killed by a signal
	ct.tool("dies", "echo half; kill -9 $$")
	ct.expect("cached dies", "half\n", 137, 1)
	ct.expect("cached dies", "half\n", 137, 2)
	// can't run
	p := ct.tool("noexec", "echo never")
	os.Chmod(p, 0o644)
	ct.sh.shared.paths.clear()
	if _, _, st := ct.run("cached noexec"); st != 127 && st != 126 {
		t.Errorf("not executable: status %d", st)
	}
	// more output than is kept: passed through, not cached
	defer func(n int) { cachedMaxOutput = n }(cachedMaxOutput)
	cachedMaxOutput = 10
	ct.tool("big", "echo 0123456789abcdef")
	ct.expect("cached big", "0123456789abcdef\n", 0, 3)
	ct.expect("cached big", "0123456789abcdef\n", 0, 4)
	// exit and return can't be replayed
	ct.expect("function bye; echo by; exit 4; end; cached bye; echo not reached", "by\n", 4, 4)
	ct.sh.ResetControlAll()
	if es := ct.entries(); len(es) != 0 {
		t.Errorf("entries: %q", es)
	}
}

// A function that writes forever must stop when its reader goes away, as
// it does without cached.
func TestCachedBrokenPipe(t *testing.T) {
	ct := newCacheTest(t)
	done := make(chan string, 1)
	go func() {
		out, _, _ := ct.run("function forever; while true; echo y; end; end; cached forever | head -n 1")
		done <- out
	}()
	select {
	case out := <-done:
		if out != "y\n" {
			t.Errorf("output %q", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cached kept running after head exited")
	}
	if es := ct.entries(); len(es) != 0 {
		t.Errorf("a run cut short was cached: %q", es)
	}
}

func TestCachedFunctionsAndBuiltins(t *testing.T) {
	ct := newCacheTest(t)
	ct.run("set -g n 0; function count; set -g n (math $n + 1); echo call $n; end")
	ct.expect("cached count", "call 1\n", 0, 0)
	ct.expect("cached count; echo $n", "call 1\n1\n", 0, 0)
	// a new definition is a new entry
	ct.run("function count; set -g n (math $n + 10); echo call $n; end")
	ct.expect("cached count; echo $n", "call 11\n11\n", 0, 0)
	ct.expect("cached printf '%s-' a b", "a-b-", 0, 0)
	ct.expect("cached printf '%s-' a b", "a-b-", 0, 0)

	// a job the function leaves running writes after cached returned: it
	// still reaches stdout, but isn't part of the entry
	ct.run("function bg; begin; sleep 0.05; echo late; end &; echo now; end")
	ct.expect("cached bg; wait", "now\nlate\n", 0, 0)
	ct.expect("cached bg; wait", "now\n", 0, 0)
}

func TestCachedBadEntries(t *testing.T) {
	ct := newCacheTest(t)
	ct.tool("gen", "echo fresh")
	ct.expect("cached gen", "fresh\n", 0, 1)
	es := ct.entries()
	if len(es) != 1 {
		t.Fatalf("entries: %q", es)
	}
	file := filepath.Join(ct.home, ".cache", "ash", "cached", es[0])
	good, _ := os.ReadFile(file)
	for i, bad := range []string{
		"",
		"garbage",
		strings.Replace(string(good), "ash-cached 1", "ash-cached 9", 1),
		string(good[:len(good)-2]), // cut off
		strings.Replace(string(good), "status 0", "status x", 1),
		strings.Replace(string(good), "\n\n", "\n", 1),
	} {
		os.WriteFile(file, []byte(bad), 0o600)
		ct.expect("cached gen", "fresh\n", 0, 2+i)
		if data, _ := os.ReadFile(file); string(data) == bad {
			t.Errorf("bad entry %d was not replaced", i)
		}
	}
	// the cache directory can't be written: the command still runs
	os.RemoveAll(filepath.Join(ct.home, ".cache"))
	os.WriteFile(filepath.Join(ct.home, ".cache"), nil, 0o644)
	out, errs, st := ct.run("cached gen")
	if out != "fresh\n" || st != 0 || !strings.HasPrefix(errs, "cached: ") {
		t.Errorf("unwritable cache: %q %q %d", out, errs, st)
	}
}

// Shells starting at the same time all write the entry. Readers must only
// ever see whole entries, and no temporary files may be left behind.
func TestCachedConcurrentShells(t *testing.T) {
	ct := newCacheTest(t)
	ct.tool("big", "seq 1 20000")
	want, _, _ := ct.run("seq 1 20000")
	if len(want) < 100000 {
		t.Fatalf("seq gave %d bytes", len(want))
	}
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sh := ct.shell()
			// writers always miss; readers mostly hit
			cmd := "cached big"
			if i%2 == 0 {
				cmd = "cached --ttl 1ns big"
			}
			for j := 0; j < 5; j++ {
				out, e, st := runCachedIn(sh, cmd, "")
				if out != want || st != 0 {
					errs <- fmt.Sprintf("shell %d run %d: %d bytes, status %d, %q", i, j, len(out), st, e)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if es := ct.entries(); len(es) != 1 || strings.HasPrefix(es[0], ".") {
		t.Errorf("entries left: %q", es)
	}
}

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90s":    90 * time.Second,
		"1h30m":  90 * time.Minute,
		"7d":     7 * 24 * time.Hour,
		"1.5d":   36 * time.Hour,
		"1d12h":  36 * time.Hour,
		"2w":     14 * 24 * time.Hour,
		"1w2d3h": 9*24*time.Hour + 3*time.Hour,
		"0":      0,
	} {
		got, err := parseTTL(in)
		if err != nil || got != want {
			t.Errorf("parseTTL(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "d", "abc", "-1h", "-2d", "1h2d", "7", "3d2w", "1dd"} {
		if d, err := parseTTL(in); err == nil {
			t.Errorf("parseTTL(%q) = %v, want an error", in, d)
		}
	}
}

func TestCachedUsage(t *testing.T) {
	ct := newCacheTest(t)
	for src, status := range map[string]int{
		"cached":                 2,
		"cached --ttl":           2,
		"cached --ttl soon true": 2,
		"cached --watch":         2,
		"cached --bogus true":    2,
		"cached --dir":           2,
	} {
		if _, errs, st := ct.run(src); st != status || errs == "" {
			t.Errorf("%s: status %d, stderr %q", src, st, errs)
		}
	}
	out, _, st := ct.run("cached --help")
	if st != 0 || !strings.HasPrefix(out, "usage: cached") {
		t.Errorf("--help: %d %q", st, out)
	}
	if builtinHelp["cached"] == "" {
		t.Error("no one-line help")
	}
}

func TestCacheEntryRoundTrip(t *testing.T) {
	e := &cacheEntry{
		created: time.Unix(0, 1730000000123456789),
		ttl:     time.Hour,
		status:  2,
		command: []string{"tool", "with space", "quote\"and\nnewline", ""},
		dir:     "/some dir",
		ident:   `exe "/bin/tool" 1 2`,
		watch:   []watchStat{{"/a b", 5, 6}, {"/missing", -1, -1}},
		out:     []byte("line\n\nafter a blank line\x00binary"),
	}
	got, err := decodeCacheEntry(e.encode())
	if err != nil {
		t.Fatal(err)
	}
	if got.created != e.created || got.ttl != e.ttl || got.status != e.status || !equalStrings(got.command, e.command) ||
		got.dir != e.dir || got.ident != e.ident || string(got.out) != string(e.out) ||
		len(got.watch) != 2 || got.watch[0] != e.watch[0] || got.watch[1] != e.watch[1] {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, e)
	}
}

// BenchmarkCachedHit is the cost of a cache hit, what a config line like
// `eval (cached brew shellenv)` costs at every start after the first.
func BenchmarkCachedHit(b *testing.B) {
	home := b.TempDir()
	bin := filepath.Join(home, "bin")
	os.Mkdir(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "tool"), []byte("#!/bin/sh\nseq 1 100\n"), 0o755)
	sh := New()
	sh.Init()
	sh.SetVar("XDG_CACHE_HOME", []string{filepath.Join(home, ".cache")}, setOpts{scope: setGlobal, export: 1})
	sh.SetVar("PATH", []string{bin + ":/usr/bin:/bin"}, setOpts{scope: setGlobal, export: 1})
	out := &lockedBuffer{}
	io := &IO{In: &Stream{}, Out: &Stream{W: out}, Err: &Stream{W: out}}
	args := []string{"cached", "tool", "--some-flag"}
	if st := bCached(sh, io, args); st != 0 {
		b.Fatalf("status %d: %s", st, out.String())
	}
	b.Run("hit", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			out.buf.Reset()
			bCached(sh, io, args)
		}
		if !strings.HasPrefix(out.String(), "1\n2\n") {
			b.Fatalf("output %q", out.String())
		}
	})
	b.Run("run", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sh.dispatch([]string{"tool", "--some-flag"}, io, nil, false, nil)
		}
	})
}
