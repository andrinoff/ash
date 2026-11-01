package repl

import (
	"strconv"
	"testing"

	"github.com/andrinoff/ash/internal/shell"
)

func TestPreviewHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home) // the test cds there; this puts the process back after
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	sh := shell.New()
	sh.Init()
	r := &REPL{sh: sh}
	io := &shell.IO{In: &shell.Stream{}, Out: &shell.Stream{}, Err: &shell.Stream{}}
	run := func(src string) { sh.RunString(src, "test", io) }

	run("x=one")
	if got := r.preview("echo $x", 7); got != "" {
		t.Errorf("before the first refresh: %q", got)
	}
	r.refreshPreview()
	if got := r.preview("echo $x", 7); got != "one" {
		t.Errorf("got %q", got)
	}
	// the preview sees the shell as it was at the last refresh
	run("x=two")
	if got := r.preview("echo $x", 7); got != "one" {
		t.Errorf("before refresh: %q", got)
	}
	r.refreshPreview()
	if got := r.preview("echo $x", 7); got != "two" {
		t.Errorf("after refresh: %q", got)
	}
	run("ash_preview=0")
	r.refreshPreview()
	if got := r.preview("echo $x", 7); got != "" {
		t.Errorf("ash_preview=0: %q", got)
	}
	run("ash_preview=1")
	r.refreshPreview()

	// The editor calls the hook in the background while the main
	// goroutine runs commands. Run with -race.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			r.preview("echo $x$y ~ ${z:=1} {a,b}", 8)
		}
	}()
	for i := 0; i < 300; i++ {
		run("y=$x; x=v" + strconv.Itoa(i) + "; f() { :; }; cd " + sh.HomeDir())
		if i%10 == 0 {
			r.refreshPreview()
		}
	}
	<-done
}

// BenchmarkRefreshPreview is the preview's cost per prompt: forking the
// shell, and the copy of the variables the shell's next assignment makes.
func BenchmarkRefreshPreview(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	sh := shell.New()
	sh.Init()
	r := &REPL{sh: sh}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.refreshPreview()
		sh.SetString("x", "1")
	}
}
