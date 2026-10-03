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

func TestWaitStatus(t *testing.T) {
	cases := []struct{ src, want string }{
		// no operands: wait for everything, return 0
		{"sh -c 'exit 7' &\nwait; echo st=$?", "st=0\n"},
		// in-process jobs have a status too
		{"(exit 3) &\nwait %1; echo st=$?", "st=3\n"},
		{"f() { return 4; }; f &\nwait %1; echo st=$?", "st=4\n"},
		{"echo hi | (read x; exit 5) &\nwait %1; echo st=$?", "st=5\n"},
		{"sh -c 'exit 5' &\nwait $!; echo st=$?", "st=5\n"},
		// the status of the last job named
		{"sh -c 'exit 2' &\nsh -c 'sleep 0.1; exit 6' &\nwait %1 %2; echo st=$?", "st=6\n"},
		// -n: whichever finishes first
		{"sh -c 'sleep 0.3; exit 2' &\nsh -c 'exit 6' &\nwait -n; echo first=$?; wait; echo all=$?", "first=6\nall=0\n"},
		// a job stopped and continued is waited for until it exits
		{"sh -c 'sleep 0.3; exit 3' &\nkill -STOP %1; sleep 0.1; kill -CONT %1\nwait %1; echo st=$?", "st=3\n"},
		// $! doesn't name an earlier, unrelated process
		{"sh -c 'exit 1' &\nwait\n{ :; } &\necho \"bg=[$!]\"; wait", "bg=[]\n"},
	}
	for _, c := range cases {
		out, errOut, _ := runScriptTimeout(t, c.src, 5*time.Second)
		if out != c.want {
			t.Errorf("%q:\ngot  %q (stderr %q)\nwant %q", c.src, out, errOut, c.want)
		}
	}
}

// Ctrl-C stops `wait`, even while the job keeps running.
func TestWaitInterrupt(t *testing.T) {
	sh := New()
	sh.Init()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	io := &IO{In: fileStream(null, false), Out: fileStream(null, false), Err: fileStream(null, false)}
	time.AfterFunc(200*time.Millisecond, sh.Interrupt)
	start := time.Now()
	st := sh.RunString("sleep 5 &\nwait", "test", io)
	if d := time.Since(start); st != 130 || d > 3*time.Second {
		t.Errorf("wait returned %d after %v, want 130 promptly", st, d)
	}
	sh.ClearInterrupt()
	sh.RunString("kill %1; wait", "test", io)
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
