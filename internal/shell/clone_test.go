package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A background subshell's cd must not write into the parent's $dirprev
// (they used to share the slice's spare capacity).
func TestCloneKeepsDirprevSeparate(t *testing.T) {
	d := t.TempDir()
	for _, n := range []string{"1", "2", "3", "4", "5", "6", "a", "b", "c", "d"} {
		if err := os.Mkdir(filepath.Join(d, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Run in a subshell so cd doesn't change the test's directory.
	src := strings.ReplaceAll(`(
		cd D/1; cd D/2; cd D/3; cd D/4; cd D/5; cd D/6
		{ cd D/a; sleep 0.2; cd D/b; } &
		cd D/c; cd D/d
		wait
		echo $dirprev[-1]
	)`, "D", d)
	out, errOut, _ := runScript(t, src)
	if want := filepath.Join(d, "c") + "\n"; out != want {
		t.Errorf("got %q (stderr %q), want %q", out, errOut, want)
	}
}
