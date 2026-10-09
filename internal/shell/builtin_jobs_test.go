package shell

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// runJobsScript is runScript with /dev/null as a real stdin file, so
// background commands don't share a Go reader.
func runJobsScript(t *testing.T, src string) (string, string, int) {
	t.Helper()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	sh := New()
	sh.Init()
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	io := &IO{In: fileStream(null, false), Out: &Stream{W: out}, Err: &Stream{W: errb}}
	status := sh.RunString(src, "test", io)
	if exit, st := sh.ShouldExit(); exit {
		status = st
	}
	return out.String(), errb.String(), status
}

func TestTrapRunsBetweenCommands(t *testing.T) {
	// The handler used to be queued but never run, and the signal
	// aborted the script instead.
	out, errOut, st := runScript(t, `
trap 'echo got USR1' USR1
kill -USR1 $$
echo after
for i in 1 2; do kill -USR1 $$; echo loop $i; done
f() { kill -s USR1 $$; echo in f; }
f
`)
	want := "got USR1\nafter\ngot USR1\nloop 1\ngot USR1\nloop 2\ngot USR1\nin f\n"
	if out != want || st != 0 {
		t.Fatalf("got %q (stderr %q) status %d, want %q", out, errOut, st, want)
	}
	out, _, st = runScript(t, `trap 'echo t; exit 7' USR2; kill -USR2 $$; echo notreached`)
	if out != "t\n" || st != 7 {
		t.Fatalf("exit in trap: got %q status %d", out, st)
	}
}

func TestTrapListing(t *testing.T) {
	// trap -- used to index past the end of its arguments.
	out, errOut, st := runScript(t, `trap --; trap 'x' USR1 USR2; trap -p USR2; trap - USR1; trap; trap 'y' 99; echo $?`)
	want := "trap -- x USR2\ntrap -- x USR2\n1\n"
	if out != want || st != 0 || !strings.Contains(errOut, "99: invalid signal") {
		t.Fatalf("got %q (stderr %q) status %d, want %q", out, errOut, st, want)
	}
}

func TestKillDoubleDash(t *testing.T) {
	out, errOut, _ := runJobsScript(t, "sleep 5 &\nkill -- $!; wait $!; echo \"status $?\"")
	if out != "status 143\n" {
		t.Fatalf("got %q (stderr %q)", out, errOut)
	}
}

func TestWaitReportsTrapFromFinishedJob(t *testing.T) {
	// The job sends the signal and ends at once; wait still has to say it
	// was interrupted (128+signal), as bash does, and the handler runs.
	out, errOut, _ := runJobsScript(t, `
trap 'echo handled' USR1
{ sleep 0.1; kill -USR1 $$; } &
wait
echo "wait=$?"
`)
	want := "handled\nwait=" + strconv.Itoa(128+int(syscall.SIGUSR1)) + "\n"
	if out != want || errOut != "" {
		t.Errorf("got %q (stderr %q), want %q", out, errOut, want)
	}
}
