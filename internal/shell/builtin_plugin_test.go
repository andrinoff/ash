package shell

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/andrinoff/ash/internal/plugin/plugintest"
)

// The test binary doubles as the plugin (see plugintest).
func TestPluginHelperProcess(t *testing.T) { plugintest.Main() }

// pluginTest is a shell with its config, cache and data in a temp HOME,
// and the command line that starts the fake plugin.
type pluginTest struct {
	t    *testing.T
	sh   *Shell
	home string
	cmd  string
	seen PluginUpdate // everything apply has applied
}

func newPluginTest(t *testing.T) *pluginTest {
	t.Helper()
	home := t.TempDir()
	t.Chdir(home) // cd changes the process directory; restore it
	sh := New()
	sh.Init()
	pt := &pluginTest{t: t, sh: sh, home: home}
	for k, v := range map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
	} {
		sh.SetVar(k, []string{v}, setOpts{scope: setGlobal, export: 1})
	}
	path, args := plugintest.Command()
	quoted := []string{Quote(path)}
	for _, a := range args[1:] {
		quoted = append(quoted, Quote(a))
	}
	pt.cmd = strings.Join(quoted, " ")
	t.Cleanup(sh.ClosePlugins)
	return pt
}

// run runs src and returns its stdout, stderr and status. stdin is the
// shell's own (not piped), unless the code pipes into a command.
func (pt *pluginTest) run(src string) (string, string, int) {
	pt.t.Helper()
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	io := &IO{In: &Stream{R: strings.NewReader(""), Inherited: true}, Out: &Stream{W: out}, Err: &Stream{W: errb}}
	st := pt.sh.RunString(src, "", io)
	return out.String(), errb.String(), st
}

func (pt *pluginTest) ok(src string) string {
	pt.t.Helper()
	out, errs, st := pt.run(src)
	if st != 0 {
		pt.t.Fatalf("%s: status %d, stderr %q", src, st, errs)
	}
	return out
}

// load loads the fake plugin. In basic mode it echoes the prompt event it
// gets with its hello; that is consumed here.
func (pt *pluginTest) load(name, mode string) {
	pt.t.Helper()
	pt.ok("set -gx " + plugintest.EnvVar + " " + mode + "; plugin load -w " + name + " " + pt.cmd)
	if mode == "basic" {
		pt.apply(`event {"event":"prompt"`)
		pt.seen = PluginUpdate{}
	}
}

// apply applies plugin actions until there has been a message containing
// want, and returns everything applied so far in the test.
func (pt *pluginTest) apply(want string) PluginUpdate {
	pt.t.Helper()
	all := &pt.seen
	deadline := time.Now().Add(5 * time.Second)
	for {
		u := pt.sh.ApplyPluginActions()
		all.Messages = append(all.Messages, u.Messages...)
		all.Redraw = all.Redraw || u.Redraw
		all.DirChanged = all.DirChanged || u.DirChanged
		if u.HasBuffer {
			all.Buffer, all.HasBuffer = u.Buffer, true
		}
		for _, m := range all.Messages {
			if strings.Contains(m, want) {
				return *all
			}
		}
		if time.Now().After(deadline) {
			pt.t.Fatalf("no message with %q; got %q", want, all.Messages)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPluginBuiltin(t *testing.T) {
	pt := newPluginTest(t)
	if out := pt.ok("plugin list"); out != "" {
		t.Fatalf("no plugins yet, got %q", out)
	}
	pt.load("basic", "basic")
	out := pt.ok("plugin list")
	f := strings.Fields(out)
	if len(f) < 4 || f[0] != "basic" || f[2] != "running" || f[3] != "prompt" || !strings.HasSuffix(out, "prompt preexec postexec cd exit\n") {
		t.Fatalf("plugin list: %q", out)
	}
	if pid, err := strconv.Atoi(f[1]); err != nil || pid <= 0 {
		t.Fatalf("pid: %q", f[1])
	}
	out = pt.ok("plugin list -v")
	for _, want := range []string{
		"    command   greet  (the greet command)\n",
		"    completes fruit slowfruit greet where\n",
		"    runs      " + pt.cmd + "\n",
		"    log       " + filepath.Join(pt.home, ".cache", "ash", "plugins", "basic.log") + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plugin list -v lacks %q:\n%s", want, out)
		}
	}
	// commands look like builtins
	if out := pt.ok("type greet; builtin -q greet; and echo yes; command -v greet"); out != "greet is a builtin\nyes\ngreet\n" {
		t.Fatalf("got %q", out)
	}
	if !pt.sh.HasCommand("greet") || pt.sh.CommandKind("greet") != "builtin" {
		t.Fatal("HasCommand/CommandKind don't know plugin commands")
	}
	cmds := pt.sh.PluginCommands()
	if len(cmds) == 0 || cmds[0] != (PluginCommand{Name: "act", Description: "the act command", Plugin: "basic"}) {
		t.Fatalf("PluginCommands: %+v", cmds)
	}
	for _, c := range cmds {
		if c.Name == "pwd" {
			t.Fatal("pwd is a builtin; the plugin can't have it")
		}
	}
	pt.ok("plugin unload basic")
	if out := pt.ok("plugin list"); out != "" {
		t.Fatalf("after unload: %q", out)
	}
	if _, errs, st := pt.run("greet"); st != 127 || !strings.Contains(errs, "greet") {
		t.Fatalf("after unload, greet: %d %q", st, errs)
	}
}

func TestPluginBuiltinErrors(t *testing.T) {
	pt := newPluginTest(t)
	cases := []struct {
		src, err string
		status   int
	}{
		{"plugin nope", "unknown subcommand 'nope'", 2},
		{"plugin load", "usage", 2},
		{"plugin load x", "usage", 2},
		{"plugin load -x a b", "unknown option -x", 2},
		{"plugin load ../x ls", "isn't a valid name", 2},
		{"plugin load x no-such-command-here", "no-such-command-here: command not found", 127},
		{"plugin load -w x /bin/sh -c 'echo broken >&2; exit 3'", "x stopped before it was ready (exit status 3); its log is ", 1},
		{"plugin load -w q /bin/sh -c 'sleep 5'", "q didn't say hello within", 1},
		{"plugin unload nope", "no plugin named 'nope'", 1},
		{"plugin reload nope", "no plugin named 'nope'", 1},
		{"plugin list -x", "unknown option -x", 2},
	}
	old := pluginHelloTimeout
	pluginHelloTimeout = 200 * time.Millisecond
	defer func() { pluginHelloTimeout = old }()
	for _, c := range cases {
		_, errs, st := pt.run(c.src)
		if st != c.status || !strings.Contains(errs, c.err) {
			t.Errorf("%s: got %d %q, want %d and %q", c.src, st, errs, c.status, c.err)
		}
	}
	// the -w error was the report: no second one at the prompt
	if u := pt.sh.ApplyPluginActions(); len(u.Messages) != 0 {
		t.Fatalf("reported twice: %q", u.Messages)
	}
	pt.load("basic", "basic")
	if _, errs, st := pt.run("plugin load basic " + pt.cmd); st != 1 || !strings.Contains(errs, "already loaded") {
		t.Fatalf("loading twice: %d %q", st, errs)
	}
	if out := pt.ok("plugin --help"); !strings.HasPrefix(out, "usage: plugin load") {
		t.Fatalf("help: %q", out)
	}
}

func TestPluginCommands(t *testing.T) {
	pt := newPluginTest(t)
	pt.load("basic", "basic")
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	cases := []struct {
		src, out, err string
		status        int
	}{
		{"greet a 'b c'", "hello a b c\n", "", 0},
		{"cat", "(no stdin)\n", "", 0},
		{"echo piped | cat", "piped\n", "", 0},
		{"printf 'x\\ny' | cat | tr a-z A-Z", "X\nY", "", 0},
		{"fail", "", "it failed\n", 3},
		{"fail; echo $status", "3\n", "it failed\n", 0},
		{"cd " + dir + "; where", dir + "\n", "", 0},
		{"set -l x (greet sub); echo $x", "hello sub\n", "", 0},
		// a builtin wins over a plugin's command
		{"cd " + dir + "; pwd", dir + "\n", "", 0},
	}
	for _, c := range cases {
		out, errs, st := pt.run(c.src)
		if out != c.out || errs != c.err || st != c.status {
			t.Errorf("%s: got (%q, %q, %d), want (%q, %q, %d)", c.src, out, errs, st, c.out, c.err, c.status)
		}
	}
	// and a function wins over both
	if out := pt.ok("function greet; echo mine; end; greet; functions -e greet; greet x"); out != "mine\nhello x\n" {
		t.Fatalf("got %q", out)
	}
	b, _ := os.ReadFile(filepath.Join(pt.home, ".cache", "ash", "plugins", "basic.log"))
	if !strings.Contains(string(b), "pwd is a builtin") {
		t.Fatalf("log: %s", b)
	}
}

func TestPluginCommandTimeout(t *testing.T) {
	pt := newPluginTest(t)
	pt.load("basic", "basic")
	old := pluginRunTimeout
	pluginRunTimeout = 150 * time.Millisecond
	defer func() { pluginRunTimeout = old }()
	start := time.Now()
	_, errs, st := pt.run("slow")
	if st != 124 || !strings.Contains(errs, "slow: plugin basic didn't answer within 150ms") {
		t.Fatalf("got %d %q", st, errs)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestPluginCommandInterrupt(t *testing.T) {
	pt := newPluginTest(t)
	pt.load("basic", "basic")
	go func() {
		time.Sleep(100 * time.Millisecond)
		pt.sh.Interrupt()
	}()
	start := time.Now()
	_, _, st := pt.run("slow")
	pt.sh.ClearInterrupt()
	if st != 130 {
		t.Fatalf("status %d", st)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("ctrl-c took %v to stop the command", d)
	}
	// the plugin is told, and still works
	pt.apply("cancel ")
	if out := pt.ok("greet again"); out != "hello again\n" {
		t.Fatalf("got %q", out)
	}
}

func TestPluginActions(t *testing.T) {
	pt := newPluginTest(t)
	pt.load("basic", "basic")
	sh := pt.sh
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)

	pt.ok("act set foo a 'b c'; act export bar 1; act segment git 'main +2'; act cd " + dir + "; act buffer 'git push'; act message 'hello there'")
	u := sh.ApplyPluginActions()
	if !reflect.DeepEqual(sh.GetList("foo"), []string{"a", "b c"}) {
		t.Errorf("foo = %q", sh.GetList("foo"))
	}
	if !strings.Contains(strings.Join(sh.Environ(), "\n"), "\nbar=1") && !strings.HasPrefix(strings.Join(sh.Environ(), "\n"), "bar=1") {
		t.Error("bar isn't exported")
	}
	for _, kv := range sh.Environ() {
		if strings.HasPrefix(kv, "foo=") {
			t.Error("foo is exported")
		}
	}
	if got := sh.Get("ash_plugin_git"); got != "main +2" {
		t.Errorf("segment: %q", got)
	}
	if segs := sh.PluginSegments(); !reflect.DeepEqual(segs, []PluginSegment{{Name: "git", Text: "main +2"}}) {
		t.Errorf("segments: %+v", segs)
	}
	if sh.Dir() != dir || sh.Get("PWD") != dir {
		t.Errorf("dir = %s, PWD = %s", sh.Dir(), sh.Get("PWD"))
	}
	if !u.Redraw || !u.DirChanged || !u.HasBuffer || u.Buffer != "git push" || !reflect.DeepEqual(u.Messages, []string{"hello there"}) {
		t.Errorf("update: %+v", u)
	}

	// the same value again changes nothing, so the prompt isn't redrawn
	pt.ok("act segment git 'main +2'; act set foo a 'b c'")
	if u := sh.ApplyPluginActions(); u.Redraw {
		t.Errorf("redraw for nothing: %+v", u)
	}

	pt.ok("act unset foo; act segment git ''")
	sh.ApplyPluginActions()
	if sh.IsSet("foo") || sh.IsSet("ash_plugin_git") || sh.PluginSegments() != nil {
		t.Errorf("unset: foo=%q segment=%q", sh.Get("foo"), sh.Get("ash_plugin_git"))
	}

	// bad requests are logged, and a failed cd is shown
	pt.ok("act set 'no good' x; act set status 1; act cd /nonexistent/dir; act segment 'bad-name' x")
	u = sh.ApplyPluginActions()
	if len(u.Messages) != 1 || !strings.Contains(u.Messages[0], "ash: plugin basic: cd: /nonexistent/dir: no such directory") || u.DirChanged {
		t.Errorf("failed cd: %+v", u)
	}
	b, _ := os.ReadFile(filepath.Join(pt.home, ".cache", "ash", "plugins", "basic.log"))
	for _, want := range []string{"set: no good: invalid variable name", "set: status: read-only variable", `segment "bad-name"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("log lacks %q:\n%s", want, b)
		}
	}

	// a relative cd starts from the current directory, and ~ works
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	pt.ok("act cd sub")
	sh.ApplyPluginActions()
	if sh.Dir() != filepath.Join(dir, "sub") {
		t.Errorf("relative cd went to %s", sh.Dir())
	}
	pt.ok("act cd '~'")
	sh.ApplyPluginActions()
	if sh.Dir() != pt.home {
		t.Errorf("cd ~ went to %s", sh.Dir())
	}
}

func TestPluginEvents(t *testing.T) {
	pt := newPluginTest(t)
	pt.load("basic", "basic")
	sh := pt.sh
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	from := sh.Dir()
	pt.ok("cd " + dir)
	pt.apply(`event {"event":"cd","from":` + strconv.Quote(from) + `,"to":` + strconv.Quote(dir) + `}`)

	sh.RunPreExec("false | true")
	sh.RunTopLevel("false | true", sh.StdIO())
	sh.SetCmdDuration(1234 * time.Millisecond)
	sh.RunPostExec("false | true", 0, 1234*time.Millisecond)
	sh.RunPrePrompt()
	pt.apply(`event {"event":"preexec","line":"false | true","cwd":` + strconv.Quote(dir) + `}`)
	pt.apply(`event {"event":"postexec","line":"false | true","status":0,"duration_ms":1234,"cwd":` + strconv.Quote(dir) + `}`)
	pt.apply(`event {"event":"prompt","cwd":` + strconv.Quote(dir) + `,"status":0,"pipestatus":[1,0],"duration_ms":1234}`)

	// subshells don't send cd events
	pt.ok("(cd /)")
	sh.RunPrePrompt()
	u := pt.apply(`event {"event":"prompt"`)
	for _, m := range u.Messages {
		if strings.Contains(m, `"to":"/"`) {
			t.Fatalf("a subshell's cd was sent: %s", m)
		}
	}

	// the exit event comes with ClosePlugins
	sh.ClosePlugins()
	if out := pt.ok("plugin list"); out != "" {
		t.Fatalf("after close: %q", out)
	}
}

func TestPluginComplete(t *testing.T) {
	pt := newPluginTest(t)
	if _, ok := pt.sh.PluginComplete("fruit", "fruit ", 6); ok {
		t.Fatal("no plugins, no completions")
	}
	pt.load("basic", "basic")
	got, ok := pt.sh.PluginComplete("fruit", "fruit ap", 8)
	want := []PluginCandidate{{"apple", "fruit"}, {"apricot", "fruit"}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v %v", got, ok)
	}
	if _, ok := pt.sh.PluginComplete("ls", "ls ", 3); ok {
		t.Fatal("nobody completes ls")
	}
	// the plugin is told where the shell is
	if got, ok := pt.sh.PluginComplete("where", "where ", 6); !ok || len(got) != 1 || got[0].Text != pt.sh.Dir() {
		t.Fatalf("where: %+v %v", got, ok)
	}
	start := time.Now()
	if _, ok := pt.sh.PluginComplete("slowfruit", "slowfruit ", 10); ok {
		t.Fatal("slowfruit answers too late")
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > time.Second {
		t.Fatalf("the completion timeout took %v", d)
	}
}

func TestPluginCrash(t *testing.T) {
	pt := newPluginTest(t)
	pt.load("basic", "basic")
	_, errs, st := pt.run("crash")
	if st != 1 || !strings.Contains(errs, "crash: plugin basic: not running (exit status 7)") {
		t.Fatalf("got %d %q", st, errs)
	}
	u := pt.apply("ash: plugin basic stopped (exit status 7); its log is ")
	time.Sleep(20 * time.Millisecond)
	n := 0
	for _, m := range append(u.Messages, pt.sh.ApplyPluginActions().Messages...) {
		if strings.Contains(m, "stopped") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("reported %d times", n)
	}
	if out := pt.ok("plugin list"); out != "basic  -       dead      exit status 7\n" {
		t.Fatalf("plugin list: %q", out)
	}
	// reload brings it back
	pt.ok("plugin reload -w basic")
	if out := pt.ok("greet back"); out != "hello back\n" {
		t.Fatalf("after reload: %q", out)
	}
}

func TestPluginReload(t *testing.T) {
	pt := newPluginTest(t)
	pt.load("basic", "basic")
	pid := func() string { return strings.Fields(pt.ok("plugin list"))[1] }
	before := pid()
	pt.ok("plugin reload -w basic")
	if after := pid(); after == before {
		t.Fatal("reload kept the process")
	}
}

func TestPluginAutoload(t *testing.T) {
	pt := newPluginTest(t)
	dir := filepath.Join(pt.home, ".config", "ash", "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path, args := plugintest.Command()
	script := "#!/bin/sh\nexec " + Quote(path) + " " + Quote(args[1]) + "\n"
	write := func(name, body string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("fake.sh", script, 0o755)
	write("notes.txt", "not a plugin", 0o644)
	write(".hidden", script, 0o755)
	write("backup.sh~", script, 0o755)
	pt.ok("set -gx " + plugintest.EnvVar + " basic")
	start := time.Now()
	pt.sh.AutoloadPlugins()
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("AutoloadPlugins took %v; it should return at once", d)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !pt.sh.HasCommand("greet") {
		if time.Now().After(deadline) {
			t.Fatalf("not loaded: %q", pt.ok("plugin list"))
		}
		time.Sleep(5 * time.Millisecond)
	}
	out := pt.ok("plugin list")
	if !strings.HasPrefix(out, "fake ") || strings.Count(out, "\n") != 1 {
		t.Fatalf("plugin list: %q", out)
	}
	// it started after the prompt was up, and hears about that prompt
	pt.apply(`event {"event":"prompt","cwd":` + strconv.Quote(pt.sh.Dir()) + `,"status":0,"pipestatus":[0],"duration_ms":0}`)
	if out := pt.ok("greet auto"); out != "hello auto\n" {
		t.Fatalf("got %q", out)
	}
	// the plugin knows its name and the shell's pid
	want := "fake\n" + strconv.Itoa(os.Getpid()) + "\n"
	if out := pt.ok("getenv ASH_PLUGIN_NAME; getenv ASH_PID"); out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestPluginEnv(t *testing.T) {
	env := pluginEnv([]string{"A=1", "ASH_PLUGIN_NAME=old", "B=2"}, "git")
	want := []string{"A=1", "B=2", "ASH_PLUGIN_NAME=git", "ASH_PID=" + strconv.Itoa(os.Getpid())}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("got %q", env)
	}
	for _, c := range []struct {
		file, name string
		ok         bool
	}{
		{"git-status.py", "git-status", true},
		{"took", "took", true},
		{"x.tar.gz", "x.tar", true},
		{".hidden", "", false},
		{"backup~", "", false},
		{"has space.sh", "", false},
	} {
		name, ok := autoloadName(c.file)
		if ok != c.ok || ok && name != c.name {
			t.Errorf("%s: got %q %v", c.file, name, ok)
		}
	}
}

func TestClosePlugins(t *testing.T) {
	pt := newPluginTest(t)
	pt.load("one", "basic")
	pt.load("two", "stubborn")
	var pids []int
	for _, l := range strings.Split(strings.TrimSpace(pt.ok("plugin list")), "\n") {
		pid, _ := strconv.Atoi(strings.Fields(l)[1])
		pids = append(pids, pid)
	}
	start := time.Now()
	pt.sh.ClosePlugins()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("ClosePlugins took %v", d)
	}
	for _, pid := range pids {
		deadline := time.Now().Add(5 * time.Second)
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				t.Fatalf("process %d is still there", pid)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if _, errs, st := pt.run("plugin load -w three " + pt.cmd); st != 1 || !strings.Contains(errs, "exiting") {
		t.Fatalf("load after close: %d %q", st, errs)
	}
}

func TestNoPluginsCostNothing(t *testing.T) {
	sh := New()
	if _, ok := sh.builtinFunc("ls"); ok {
		t.Fatal("ls isn't a builtin")
	}
	if u := sh.ApplyPluginActions(); !reflect.DeepEqual(u, PluginUpdate{}) {
		t.Fatalf("got %+v", u)
	}
	if sh.shared.plugins.mgr.Load() != nil {
		t.Fatal("looking things up must not start the plugin manager")
	}
	sh.ClosePlugins()
}

func BenchmarkBuiltinLookup(b *testing.B) {
	sh := New()
	b.Run("no-plugins", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sh.builtinFunc("ls")
		}
	})
	sh.pluginManager()
	b.Run("plugins", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sh.builtinFunc("ls")
		}
	})
}

func BenchmarkApplyPluginActionsIdle(b *testing.B) {
	sh := New()
	sh.pluginManager()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sh.ApplyPluginActions()
	}
}

// BenchmarkPluginCommand is the round trip of a plugin command: request
// out, answer back, output written.
func BenchmarkPluginCommand(b *testing.B) {
	sh := New()
	sh.Init()
	sh.SetVar("XDG_CACHE_HOME", []string{b.TempDir()}, setOpts{scope: setGlobal, export: 1})
	sh.SetVar(plugintest.EnvVar, []string{"sink"}, setOpts{scope: setGlobal, export: 1})
	path, args := plugintest.Command()
	io := &IO{In: &Stream{Inherited: true}, Out: &Stream{W: &lockedBuffer{}}, Err: &Stream{W: &lockedBuffer{}}}
	if st := sh.RunString("plugin load -w sink "+Quote(path)+" "+Quote(args[1]), "", io); st != 0 {
		b.Fatalf("load: %d", st)
	}
	b.Cleanup(sh.ClosePlugins)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if st := sh.RunString("greet x", "", io); st != 0 {
			b.Fatalf("status %d", st)
		}
	}
}
