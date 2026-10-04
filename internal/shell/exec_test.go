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
	return runScriptSetup(t, src, d, nil)
}

// runScriptSetup is runScriptTimeout with a hook to adjust the shell.
func runScriptSetup(t *testing.T, src string, d time.Duration, setup func(*Shell)) (string, string, int) {
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
		if setup != nil {
			setup(sh)
		}
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

// With job control a pipeline's processes get their own process group,
// which has the terminal, so ctrl-c reaches only them. Builtins in the
// same pipeline have to stop too instead of running on forever.
func TestInterruptReachesInProcessElements(t *testing.T) {
	for _, src := range []string{
		"while true; do :; done | sh -c 'echo $$ > %s; exec sleep 30'",
		"sh -c 'echo $$ > %s; exec sleep 30' | while true; do :; done",
	} {
		pidFile := t.TempDir() + "/pid"
		src = fmt.Sprintf(src, pidFile)
		go func() {
			// ctrl-c: SIGINT to the job's process group
			for i := 0; i < 100; i++ {
				time.Sleep(20 * time.Millisecond)
				b, err := os.ReadFile(pidFile)
				var pid int
				if _, err2 := fmt.Sscan(string(b), &pid); err == nil && err2 == nil && pid > 0 {
					time.Sleep(50 * time.Millisecond)
					syscall.Kill(-pid, syscall.SIGINT)
					return
				}
			}
		}()
		_, errOut, st := runScriptSetup(t, src+"; echo NOTREACHED", 5*time.Second, func(sh *Shell) { sh.JobControl = true })
		if st == 0 {
			t.Errorf("%q: status %d (stderr %q)", src, st, errOut)
		}
	}
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

func TestExecRedirections(t *testing.T) {
	dir := t.TempDir()
	file := dir + "/f"
	cases := []struct{ src, out, errOut string }{
		// the descriptor stays open for the rest of the script
		{"exec 3>" + file + "; i=0; while [ $i -lt 300 ]; do echo $i >&3; i=$((i+1)); done; exec 3>&-; wc -l < " + file + " | tr -d ' '", "300\n", ""},
		// and child processes get it
		{"exec 3>" + file + "; sh -c 'echo child >&3'; exec 3>&-; cat " + file, "child\n", ""},
		{"exec 4<" + file + "; read -u 4 l; echo \"l=$l\"; cat <&4", "l=child\n", ""},
		// even when a process substitution's pipe gets number 3
		{"exec 3>" + file + "; cat <(echo ps); echo x >&3; exec 3>&-; cat " + file, "ps\nx\n", ""},
		// saving and restoring stdout
		{"exec 3>&1; exec 1>/dev/null; echo hidden; exec 1>&3 3>&-; echo restored; echo bad >&3", "restored\n", "ash: 3: bad file descriptor\n"},
		// in a subshell it ends with the subshell
		{"(exec >/dev/null; echo hidden); echo visible", "visible\n", ""},
		{"x=$(exec 2>/dev/null; echo hi); echo $x; echo err >&2", "hi\n", "err\n"},
		{"echo a | { exec >/dev/null; cat; }; echo visible", "visible\n", ""},
		{"{ exec >/dev/null; } &\nwait; echo visible", "visible\n", ""},
		// in a function it lasts
		{"f() { exec 5>" + file + "; }; f 2>/dev/null; echo via5 >&5; exec 5>&-; cat " + file, "via5\n", ""},
		{"exec 3>" + file + "; f() { exec 3>" + file + "2; }; f 2>/dev/null; echo hi >&3; exec 3>&-; cat " + file + "; echo sep; cat " + file + "2", "sep\nhi\n", ""},
	}
	for _, c := range cases {
		out, errOut, _ := runScriptTimeout(t, c.src, 5*time.Second)
		if out != c.out || errOut != c.errOut {
			t.Errorf("%q:\ngot  %q %q\nwant %q %q", c.src, out, errOut, c.out, c.errOut)
		}
	}
}

// A script can't reach the process's own descriptors (the Go runtime's
// signal pipe and poller, the editor's files) through >&N.
func TestDupOfUnknownDescriptor(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "private")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, errOut, st := runScript(t, fmt.Sprintf("echo leaked >&%d", f.Fd()))
	if st != 1 || !strings.Contains(errOut, "bad file descriptor") {
		t.Errorf("got status %d, stderr %q", st, errOut)
	}
	b, _ := os.ReadFile(f.Name())
	if len(b) != 0 {
		t.Errorf("wrote %q to the process's own descriptor", b)
	}
	if _, err := f.Stat(); err != nil {
		t.Errorf("descriptor was closed: %v", err)
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
