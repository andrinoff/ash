package shell

import (
	"strings"
	"testing"
)

// runScript runs src in a fresh non-interactive shell and returns what
// it wrote to stdout and stderr, and its exit status.
func runScript(t *testing.T, src string) (string, string, int) {
	t.Helper()
	return runScriptIn(t, src, "")
}

// runScriptIn is runScript with stdin contents.
func runScriptIn(t *testing.T, src, stdin string) (string, string, int) {
	t.Helper()
	sh := New()
	sh.Init()
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	io := &IO{
		In:  &Stream{R: strings.NewReader(stdin)},
		Out: &Stream{W: out},
		Err: &Stream{W: errb},
	}
	status := sh.RunString(src, "test", io)
	if exit, st := sh.ShouldExit(); exit {
		status = st
	}
	return out.String(), errb.String(), status
}

// runFish is runScript for fish-syntax source (fish quoting rules).
func runFish(t *testing.T, src string) (string, string, int) {
	t.Helper()
	sh := New()
	sh.Init()
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	io := &IO{In: &Stream{R: strings.NewReader("")}, Out: &Stream{W: out}, Err: &Stream{W: errb}}
	status := sh.RunString(src, "test.fish", io)
	return out.String(), errb.String(), status
}

func TestHelpers(t *testing.T) {
	out, _, st := runScript(t, "echo hi; x=(a b); echo $x[2] | tr a-z A-Z; false")
	if out != "hi\nB\n" || st != 1 {
		t.Fatalf("got %q %d", out, st)
	}
}
