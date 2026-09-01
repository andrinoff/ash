package shell

import (
	"strings"
	"testing"
)

func TestDirenv(t *testing.T) {
	it := newIntegTest(t)
	it.tool("direnv", `cat "$LOG.reply" 2>/dev/null; printf 'gone=%s\n' "${GONE-unset}" >> "$LOG"`)
	it.mustRun("GONE=1; export GONE; LOCAL=keep; integrate direnv")
	if calls := it.calls(); calls != nil {
		t.Fatalf("direnv ran before the first prompt: %q", calls)
	}

	it.write("log.reply", `{"FOO":"bar baz","GONE":null,"EMPTY":"","DIRENV_DIR":"-/proj","PATH":"/proj/bin:/usr/bin:/bin","bad name":"x"}`)
	it.sh.RunPrePrompt()
	// The variables reach direnv through the environment.
	if calls := it.calls(); !equalLines(calls, []string{"direnv [export] [json]", "gone=1"}) {
		t.Errorf("calls %q", calls)
	}
	for name, want := range map[string]string{"FOO": "bar baz", "EMPTY": "", "DIRENV_DIR": "-/proj", "PATH": "/proj/bin:/usr/bin:/bin", "LOCAL": "keep"} {
		if got := it.sh.Get(name); got != want {
			t.Errorf("%s=%q, want %q", name, got, want)
		}
	}
	if it.sh.IsSet("GONE") {
		t.Error("GONE is still set")
	}
	if p := it.sh.GetList("PATH"); len(p) != 3 {
		t.Errorf("PATH is not a path list: %q", p)
	}
	env := strings.Join(it.sh.Environ(), "\n")
	for _, kv := range []string{"FOO=bar baz", "DIRENV_DIR=-/proj", "EMPTY="} {
		if !strings.Contains(env, "\n"+kv+"\n") && !strings.HasPrefix(env, kv+"\n") && !strings.HasSuffix(env, "\n"+kv) {
			t.Errorf("%s is not exported", kv)
		}
	}

	// No output (nothing changed) and garbage change nothing.
	for _, reply := range []string{"", "not json"} {
		it.write("log.reply", reply)
		it.sh.RunPrePrompt()
		if it.sh.Get("FOO") != "bar baz" {
			t.Errorf("reply %q changed FOO", reply)
		}
		it.calls()
	}
}
