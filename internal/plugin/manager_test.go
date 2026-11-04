package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/andrinoff/ash/internal/plugin/plugintest"
)

// The test binary doubles as the plugin (see plugintest).
func TestPluginHelperProcess(t *testing.T) { plugintest.Main() }

type harness struct {
	t       *testing.T
	m       *Manager
	dir     string
	wake    chan struct{}
	backlog []Action
}

func newHarness(t *testing.T, o Options) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{t: t, dir: dir, wake: make(chan struct{}, 1)}
	if o.LogDir == "" {
		o.LogDir = filepath.Join(dir, "logs")
	}
	o.Wake = func() {
		select {
		case h.wake <- struct{}{}:
		default:
		}
	}
	h.m = New(o)
	t.Cleanup(func() { h.m.Close(100 * time.Millisecond) })
	return h
}

func (h *harness) spec(name, mode string) Spec {
	path, args := plugintest.Command()
	return Spec{Name: name, Path: path, Args: args, Dir: h.dir, Env: append(os.Environ(), plugintest.Env(mode))}
}

// load starts a fake plugin and waits for its hello.
func (h *harness) load(name, mode string) {
	h.t.Helper()
	if err := h.m.Load(h.spec(name, mode)); err != nil {
		h.t.Fatal(err)
	}
	if err := h.m.WaitReady(name, 5*time.Second); err != nil {
		h.t.Fatal(err)
	}
}

// next waits for the first action that matches f. Others stay in the
// backlog for later calls.
func (h *harness) next(what string, f func(Action) bool) Action {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.backlog = append(h.backlog, h.m.Actions()...)
		for i, a := range h.backlog {
			if f(a) {
				h.backlog = append(h.backlog[:i:i], h.backlog[i+1:]...)
				return a
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; got %+v", what, h.backlog)
		}
		select {
		case <-h.wake:
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (h *harness) message(prefix string) string {
	h.t.Helper()
	return h.next("a message starting with "+prefix, func(a Action) bool {
		return a.Kind == ActMessage && strings.HasPrefix(a.Text, prefix)
	}).Text
}

func (h *harness) run(name string, args ...string) (Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.m.Run(ctx, name, args, h.dir, nil)
}

func (h *harness) info(name string) Info {
	for _, in := range h.m.List() {
		if in.Name == name {
			return in
		}
	}
	h.t.Fatalf("plugin %s isn't listed", name)
	return Info{}
}

func TestLoadAndList(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	in := h.info("basic")
	if in.State != Running || in.Pid <= 0 || in.Log == "" {
		t.Fatalf("got %+v", in)
	}
	if !reflect.DeepEqual(in.Events, []string{"prompt", "preexec", "postexec", "cd", "exit"}) {
		t.Fatalf("events: %v", in.Events)
	}
	if !h.m.HasCommand("greet") || h.m.HasCommand("nope") || !h.m.Completes("fruit") {
		t.Fatal("commands and completions from the hello aren't registered")
	}
	if p := h.m.CommandPlugin("greet"); p != "basic" {
		t.Fatalf("greet comes from %q", p)
	}
	cmds := h.m.Commands()
	if len(cmds) != 13 || cmds[0].Name != "act" || cmds[0].Description != "the act command" || cmds[0].Plugin != "basic" {
		t.Fatalf("commands: %+v", cmds)
	}
	if err := h.m.Load(h.spec("basic", "basic")); !errors.Is(err, ErrLoaded) {
		t.Fatalf("loading twice: %v", err)
	}
	if err := h.m.Load(h.spec("../x", "basic")); err == nil {
		t.Fatal("a bad name should be refused")
	}
	if err := h.m.Load(Spec{Name: "missing", Path: filepath.Join(h.dir, "nonexistent")}); err == nil {
		t.Fatal("a missing program should fail to load")
	}
	b, err := os.ReadFile(in.Log)
	if err != nil || !strings.Contains(string(b), "starting ") {
		t.Fatalf("log: %q %v", b, err)
	}
}

func TestEvents(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	if !h.m.Wants(EventPrompt) || !h.m.Wants(EventCd) || h.m.Wants("nope") {
		t.Fatal("Wants is wrong")
	}
	h.m.Prompt("/home/me", 1, []int{0, 1}, 1500*time.Millisecond)
	h.m.PreExec("make && ./run", "/src")
	h.m.PostExec("false", 1, 3*time.Millisecond, "/src")
	h.m.Cd("/a", "/b")
	h.m.Prompt("/b", 0, nil, 0)
	for _, want := range []string{
		`{"event":"prompt","cwd":"/home/me","status":1,"pipestatus":[0,1],"duration_ms":1500}`,
		`{"event":"preexec","line":"make && ./run","cwd":"/src"}`,
		`{"event":"postexec","line":"false","status":1,"duration_ms":3,"cwd":"/src"}`,
		`{"event":"cd","from":"/a","to":"/b"}`,
		`{"event":"prompt","cwd":"/b","status":0,"pipestatus":[0],"duration_ms":0}`,
	} {
		if got := h.message("event "); got != "event "+want {
			t.Errorf("got  %s\nwant event %s", got, want)
		}
	}
}

func TestLatePluginHearsTheLastPrompt(t *testing.T) {
	h := newHarness(t, Options{})
	h.m.Prompt("/before", 2, nil, 0) // nobody listens yet
	h.m.Prompt("/now", 1, []int{1}, 5*time.Millisecond)
	h.load("basic", "basic")
	want := `event {"event":"prompt","cwd":"/now","status":1,"pipestatus":[1],"duration_ms":5}`
	if got := h.message("event "); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// a plugin that doesn't listen to prompts doesn't get it
	h.load("spam", "spam")
	h.m.Cd("/a", "/b")
	if got := h.message("event "); !strings.Contains(got, `"event":"cd"`) {
		t.Fatalf("got %s", got)
	}
}

func TestOnlySubscribedEvents(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	if _, err := h.run("rehello"); err != nil {
		t.Fatal(err)
	}
	// the new hello only listens to prompt, and replaces the commands
	waitUntil(t, "the new hello", func() bool { return h.m.HasCommand("greet2") })
	if h.m.HasCommand("greet") || h.m.Wants(EventPreExec) {
		t.Fatal("the second hello should replace the first")
	}
	h.m.PreExec("ls", "/")
	h.m.Prompt("/", 0, nil, 0)
	if got := h.message("event "); !strings.Contains(got, `"event":"prompt"`) {
		t.Fatalf("got %s before the prompt event", got)
	}
}

func TestRun(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	cases := []struct {
		name  string
		args  []string
		stdin *string
		want  Result
	}{
		{"greet", []string{"a", "b c"}, nil, Result{Stdout: "hello a b c\n"}},
		{"cat", nil, nil, Result{Stdout: "(no stdin)\n"}},
		{"cat", nil, ptr("piped\n"), Result{Stdout: "piped\n"}},
		{"cat", nil, ptr(""), Result{Stdout: ""}},
		{"pwd", nil, nil, Result{Stdout: "/some/where\n"}},
		{"fail", nil, nil, Result{Stderr: "it failed\n", Status: 3}},
	}
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		got, err := h.m.Run(ctx, c.name, c.args, "/some/where", c.stdin)
		cancel()
		if err != nil || got != c.want {
			t.Errorf("%s %v: got %+v %v, want %+v", c.name, c.args, got, err, c.want)
		}
	}
	if _, err := h.run("nope"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown command: %v", err)
	}
}

func TestRunConcurrent(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	var wg sync.WaitGroup
	results := make([]string, 6)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// later answers out of order, after (6-i)*20ms
			r, err := h.run("later", strconv.Itoa((6-i)*20))
			if err != nil {
				results[i] = err.Error()
				return
			}
			results[i] = r.Stdout
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if want := "later " + strconv.Itoa((6-i)*20) + "\n"; r != want {
			t.Errorf("request %d got %q, want %q", i, r, want)
		}
	}
}

func TestRunTimeoutAndCancel(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := h.m.Run(ctx, "slow", nil, "/", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the timeout took %v", d)
	}
	// the plugin hears about it
	h.message("cancel ")
	// and still works
	if r, err := h.run("greet", "x"); err != nil || r.Stdout != "hello x\n" {
		t.Fatalf("after a timeout: %+v %v", r, err)
	}
}

func TestComplete(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	got, ok := h.m.Complete("fruit", "fruit ap", 8, time.Second)
	want := []Candidate{{"apple", "fruit"}, {"apricot", "fruit"}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v %v", got, ok)
	}
	// the cursor counts
	if got, ok := h.m.Complete("fruit", "fruit ap ban", 8, time.Second); !ok || len(got) != 2 {
		t.Fatalf("cursor in the middle: %+v %v", got, ok)
	}
	if got, ok := h.m.Complete("fruit", "fruit x", 7, time.Second); !ok || len(got) != 0 {
		t.Fatalf("no match: %+v %v", got, ok)
	}
	if _, ok := h.m.Complete("ls", "ls ", 3, time.Second); ok {
		t.Fatal("nobody completes ls")
	}
	// answered from the cache the second time
	start := time.Now()
	if got, ok := h.m.Complete("fruit", "fruit ap", 8, time.Second); !ok || len(got) != 2 {
		t.Fatalf("cached: %+v %v", got, ok)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("a cached answer took %v", d)
	}
}

func TestCompleteTimeoutKeepsLateAnswer(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	start := time.Now()
	if got, ok := h.m.Complete("slowfruit", "slowfruit ", 10, 100*time.Millisecond); ok {
		t.Fatalf("expected a timeout, got %+v", got)
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("the timeout took %v", d)
	}
	// the answer comes after 500ms and is kept for the next tab press
	waitUntil(t, "the late answer", func() bool {
		_, ok := h.m.cache.get(cacheKeyFor(h, "slowfruit", "slowfruit ", 10))
		return ok
	})
	got, ok := h.m.Complete("slowfruit", "slowfruit ", 10, 100*time.Millisecond)
	if !ok || len(got) != 1 || got[0].Text != "late" {
		t.Fatalf("got %+v %v", got, ok)
	}
}

func cacheKeyFor(h *harness, cmd, line string, cursor int) string {
	p := h.m.tables.Load().complete[cmd]
	return strconv.FormatUint(p.seq, 10) + "\x00" + cmd + "\x00" + strconv.Itoa(cursor) + "\x00" + line
}

func TestCrashReportedOnce(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	if _, err := h.run("crash"); !errors.Is(err, ErrDead) {
		t.Fatalf("got %v", err)
	}
	msg := h.message("ash: plugin basic stopped")
	if !strings.Contains(msg, "exit status 7") || !strings.Contains(msg, "basic.log") {
		t.Fatalf("message: %s", msg)
	}
	in := h.info("basic")
	if in.State != Dead || in.Exit != "exit status 7" {
		t.Fatalf("info: %+v", in)
	}
	if h.m.HasCommand("greet") || h.m.Wants(EventPrompt) {
		t.Fatal("a dead plugin keeps no commands or events")
	}
	if _, err := h.run("greet"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("got %v", err)
	}
	h.m.Prompt("/", 0, nil, 0) // goes nowhere
	time.Sleep(50 * time.Millisecond)
	for _, a := range append(h.backlog, h.m.Actions()...) {
		if a.Kind == ActMessage && strings.Contains(a.Text, "stopped") {
			t.Fatalf("reported twice: %s", a.Text)
		}
	}
	// a dead plugin can be loaded again under the same name
	h.load("basic", "basic")
}

func TestExitBeforeHello(t *testing.T) {
	h := newHarness(t, Options{})
	err := h.m.Load(Spec{Name: "sh", Path: "/bin/sh", Args: []string{"sh", "-c", "echo oops >&2; exit 3"}, Dir: h.dir})
	if err != nil {
		t.Fatal(err)
	}
	err = h.m.WaitReady("sh", 5*time.Second)
	if !errors.Is(err, ErrDead) || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("got %v", err)
	}
	// the error was the report; there's no second one
	for _, a := range h.m.Actions() {
		if a.Kind == ActMessage {
			t.Fatalf("reported twice: %s", a.Text)
		}
	}
	// without WaitReady, the exit is reported in a message
	if err := h.m.Load(Spec{Name: "sh2", Path: "/bin/sh", Args: []string{"sh", "-c", "exit 4"}, Dir: h.dir}); err != nil {
		t.Fatal(err)
	}
	h.message("ash: plugin sh2 stopped (exit status 4)")
	b, _ := os.ReadFile(h.info("sh").Log)
	if !strings.Contains(string(b), "oops\n") || !strings.Contains(string(b), "exited: exit status 3") {
		t.Fatalf("stderr and the exit go to the log: %q", b)
	}
}

func TestSilentPlugin(t *testing.T) {
	h := newHarness(t, Options{})
	if err := h.m.Load(h.spec("silent", "silent")); err != nil {
		t.Fatal(err)
	}
	if err := h.m.WaitReady("silent", 100*time.Millisecond); !errors.Is(err, ErrNoHello) {
		t.Fatalf("got %v", err)
	}
	if in := h.info("silent"); in.State != Starting {
		t.Fatalf("state %v", in.State)
	}
}

func TestUnloadKillsProcessGroup(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	r, err := h.run("spawn")
	if err != nil || r.Status != 0 {
		t.Fatalf("spawn: %+v %v", r, err)
	}
	child, _ := strconv.Atoi(strings.TrimSpace(r.Stdout))
	leader := h.info("basic").Pid
	if _, err := h.run("act", "segment", "git", "main"); err != nil {
		t.Fatal(err)
	}
	h.next("the segment", func(a Action) bool { return a.Kind == ActSegment && a.Text == "main" })
	start := time.Now()
	if err := h.m.Unload("basic"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("unload took %v", d)
	}
	for _, pid := range []int{leader, child} {
		waitUntil(t, "process "+strconv.Itoa(pid)+" to go away", func() bool { return gone(pid) })
	}
	if len(h.m.List()) != 0 || h.m.HasCommand("greet") {
		t.Fatal("an unloaded plugin is forgotten")
	}
	// its segment is cleared, and an unload is not a crash
	h.next("the segment to clear", func(a Action) bool { return a.Kind == ActSegment && a.Name == "git" && a.Text == "" })
	for _, a := range append(h.backlog, h.m.Actions()...) {
		if a.Kind == ActMessage && strings.Contains(a.Text, "stopped") {
			t.Fatalf("an unload is reported as a crash: %s", a.Text)
		}
	}
	if err := h.m.Unload("basic"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("second unload: %v", err)
	}
}

// gone reports whether pid no longer exists (or is a zombie nobody will
// see again).
func gone(pid int) bool {
	err := syscall.Kill(pid, 0)
	return errors.Is(err, syscall.ESRCH)
}

func TestReload(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	old := h.info("basic").Pid
	if err := h.m.Reload("basic", h.dir, append(os.Environ(), plugintest.Env("basic"))); err != nil {
		t.Fatal(err)
	}
	if err := h.m.WaitReady("basic", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if pid := h.info("basic").Pid; pid == old {
		t.Fatal("reload kept the old process")
	}
	if r, err := h.run("greet", "again"); err != nil || r.Stdout != "hello again\n" {
		t.Fatalf("after reload: %+v %v", r, err)
	}
	if err := h.m.Reload("nope", "/", nil); !errors.Is(err, ErrUnknown) {
		t.Fatalf("got %v", err)
	}
}

func TestReservedCommands(t *testing.T) {
	h := newHarness(t, Options{Reserved: func(n string) bool { return n == "pwd" }})
	h.load("basic", "basic")
	if h.m.HasCommand("pwd") || !h.m.HasCommand("greet") {
		t.Fatal("a builtin's name can't be taken")
	}
	b, _ := os.ReadFile(h.info("basic").Log)
	if !strings.Contains(string(b), "pwd is a builtin") {
		t.Fatalf("log: %s", b)
	}
}

func TestFirstPluginWinsACommand(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("one", "basic")
	h.load("two", "basic")
	if p := h.m.CommandPlugin("greet"); p != "one" {
		t.Fatalf("greet from %s", p)
	}
	if err := h.m.Unload("one"); err != nil {
		t.Fatal(err)
	}
	if p := h.m.CommandPlugin("greet"); p != "two" {
		t.Fatalf("after unloading one, greet from %q", p)
	}
}

func TestBadLinesAreLogged(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	if _, err := h.run("noise"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(h.info("basic").Log)
	for _, want := range []string{"isn't a JSON object", "nothing ash knows: {}", `segment "bad name"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("log lacks %q:\n%s", want, b)
		}
	}
}

func TestFullQueueDropsEvents(t *testing.T) {
	h := newHarness(t, Options{QueueSize: 16})
	h.load("deaf", "deaf")
	start := time.Now()
	for i := 0; i < 20000; i++ {
		h.m.PostExec("echo "+strconv.Itoa(i)+" "+strings.Repeat("x", 100), 0, 0, "/")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("sending to a plugin that doesn't read took %v", d)
	}
	if n := h.info("deaf").DroppedEvents; n == 0 {
		t.Fatal("nothing was dropped")
	}
	// requests give up at their deadline
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := h.m.Run(ctx, "deafcmd", nil, "/", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestChattyPlugin(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("spam", "spam")
	// Don't take the actions until the plugin is done: 3000 updates of
	// one segment and 2000 messages have to fit in maxPending.
	waitUntil(t, "the last segment", func() bool {
		h.m.amu.Lock()
		defer h.m.amu.Unlock()
		for _, a := range h.m.actions {
			if a.Kind == ActSegment && a.Text == "done" {
				return true
			}
		}
		return false
	})
	acts := h.m.Actions()
	segments, messages := 0, 0
	for _, a := range acts {
		switch a.Kind {
		case ActSegment:
			segments++
		case ActMessage:
			messages++
		}
	}
	if segments != 1 || messages != maxPending-1 {
		t.Fatalf("%d segment updates and %d messages queued", segments, messages)
	}
	if n := h.info("spam").DroppedActions; n != 2000-(maxPending-1) {
		t.Fatalf("%d actions counted as dropped", n)
	}
}

func TestClose(t *testing.T) {
	h := newHarness(t, Options{})
	h.load("basic", "basic")
	h.load("stubborn", "stubborn")
	pids := []int{h.info("basic").Pid, h.info("stubborn").Pid}
	start := time.Now()
	h.m.Close(200 * time.Millisecond)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("close took %v", d)
	}
	for _, pid := range pids {
		waitUntil(t, "process "+strconv.Itoa(pid)+" to go away", func() bool { return gone(pid) })
	}
	if err := h.m.Load(h.spec("basic", "basic")); !errors.Is(err, ErrClosed) {
		t.Fatalf("load after close: %v", err)
	}
	h.m.Close(0) // twice is fine
}

func TestWakeOncePerBatch(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	m := New(Options{Wake: func() { mu.Lock(); calls++; mu.Unlock() }})
	m.Notify("x", "one")
	m.Notify("x", "two")
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 1 {
		t.Fatalf("woken %d times", n)
	}
	if a := m.Actions(); len(a) != 2 {
		t.Fatalf("got %+v", a)
	}
	m.Notify("x", "three")
	mu.Lock()
	n = calls
	mu.Unlock()
	if n != 2 {
		t.Fatalf("woken %d times after taking the actions", n)
	}
	if m.Actions() == nil || m.Actions() != nil {
		t.Fatal("Actions should drain")
	}
}

func waitUntil(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
