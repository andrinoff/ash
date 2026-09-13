package shell

import (
	"strings"
	"testing"
	"time"
)

func TestIntegrateBuiltin(t *testing.T) {
	it := newIntegTest(t)
	cases := []struct {
		name, src  string
		installed  bool // whether starship is installed by then
		errs       string
		errPrefix  bool
		wantStatus int
	}{
		// Not installed: status 1 and silence, so configs stay portable.
		{"missing tool", "integrate starship", false, "", false, 1},
		{"missing tool explained", "integrate -v starship", false, "integrate: starship: not installed (no starship in $PATH)\n", false, 1},
		{"unknown tool", "integrate nosuch", false, "integrate: unknown tool 'nosuch' (known: ", true, 2},
		{"unknown option", "integrate -q starship", false, "integrate: unknown option -q\n", false, 2},
		{"installed", "integrate starship", true, "", false, 0},
		{"worst status wins", "integrate nosuch starship", true, "integrate: unknown tool 'nosuch'", true, 2},
		{"explained", "integrate -v starship", true, "integrate: starship: the prompt is drawn by " + it.bin + "/starship\n", false, 0},
		{"after --", "integrate -- starship", true, "", false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.installed && !it.sh.HasCommand("starship") {
				it.tool("starship", "")
			}
			out, errs, st := it.run(c.src)
			if st != c.wantStatus || out != "" {
				t.Errorf("status %d out %q, want %d", st, out, c.wantStatus)
			}
			if c.errPrefix && !strings.HasPrefix(errs, c.errs) || !c.errPrefix && errs != c.errs {
				t.Errorf("stderr %q, want %q", errs, c.errs)
			}
		})
	}
	usage := it.mustRun("integrate -h")
	for _, tool := range integrationList {
		if !strings.Contains(usage, "  "+tool.name+" ") {
			t.Errorf("help does not mention %q:\n%s", tool.name, usage)
		}
	}
}

func TestIntegrateList(t *testing.T) {
	list := func(it *integTest) map[string]string {
		got := map[string]string{}
		for _, line := range strings.Split(strings.TrimSuffix(it.mustRun("integrate"), "\n"), "\n") {
			f := strings.Fields(line)
			got[f[0]] = strings.Join(f[1:], " ")
		}
		if len(got) != len(integrationList) {
			t.Errorf("listed %d tools, want %d", len(got), len(integrationList))
		}
		return got
	}
	it := newIntegTest(t)
	if got := list(it)["starship"]; got != "not installed" {
		t.Errorf("starship: %q, want not installed", got)
	}
	it.tool("starship", "")
	if got, want := list(it)["starship"], "available "+it.bin+"/starship"; got != want {
		t.Errorf("starship: %q, want %q", got, want)
	}
	if it.sh.IntegrationEnabled("starship") {
		t.Error("IntegrationEnabled before integrate")
	}
	it.mustRun("integrate starship")
	if got, want := list(it)["starship"], "enabled "+it.bin+"/starship"; got != want {
		t.Errorf("starship: %q, want %q", got, want)
	}
	if !it.sh.IntegrationEnabled("starship") {
		t.Error("IntegrationEnabled after integrate")
	}
}

func TestIntegrateStarship(t *testing.T) {
	it := newIntegTest(t)
	it.tool("starship", "")
	if it.sh.PromptProvider() != "" {
		t.Fatal("prompt provider set before integrate")
	}
	it.mustRun("integrate starship")
	if p := it.sh.PromptProvider(); p != "starship" {
		t.Errorf("prompt provider %q", p)
	}
	if v := it.sh.Get("VIRTUAL_ENV_DISABLE_PROMPT"); v != "1" {
		t.Errorf("VIRTUAL_ENV_DISABLE_PROMPT=%q", v)
	}
	if calls := it.calls(); calls != nil {
		t.Errorf("starship was run: %q", calls)
	}
}

func TestIntegrateBindsWhenEditorStarts(t *testing.T) {
	it := newIntegTest(t)
	it.sh.integrateBind(integBinding{"ctrl-x", "cmd-a"})
	it.sh.integrateBind(integBinding{"ctrl-y", "cmd-b"})
	ed := &integEditor{}
	it.sh.Editor = ed
	it.sh.RunPrePrompt()
	it.sh.RunPrePrompt()
	want := []string{"default ctrl-x cmd-a", "insert ctrl-x cmd-a", "default ctrl-y cmd-b", "insert ctrl-y cmd-b"}
	if !equalLines(ed.binds, want) {
		t.Errorf("binds %q, want %q", ed.binds, want)
	}
	if n := len(it.sh.shared.hooks.prePrompt); n != 1 {
		t.Errorf("%d prompt hooks, want 1", n)
	}
}

func TestIntegrationEvalKeepsStatus(t *testing.T) {
	it := newIntegTest(t)
	it.run("false")
	it.sh.integrationEval("export FROM_TOOL='a b'\nunset HOME_NOT_SET\ntrue", "tool")
	if it.sh.Status() != 1 {
		t.Errorf("status %d after eval, want 1", it.sh.Status())
	}
	if v := it.sh.Get("FROM_TOOL"); v != "a b" {
		t.Errorf("FROM_TOOL=%q", v)
	}
}

// WaitIntegrations may give up while work still runs (the frontend waits
// with a timeout); work started afterwards must not trip over the wait it
// abandoned (a sync.WaitGroup panics: "reused before previous Wait has
// returned", which kills the shell).
func TestWaitIntegrationsAfterTimeout(t *testing.T) {
	sh := New()
	in := sh.shared.integr
	// Nothing to wait for is finished at once, whatever the timeout.
	if !sh.WaitIntegrations(0) {
		t.Fatal("nothing runs, yet WaitIntegrations timed out")
	}
	for i := 0; i < 20000; i++ {
		release := make(chan struct{})
		in.background(func() { <-release })
		if sh.WaitIntegrations(0) {
			t.Fatal("WaitIntegrations reported running work as finished")
		}
		close(release)
		in.background(func() {})
	}
	if !sh.WaitIntegrations(10 * time.Second) {
		t.Fatal("background work did not finish")
	}
}

func TestIntegSplit0(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a\x00", []string{"a"}},
		{"a b\x00c\nd\x00", []string{"a b", "c\nd"}},
		{"a\x00\x00b", []string{"a", "b"}},
	}
	for _, c := range cases {
		if got := integSplit0(c.in); !equalLines(got, c.want) {
			t.Errorf("integSplit0(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
