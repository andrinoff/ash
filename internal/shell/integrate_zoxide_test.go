package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// zoxideTest installs a fake zoxide: `query` prints $LOG.reply and exits
// with $LOG.status; every call is logged along with $_ZO_DATA_DIR.
func zoxideTest(t *testing.T) *integTest {
	t.Chdir(t.TempDir())
	it := newIntegTest(t)
	it.tool("zoxide", `printf 'data=%s\n' "$_ZO_DATA_DIR" >> "$LOG"
if [ "$1" = query ]; then
	cat "$LOG.reply" 2>/dev/null
	exit "$(cat "$LOG.status" 2>/dev/null || echo 0)"
fi`)
	it.mustRun("integrate zoxide")
	return it
}

func TestZoxideJump(t *testing.T) {
	it := zoxideTest(t)
	proj := it.mkdir("proj", "other")
	other := filepath.Join(it.home, "other")
	it.mustRun("cd " + proj)
	it.calls() // the cd's add

	cases := []struct {
		name, src, reply, dir string
		status                int
		calls                 []string
	}{
		{"home", "z", "", it.home, 0, []string{"zoxide [add] [--] [" + it.home + "]", "data=" + it.home + "/zoxide"}},
		// Each case starts with `cd proj` from where the last one went.
		{"back", "z -", "", it.home, 0, []string{"zoxide [add] [--] [" + it.home + "]", "data=" + it.home + "/zoxide"}},
		{"existing directory", "z ../other", "", other, 0, []string{"zoxide [add] [--] [" + other + "]", "data=" + it.home + "/zoxide"}},
		{"after --", "z -- " + proj, "", proj, 0, []string{"zoxide [add] [--] [" + proj + "]", "data=" + it.home + "/zoxide"}},
		{"query", "z oth er", other + "\n", other, 0, []string{
			"zoxide [query] [--exclude] [" + proj + "] [--] [oth] [er]", "data=" + it.home + "/zoxide",
			"zoxide [add] [--] [" + other + "]", "data=" + it.home + "/zoxide"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it.write("log.reply", c.reply)
			if _, errs, st := it.run(c.src); st != c.status {
				t.Fatalf("status %d: %s", st, errs)
			}
			if it.sh.Dir() != c.dir {
				t.Errorf("in %s, want %s", it.sh.Dir(), c.dir)
			}
			if calls := it.calls(); !equalLines(calls, c.calls) {
				t.Errorf("calls\n%q\nwant\n%q", calls, c.calls)
			}
			it.mustRun("cd " + proj)
			it.calls()
		})
	}

	// No match: zoxide's status, and we stay put.
	it.write("log.status", "1")
	if _, _, st := it.run("z nothing"); st != 1 || it.sh.Dir() != proj {
		t.Errorf("no match: status %d, in %s", st, it.sh.Dir())
	}
	os.Remove(filepath.Join(it.home, "log.status"))

	// _ZO_ECHO=1 prints where we went.
	it.export("_ZO_ECHO", "1")
	it.write("log.reply", other+"\n")
	if out := it.mustRun("z oth"); out != other+"\n" {
		t.Errorf("_ZO_ECHO: %q", out)
	}
}

func TestZiNeedsZoxide(t *testing.T) {
	it := newIntegTest(t)
	if _, _, st := it.run("zi foo"); st != 127 {
		t.Errorf("zi without integrate: status %d, want 127", st)
	}
}

func TestZoxideInteractive(t *testing.T) {
	it := zoxideTest(t)
	target := it.mkdir("picked")
	it.write("log.reply", target+"\n")
	it.mustRun("zi pi ck")
	if it.sh.Dir() != target {
		t.Errorf("in %s, want %s", it.sh.Dir(), target)
	}
	want := []string{"zoxide [query] [--interactive] [--] [pi] [ck]", "data=" + it.home + "/zoxide",
		"zoxide [add] [--] [" + target + "]", "data=" + it.home + "/zoxide"}
	if calls := it.calls(); !equalLines(calls, want) {
		t.Errorf("calls\n%q\nwant\n%q", calls, want)
	}
	// Cancelled: no cd.
	it.write("log.status", "130")
	if _, _, st := it.run("zi"); st != 130 || it.sh.Dir() != target {
		t.Errorf("cancelled zi: status %d, in %s", st, it.sh.Dir())
	}
}

func TestZoxideAdd(t *testing.T) {
	it := zoxideTest(t)
	real := it.mkdir("real")
	link := filepath.Join(it.home, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	it.mustRun("cd " + link)
	want := []string{"zoxide [add] [--] [" + link + "]", "data=" + it.home + "/zoxide"}
	if calls := it.calls(); !equalLines(calls, want) {
		t.Errorf("calls %q, want %q", calls, want)
	}
	// _ZO_RESOLVE_SYMLINKS=1 adds the physical directory.
	it.export("_ZO_RESOLVE_SYMLINKS", "1")
	it.mustRun("cd /; cd " + link)
	physical, _ := filepath.EvalSymlinks(link)
	// The two cds may be batched into one `zoxide add`.
	added := ""
	for _, c := range it.calls() {
		if strings.HasPrefix(c, "zoxide [add] [--] ") {
			added += strings.ReplaceAll(strings.TrimPrefix(c, "zoxide [add] [--] "), "] [", "][")
		}
	}
	if added != "[/]["+physical+"]" {
		t.Errorf("added %s, want [/][%s]", added, physical)
	}
	// Private mode adds nothing.
	it.mustRun("fish_private_mode=1; cd " + real)
	if calls := it.calls(); calls != nil {
		t.Errorf("private mode: %q", calls)
	}
}

// `zoxide add` gives up on the whole call when one of its directories is
// gone ("not a directory"), so a batch must not carry a directory that was
// removed while the batch waited for the previous add.
func TestZoxideAddSkipsRemovedDirs(t *testing.T) {
	t.Chdir(t.TempDir())
	it := newIntegTest(t)
	// The first add waits for $LOG.go, so the next cds pile up into one
	// batch; like zoxide, any argument that is not a directory fails it.
	it.tool("zoxide", `if [ ! -e "$LOG.first" ]; then
	: > "$LOG.first"
	while [ ! -e "$LOG.go" ]; do sleep 0.01; done
fi
shift 2
for d; do [ -d "$d" ] || { echo "zoxide: not a directory: $d" >&2; exit 1; }; done`)
	it.mustRun("integrate zoxide")
	a, b, c := it.mkdir("a"), it.mkdir("b"), it.mkdir("c")
	it.mustRun("cd " + a)
	for deadline := time.Now().Add(10 * time.Second); !fileExists(filepath.Join(it.home, "log.first")); {
		if time.Now().After(deadline) {
			t.Fatal("the first zoxide add did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	it.mustRun("cd " + b + "; cd " + c)
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	it.write("log.go", "")
	want := []string{"zoxide [add] [--] [" + a + "]", "zoxide [add] [--] [" + c + "]"}
	if calls := it.calls(); !equalLines(calls, want) {
		t.Errorf("calls\n%q\nwant\n%q", calls, want)
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestRealZoxide(t *testing.T) {
	t.Chdir(t.TempDir())
	it := newIntegTest(t)
	it.realTool("zoxide")
	alpha := it.mkdir("work/alphaproj", "work/betaproj")
	it.mustRun("integrate zoxide; cd " + alpha + "; cd ../betaproj; cd " + alpha + "; cd /")
	it.sh.WaitIntegrations(10 * time.Second)
	if _, errs, st := it.run("z alpha"); st != 0 || it.sh.Dir() != alpha {
		t.Fatalf("z alpha: status %d, in %s, stderr %q", st, it.sh.Dir(), errs)
	}
	// The current directory is excluded, so the next best wins.
	if _, errs, st := it.run("z proj"); st != 0 || it.sh.Dir() != filepath.Join(it.home, "work/betaproj") {
		t.Errorf("z proj: status %d, in %s, stderr %q", st, it.sh.Dir(), errs)
	}
}
