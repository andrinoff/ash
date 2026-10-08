package shell

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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
		// a command with redirections is in the same process group
		"sh -c 'echo $$ > %s; exec sleep 30' | sleep 30 >/dev/null",
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

// A backgrounded block or function runs in the shell: ctrl-c in the
// foreground must leave it alone, and kill %N must stop it.
func TestInProcessBackgroundJobs(t *testing.T) {
	sh := New()
	sh.Init()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	out := &lockedBuffer{}
	io := &IO{In: fileStream(null, false), Out: &Stream{W: out}, Err: &Stream{W: out}}
	run := func(src string) int {
		done := make(chan int, 1)
		go func() { done <- sh.RunString(src, "test", io) }()
		select {
		case st := <-done:
			return st
		case <-time.After(5 * time.Second):
			t.Fatalf("%q did not finish", src)
		}
		return 0
	}
	run("while true; do sleep 0.05; done &\nf() { sleep 30; echo after-sleep; }; f &")
	time.AfterFunc(200*time.Millisecond, sh.Interrupt) // ctrl-c
	run("while true; do :; done")
	time.Sleep(200 * time.Millisecond) // (the shell clears it at the next prompt)
	sh.ClearInterrupt()
	time.Sleep(100 * time.Millisecond)
	run("jobs")
	if got := out.String(); strings.Count(got, "running") != 2 {
		t.Fatalf("background jobs after ctrl-c in the foreground:\n%s", got)
	}
	run("kill %1 %2; wait %1 %2; echo waited")
	if got := out.String(); !strings.HasSuffix(got, "waited\n") || strings.Contains(got, "after-sleep") {
		t.Errorf("after kill: %q", got)
	}
	// exiting the shell hangs up their commands too
	run("f &")
	time.Sleep(100 * time.Millisecond)
	sh.HangupJobs()
	run("wait %1; echo hung-up")
	if got := out.String(); !strings.HasSuffix(got, "hung-up\n") || strings.Contains(got, "after-sleep") {
		t.Errorf("after hangup: %q", got)
	}
}

// Remembered command locations must not go stale.
func TestCommandLookupCache(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"a", "b", "x/bin", "y/bin"} {
		os.MkdirAll(dir+"/"+d, 0o755)
	}
	write := func(path, msg string) {
		if err := os.WriteFile(dir+"/"+path, []byte("#!/bin/sh\necho "+msg+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("x/bin/tool", "from-x")
	write("y/bin/tool", "from-y")
	write("a/moved", "from-a")
	// (in a subshell: cd in the main shell changes the test's directory)
	src := "(cd " + dir + "\n" +
		// a relative PATH entry finds a different command after cd
		"PATH=bin:" + dir + "/a:" + dir + "/b:$PATH\ncd x; tool; cd ../y; tool; cd ..\n" +
		// a command that moved to a later PATH entry is found there
		"moved; mv a/moved b/moved; moved)"
	out, errOut, _ := runScriptTimeout(t, src, 5*time.Second)
	if want := "from-x\nfrom-y\nfrom-a\nfrom-a\n"; out != want {
		t.Errorf("got %q (stderr %q), want %q", out, errOut, want)
	}
}

// kill -- -PGID signals a process group.
func TestKillProcessGroup(t *testing.T) {
	// (job control: the pipeline's processes get a group of their own)
	out, errOut, _ := runScriptSetup(t, "sh -c 'echo $$; exec sleep 5' </dev/null 2>/dev/null | { read pid; kill -- -$pid; echo st=$?; }", 5*time.Second, func(sh *Shell) { sh.JobControl = true })
	if out != "st=0\n" {
		t.Errorf("got %q (stderr %q)", out, errOut)
	}
}

func TestTrapDashDash(t *testing.T) {
	out, errOut, st := runScript(t, "trap 'echo x' USR2; trap --; trap - USR2; echo after")
	if !strings.Contains(out, "USR2") || !strings.HasSuffix(out, "after\n") || st != 0 {
		t.Errorf("got %q %q %d", out, errOut, st)
	}
}

// A trapped signal runs its handler between commands; it doesn't abort
// the script like ctrl-c.
func TestSignalTraps(t *testing.T) {
	log := t.TempDir() + "/log"
	cases := []struct{ src, want string }{
		{"(sleep 0.2; kill -USR1 $$) &\ni=0; while [ $i -lt 5 ]; do sleep 0.1; i=$((i+1)); done; echo done i=$i", "done i=5\n"},
		{"false; kill -USR1 $$; echo after $?", "after 0\n"},
		// wait returns early, then the handler runs
		{"(sleep 0.2; kill -USR1 $$) &\nsleep 3 &\nwait %2; echo wait=$?; kill %2", fmt.Sprintf("wait=%d\n", 128+int(syscall.SIGUSR1))},
	}
	for _, c := range cases {
		os.Remove(log)
		out, errOut, _ := runScriptTimeout(t, "trap 'echo got >> "+log+"' USR1\n"+c.src, 5*time.Second)
		b, _ := os.ReadFile(log)
		if out != c.want || string(b) != "got\n" {
			t.Errorf("%q:\ngot  %q (stderr %q), handler wrote %q\nwant %q", c.src, out, errOut, b, c.want)
		}
	}
}

// Opening a FIFO waits for its other end, which may come later: starting a
// job must not wait for it.
func TestFIFORedirections(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"f1", "f2"} {
		if err := unix.Mkfifo(dir+"/"+f, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src := "cat < " + dir + "/f1 > " + dir + "/out &\necho hello > " + dir + "/f1; wait; cat " + dir + "/out\n" +
		"(echo hi > " + dir + "/f2) | cat < " + dir + "/f2"
	out, errOut, _ := runScriptTimeout(t, src, 5*time.Second)
	if out != "hello\nhi\n" {
		t.Errorf("got %q (stderr %q)", out, errOut)
	}
}

// The prompt reports a background job that stopped (on terminal input).
func TestNotifyStoppedBackgroundJob(t *testing.T) {
	sh := New()
	sh.Init()
	sh.Interactive = true
	b := &lockedBuffer{}
	io := &IO{In: &Stream{}, Out: &Stream{W: b}, Err: &Stream{W: b}}
	sh.Stderr, _ = os.Open(os.DevNull)
	defer sh.Stderr.Close()
	sh.RunString("sleep 30 &\nkill -STOP %1; sleep 0.2", "test", io)
	var note strings.Builder
	sh.NotifyJobs(&note)
	sh.RunString("kill -KILL %1; wait", "test", io)
	if !strings.Contains(note.String(), "job 1, 'sleep 30 &' has stopped") {
		t.Errorf("got %q", note.String())
	}
}

func TestNoclobber(t *testing.T) {
	dir := t.TempDir()
	f := dir + "/f"
	out, errOut, _ := runScript(t, "set -C; echo a > "+f+"; echo b > "+f+"; echo st=$?; echo c >| "+f+"; echo d &> "+f+"; echo st=$?; echo e > /dev/null; echo st=$?; cat "+f)
	if want := "st=1\nst=1\nst=0\nc\n"; out != want {
		t.Errorf("got %q (stderr %q), want %q", out, errOut, want)
	}
	// the lock file idiom: only one of several racing shells gets the lock
	lock := dir + "/lock"
	var wg sync.WaitGroup
	var won atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sh := New()
			b := &lockedBuffer{}
			io := &IO{In: &Stream{}, Out: &Stream{W: b}, Err: &Stream{W: b}}
			if sh.RunString("set -C; echo $$ > "+lock, "test", io) == 0 {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := won.Load(); n != 1 {
		t.Errorf("%d shells got the lock", n)
	}
}

// openFds counts this process's open descriptors.
func openFds() int {
	n := 0
	for fd := 0; fd < 1024; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			n++
		}
	}
	return n
}

// The shell's copies of a background pipeline's <(...) and >(...) pipes
// must be closed once its commands are started.
func TestProcSubstFdsInBackground(t *testing.T) {
	before := openFds()
	runScriptTimeout(t, "for i in 1 2 3 4 5; do cat <(echo a) | cat >/dev/null &\necho a | tee >(cat >/dev/null) >/dev/null &\ndone\nwait; sleep 0.2", 5*time.Second)
	if n := openFds() - before; n > 2 {
		t.Errorf("%d more descriptors open", n)
	}
}

// zombies counts this process's children that have exited unreaped.
func zombies(t *testing.T) int {
	t.Helper()
	out, errOut, st := runScript(t, fmt.Sprintf("ps -A -o ppid=,stat= | awk '$1 == %d && $2 ~ /Z/' | wc -l", os.Getpid()))
	var n int
	if _, err := fmt.Sscan(out, &n); err != nil || st != 0 {
		t.Fatalf("counting zombies: %q %q %d", out, errOut, st)
	}
	return n
}

// A script that starts background jobs and never waits for them must not
// pile up zombies.
func TestBackgroundJobsReaped(t *testing.T) {
	before := zombies(t)
	runScriptTimeout(t, "for i in 1 2 3 4 5 6 7 8 9 10; do /usr/bin/true &\ndone\nsleep 0.3; /usr/bin/true &\nsleep 0.3", 5*time.Second)
	if n := zombies(t) - before; n > 1 {
		t.Errorf("%d zombies left", n)
	}
	// and disowned jobs are reaped too
	before = zombies(t)
	runScriptTimeout(t, "/usr/bin/true &!\n/usr/bin/true | /usr/bin/true &!\nsleep 0.3; jobs\n/usr/bin/true &\nsleep 0.3", 5*time.Second)
	if n := zombies(t) - before; n > 1 {
		t.Errorf("%d zombies left by disowned jobs", n)
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
		// a command with redirections is a process of its own
		{"sh -c 'exit 4' >/dev/null &\ncase $! in ''|0) echo no-pid;; *) echo pid;; esac\nwait $!; echo st=$?", "pid\nst=4\n"},
		{"cat </nonexistent &\nwait %1; echo st=$?", "st=1\n"},
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
		// a redirection copies a descriptor from the table to another number
		{"exec 3>" + file + "; echo a 4>&3 >&4; sh -c 'echo b >&5' 5>&3; { echo c >&6; } 6>&3; f() { echo d >&7; }; f 7>&3; exec 3>&-; cat " + file, "a\nb\nc\nd\n", ""},
		{"exec 3>&1; echo out 4>&3 >&4; exec 4>&3; echo four >&4; exec 3>&- 4>&-", "out\nfour\n", ""},
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

// Descriptors the process opened itself (close-on-exec, like the runtime's
// poller) are never taken for ones ash inherited.
func TestInheritedDescriptors(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd := int(f.Fd())
	has := func() bool {
		for _, n := range openWithoutCloexec() {
			if n == fd {
				return true
			}
		}
		return false
	}
	if has() {
		t.Errorf("fd %d (close-on-exec) taken for an inherited one", fd)
	}
	unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0)
	if !has() {
		t.Errorf("fd %d (inheritable) not found", fd)
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
