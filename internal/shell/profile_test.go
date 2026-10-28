package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const ms = time.Millisecond

// stmtAt makes a profile item for the statement that starts with text in
// src.
func stmtAt(t *testing.T, name, src, text string, self, total time.Duration) profItem {
	t.Helper()
	i := strings.Index(src, text)
	if i < 0 {
		t.Fatalf("%q not in source", text)
	}
	return profItem{name: name, src: src, pos: i, end: i + len(text), self: self, total: total}
}

func TestProfileReportFormat(t *testing.T) {
	cfg := "/home/me/.config/ash/config.ash"
	src := "set -g x 1\n" +
		"if status is-interactive\n" +
		"    eval (brew shellenv)\n" +
		"    kubectl completion fish | source\n" +
		"end\n" +
		"echo \x1b[31mred\x1b[0m\n" +
		"set -gx LONG 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'\n"
	ifStmt := stmtAt(t, cfg, src, "if status is-interactive\n    eval (brew shellenv)\n    kubectl completion fish | source\nend", 210*time.Microsecond, 140330*time.Microsecond)
	p := &Profile{
		home:   "/home/me",
		config: "/home/me/.config/ash",
		phases: []profPhase{{name: "config", start: 2 * ms, dur: 156040 * time.Microsecond}},
		items: []profItem{
			{name: cfg, file: true, self: 400 * time.Microsecond, total: 155300 * time.Microsecond},
			stmtAt(t, cfg, src, "set -g x 1", 20*time.Microsecond, 20*time.Microsecond),
			ifStmt,
			stmtAt(t, cfg, src, "eval (brew shellenv)", 60020*time.Microsecond, 60020*time.Microsecond),
			stmtAt(t, cfg, src, "kubectl completion fish | source", 80100*time.Microsecond, 80100*time.Microsecond),
			stmtAt(t, cfg, src, "echo \x1b[31mred\x1b[0m", 30*time.Microsecond, 30*time.Microsecond),
			stmtAt(t, cfg, src, "set -gx LONG 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'", 25*time.Microsecond, 25*time.Microsecond),
			{name: "/home/me/.nvm/nvm.sh", file: true, self: 3800 * time.Microsecond, total: 45200 * time.Microsecond},
			{name: "/opt/some/very/long/directory/name/that/goes/on/init.sh", file: true, self: 10 * time.Microsecond, total: 900 * time.Microsecond},
		},
	}
	nvm := strings.Repeat("x\n", 409) + "nvm_auto use\n"
	p.items = append(p.items, stmtAt(t, "/home/me/.nvm/nvm.sh", nvm, "nvm_auto use", 40*ms, 40*ms))
	// enough small ones to fill the table
	small := "a\nb\nc\nd\ne\nf\n"
	for i, s := range []string{"a", "b", "c", "d", "e", "f"} {
		p.items = append(p.items, stmtAt(t, "/home/me/.config/ash/conf.d/x.ash", small, s, time.Duration(6-i)*time.Microsecond, time.Duration(6-i)*time.Microsecond))
	}

	var b strings.Builder
	p.WriteReport(&b, "first prompt", 171310*time.Microsecond)
	want := `ash: profile, slowest first
  where                                 self     total  statement
  config.ash:4                       80.10ms   80.10ms  kubectl completion fish | source
  config.ash:3                       60.02ms   60.02ms  eval (brew shellenv)
  ~/.nvm/nvm.sh:410                  40.00ms   40.00ms  nvm_auto use
  config.ash:2                        0.21ms  140.33ms  if status is-interactive …
  config.ash:6                        0.03ms    0.03ms  echo [31mred[0m
  config.ash:7                        0.03ms    0.03ms  set -gx LONG 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa…
  config.ash:1                        0.02ms    0.02ms  set -g x 1
  conf.d/x.ash:1                      0.01ms    0.01ms  a
  conf.d/x.ash:2                      0.01ms    0.01ms  b
  conf.d/x.ash:3                      0.00ms    0.00ms  c
  ... and 3 more statements, 0.01ms in all
  config.ash                          0.40ms  155.30ms  (whole file)
  ~/.nvm/nvm.sh                       3.80ms   45.20ms  (whole file)
  …ctory/name/that/goes/on/init.sh    0.01ms    0.90ms  (whole file)
ash: config took 156.04ms, first prompt at 171.31ms
`
	if got := b.String(); got != want {
		t.Errorf("report:\n%s\nwant:\n%s", got, want)
	}

	// Nothing measured: only the summary.
	b.Reset()
	(&Profile{}).WriteReport(&b, "exit", 5*ms)
	if got := b.String(); got != "ash: exit at 5.00ms\n" {
		t.Errorf("empty report: %q", got)
	}
}

// profTest runs files and strings in a shell with profiling on.
type profTest struct {
	t   *testing.T
	sh  *Shell
	dir string
	io  *IO
	out *lockedBuffer
}

func newProfTest(t *testing.T) *profTest {
	t.Helper()
	sh := New()
	sh.Init()
	dir := t.TempDir()
	sh.SetVar("HOME", []string{dir}, setOpts{scope: setGlobal, export: 1})
	sh.SetVar("XDG_CONFIG_HOME", []string{filepath.Join(dir, ".config")}, setOpts{scope: setGlobal, export: 1})
	sh.SetVar("D", []string{dir}, setOpts{scope: setGlobal})
	out := &lockedBuffer{}
	// No stdin reader: external commands would share it, and their stdin
	// copiers would race on it.
	io := &IO{In: &Stream{}, Out: &Stream{W: out}, Err: &Stream{W: &lockedBuffer{}}}
	sh.StartProfile(time.Now())
	return &profTest{t: t, sh: sh, dir: dir, io: io, out: out}
}

func (pt *profTest) write(name, body string) string {
	pt.t.Helper()
	p := filepath.Join(pt.dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		pt.t.Fatal(err)
	}
	return p
}

// stop ends profiling and returns the items by their where column (with
// the statement text for statements, since lines can repeat).
func (pt *profTest) stop() (*Profile, map[string]profItem) {
	pt.t.Helper()
	p := pt.sh.StopProfile()
	if p == nil {
		pt.t.Fatal("not profiling")
	}
	byWhere := map[string]profItem{}
	for _, it := range p.items {
		key := p.where(it)
		if !it.file {
			key += " " + profText(it.src, it.pos, it.end)
		}
		if _, dup := byWhere[key]; dup {
			pt.t.Fatalf("%s measured twice", key)
		}
		byWhere[key] = it
	}
	return p, byWhere
}

func keys(m map[string]profItem) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestProfileStatementsAndFiles(t *testing.T) {
	pt := newProfTest(t)
	pt.write("other.ash", "set -g o 1\nsleep 0.02\n")
	cfg := pt.write("config.ash", `set -g x 1
if test -n "$D"
    sleep 0.03
    source $D/other.ash
else
    echo not run
end
function f
    echo in function
end
for i in 1 2
    set -g y $i
end
f
switch a
    case a
        set -g z 1
end
begin; set -g w 1; end
echo 'set -g p 1' | source
`)
	pt.sh.RunFile(cfg, nil, pt.io)
	p, items := pt.stop()
	want := []string{
		"~/config.ash", "~/other.ash", "stdin",
		"~/config.ash:1 set -g x 1",
		"~/config.ash:2 if test -n \"$D\" …",
		"~/config.ash:2 test -n \"$D\"",
		"~/config.ash:3 sleep 0.03",
		"~/config.ash:4 source $D/other.ash",
		"~/other.ash:1 set -g o 1",
		"~/other.ash:2 sleep 0.02",
		"~/config.ash:8 function f …",
		"~/config.ash:11 for i in 1 2 …",
		"~/config.ash:14 f",
		"~/config.ash:15 switch a …",
		"~/config.ash:17 set -g z 1",
		"~/config.ash:19 begin; set -g w 1; end",
		"~/config.ash:19 set -g w 1",
		"~/config.ash:20 echo 'set -g p 1' | source",
		"stdin:1 set -g p 1",
	}
	for _, w := range want {
		if _, ok := items[w]; !ok {
			t.Errorf("missing %q; have %q", w, keys(items))
		}
	}
	if len(items) != len(want) {
		t.Errorf("measured %d things, want %d: %q", len(items), len(want), keys(items))
	}

	ifStmt := items["~/config.ash:2 if test -n \"$D\" …"]
	src := items["~/config.ash:4 source $D/other.ash"]
	other := items["~/other.ash"]
	if ifStmt.total < 50*ms {
		t.Errorf("if took %v, want at least the 50ms of sleeps in it", ifStmt.total)
	}
	if ifStmt.self > ifStmt.total/2 {
		t.Errorf("if: self %v of total %v; the sleeps are nested statements", ifStmt.self, ifStmt.total)
	}
	if other.total < 20*ms || src.total < other.total || src.self > src.total/2 {
		t.Errorf("source: self %v total %v, file total %v", src.self, src.total, other.total)
	}
	// self times add up to the whole: nothing is counted twice
	var sum time.Duration
	for _, it := range p.items {
		sum += it.self
	}
	if file := items["~/config.ash"]; sum != file.total {
		t.Errorf("self times add up to %v, the config took %v", sum, file.total)
	}
}

func TestProfileStrings(t *testing.T) {
	pt := newProfTest(t)
	// unnamed strings (traps, bindings) are not broken down by themselves
	pt.sh.RunString("set -g a 1", "", pt.io)
	// ash -c names its command
	pt.sh.ProfileAs("-c")
	pt.sh.RunString("set -g b 1\nset -g c 1", "", pt.io)
	// the name is used once
	pt.sh.RunString("set -g d 1", "", pt.io)
	// named strings run at the top level are: ash < script
	pt.sh.RunString("set -g e 1", "stdin", pt.io)
	// strings run by a statement are part of that statement
	cfg := pt.write("config.ash", "argparse 'n/num=!set -g checked 1' -- --num 5\ntrap 'set -g bye 1' EXIT\n")
	pt.sh.RunFile(cfg, nil, pt.io)
	pt.sh.RunExitTrap()
	if pt.sh.Get("checked") != "1" || pt.sh.Get("bye") != "1" {
		t.Fatal("the validation and the trap should have run")
	}
	_, items := pt.stop()
	want := []string{
		"-c:1 set -g b 1", "-c:2 set -g c 1", "stdin:1 set -g e 1",
		"~/config.ash", "~/config.ash:1 argparse 'n/num=!set -g checked 1' -- --num 5", "~/config.ash:2 trap 'set -g bye 1' EXIT",
	}
	for _, w := range want {
		if _, ok := items[w]; !ok {
			t.Errorf("missing %q; have %q", w, keys(items))
		}
	}
	if len(items) != len(want) {
		t.Errorf("measured %q, want %q", keys(items), want)
	}
}

func TestProfilePhasesAndStop(t *testing.T) {
	pt := newProfTest(t)
	end := pt.sh.ProfilePhase("config")
	pt.sh.ProfileAs("-c")
	pt.sh.RunString("sleep 0.01", "", pt.io)
	end()
	p, _ := pt.stop()
	if len(p.phases) != 1 || p.phases[0].name != "config" || p.phases[0].dur < 10*ms {
		t.Errorf("phases: %+v", p.phases)
	}
	if pt.sh.Profiling() || pt.sh.StopProfile() != nil {
		t.Error("still profiling after StopProfile")
	}
	// nothing is recorded any more, and the hooks are no-ops
	pt.sh.ProfileAs("-c")
	pt.sh.ProfilePhase("x")()
	if st := pt.sh.RunString("set -g after 1", "", pt.io); st != 0 || pt.sh.Get("after") != "1" {
		t.Error("running after the profile stopped failed")
	}
}

// A subshell that outlives StopProfile (a background job started by the
// config) must not record into the finished profile or race with it.
func TestProfileBackgroundAfterStop(t *testing.T) {
	pt := newProfTest(t)
	f := pt.write("bg.ash", "set -g x 1\n")
	pt.sh.ProfileAs("-c")
	pt.sh.RunString("begin; sleep 0.05; source "+f+"; end &", "", pt.io)
	p, _ := pt.stop()
	n := len(p.items)
	pt.sh.RunString("wait", "", pt.io)
	if len(p.items) != n {
		t.Error("the profile changed after it was stopped")
	}
}

func TestProfText(t *testing.T) {
	cases := []struct{ src, want string }{
		{"echo hi", "echo hi"},
		{"  echo\thi  ", "echo hi"},
		{"if true\n  echo\nend", "if true …"},
		{"echo \x07bell\x1b[0m", "echo bell[0m"},
		{strings.Repeat("é", 60), strings.Repeat("é", 49) + "…"},
	}
	for _, c := range cases {
		if got := profText(c.src, 0, len(c.src)); got != c.want {
			t.Errorf("profText(%q) = %q, want %q", c.src, got, c.want)
		}
	}
	if got := profText("abc", 2, 1); got != "" {
		t.Errorf("bad range: %q", got)
	}
}

// The profiler's hooks sit on the statement loop. With profiling off they
// must cost nothing measurable: compare the ns/op of off with the same
// benchmark on a build without them (see docs/config.md for numbers).
func BenchmarkProfileHook(b *testing.B) {
	src := `i=0; while [ $i -lt 1000 ]; do i=$((i+1)); x=$i; done`
	run := func(b *testing.B, profile bool) {
		sh := New()
		sh.Init()
		io := &IO{In: &Stream{}, Out: &Stream{W: &lockedBuffer{}}, Err: &Stream{W: &lockedBuffer{}}}
		if profile {
			sh.StartProfile(time.Now())
			sh.ProfileAs("-c")
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sh.RunString(src, "bench", io)
		}
	}
	b.Run("off", func(b *testing.B) { run(b, false) })
	b.Run("on", func(b *testing.B) { run(b, true) })
}
