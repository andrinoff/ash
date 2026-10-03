package shell

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runScriptTimeout is runScript with stdin from /dev/null, failing the
// test instead of hanging.
func runScriptTimeout(t *testing.T, src string, d time.Duration) (string, string, int) {
	t.Helper()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { null.Close() })
	type result struct {
		out, errOut string
		status      int
	}
	ch := make(chan result, 1)
	go func() {
		sh := New()
		sh.Init()
		out, errb := &lockedBuffer{}, &lockedBuffer{}
		io := &IO{In: fileStream(null, false), Out: &Stream{W: out}, Err: &Stream{W: errb}}
		st := sh.RunString(src, "test", io)
		if exit, s := sh.ShouldExit(); exit {
			st = s
		}
		ch <- result{out.String(), errb.String(), st}
	}()
	select {
	case r := <-ch:
		return r.out, r.errOut, r.status
	case <-time.After(d):
		t.Fatalf("%q did not finish within %v", src, d)
	}
	return "", "", 0
}

// A child stopped with SIGSTOP is stopped, and one continued with SIGCONT
// is running again (darwin's wait statuses for these were read backwards).
func TestStoppedAndContinuedChildren(t *testing.T) {
	// (real files for its stdio: the test's buffers would wait for it to exit)
	out, errOut, _ := runScriptTimeout(t, "sh -c 'kill -STOP $$; exit 3' </dev/null >/dev/null 2>&1\necho stopped=$?\nkill -KILL %1", 5*time.Second)
	if want := fmt.Sprintf("stopped=%d\n", 128+int(syscall.SIGSTOP)); out != want {
		t.Errorf("foreground stop: got %q (stderr %q), want %q", out, errOut, want)
	}
	out, errOut, _ = runScriptTimeout(t, "sleep 5 &\nkill -STOP %1; sleep 0.2; jobs\nkill -CONT %1; sleep 0.2; jobs\nkill %1", 5*time.Second)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "stopped") || !strings.Contains(lines[1], "running") {
		t.Errorf("background stop/continue: got %q (stderr %q)", out, errOut)
	}
}

// An external command that doesn't read its stdin must leave the rest of
// a heredoc for the commands after it, as it would with a file.
func TestHeredocSharedWithExternals(t *testing.T) {
	cases := []struct{ src, want string }{
		{"while read line; do echo \"got $line\"; /usr/bin/true; done <<EOF\na\nb\nc\nEOF\n", "got a\ngot b\ngot c\n"},
		{"while read l; do echo $l; /usr/bin/true; done <<< \"x\ny\"", "x\ny\n"},
		{"{ read a; echo first=$a; cat; } <<EOF\none\ntwo\nthree\nEOF\n", "first=one\ntwo\nthree\n"},
	}
	for _, c := range cases {
		out, errOut, _ := runScript(t, c.src)
		if out != c.want {
			t.Errorf("%q:\ngot  %q (stderr %q)\nwant %q", c.src, out, errOut, c.want)
		}
	}
}
