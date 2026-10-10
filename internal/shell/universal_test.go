package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runUniversal runs src in a fresh shell whose config directory is dir,
// loading universal variables first if load is set (as the REPL does).
func runUniversal(t *testing.T, dir string, load bool, src string) (string, string, int) {
	t.Helper()
	sh := New()
	sh.Init()
	sh.SetVar("XDG_CONFIG_HOME", []string{dir}, setOpts{scope: setGlobal, export: 1})
	if load {
		sh.LoadUniversal()
	}
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	io := &IO{In: &Stream{}, Out: &Stream{W: out}, Err: &Stream{W: errb}}
	st := sh.RunString(src, "test.fish", io)
	return out.String(), errb.String(), st
}

func TestUniversalSaveKeepsOtherVariables(t *testing.T) {
	dir := t.TempDir()
	runUniversal(t, dir, true, `set -U a 1; set -U b x y; set -U e ''; set -U f`)
	// A shell that never loaded the file (a script, or a session
	// started earlier) used to rewrite it with only its own variables.
	runUniversal(t, dir, false, `set -U c 3`)
	out, errOut, _ := runUniversal(t, dir, true, `echo $a; echo $b; echo $c; echo (count $e) (count $f)`)
	if want := "1\nx y\n3\n1 0\n"; out != want {
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
}

func TestUniversalChangedWithoutScope(t *testing.T) {
	dir := t.TempDir()
	runUniversal(t, dir, true, `set -U a 1; set -U b 2; set -U c 3`)
	// fish: set and set -e on a universal variable change the saved one
	runUniversal(t, dir, true, `set a changed; set -e b; set -a c 4`)
	out, errOut, _ := runUniversal(t, dir, true, `echo $a; set -q b; echo $status; echo $c`)
	if want := "changed\n1\n3 4\n"; out != want {
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
	// set -g shadows it for this session only; erasing the shadow
	// uncovers the universal value again.
	out, _, _ = runUniversal(t, dir, true, `set -g a shadow; echo $a; set -e a; echo $a`)
	if want := "shadow\nchanged\n"; out != want {
		t.Fatalf("shadow: got %q, want %q", out, want)
	}
	out, _, _ = runUniversal(t, dir, true, `echo $a; set -Ue a; echo $status; set -Ue a; echo $status; set -q a; echo $status`)
	if want := "changed\n0\n4\n1\n"; out != want {
		t.Fatalf("erase: got %q, want %q", out, want)
	}
	b, err := os.ReadFile(filepath.Join(dir, "ash", "variables"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), " a\x1e") || !strings.Contains(string(b), "- c\x1e3\x1e4\n") {
		t.Fatalf("file: %q", b)
	}
}
