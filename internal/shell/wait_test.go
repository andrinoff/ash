package shell

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// A foreground job stopped with SIGSTOP (kill -STOP from another
// terminal) must be reported as stopped, not waited on until it is
// continued.
func TestSigstopStopsForegroundJob(t *testing.T) {
	sh := New()
	sh.Init()
	sh.JobControl = true // jobs get process groups, as in an interactive shell
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	// files, like a terminal: a stopped job keeps pipes open
	io := &IO{In: fileStream(null, false), Out: fileStream(null, false), Err: fileStream(null, false)}
	done := make(chan int, 1)
	go func() { done <- sh.RunString(`sh -c 'kill -STOP $$; exit 7'`, "test", io) }()
	select {
	case st := <-done:
		if want := 128 + int(syscall.SIGSTOP); st != want {
			t.Errorf("status %d, want %d", st, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the shell kept waiting for a stopped job")
	}
	if !sh.StoppedJobs() {
		t.Error("the job isn't listed as stopped")
	}
	sh.HangupJobs()
}
