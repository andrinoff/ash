package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keepWd restores the test process's directory, which cd changes.
func keepWd(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
}

func TestCdPath(t *testing.T) {
	keepWd(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"d1", "cdp/d1", "cdp/.hid", "real"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	out, errOut, _ := runScript(t, `cd `+Quote(dir)+`
CDPATH=:cdp; cd d1; pwd; cd ..
CDPATH=cdp; cd .hid; pwd; cd ../..
cd -P link; pwd; cd ..; cd link; pwd`)
	r := strings.NewReplacer(dir, "D")
	// An empty CDPATH entry is the current directory and comes first;
	// names starting with a dot (but not ./) are looked up in CDPATH.
	if want := "D/d1\nD/cdp/.hid\nD/cdp/.hid\nD/real\nD/link\n"; r.Replace(out) != want {
		t.Fatalf("got %q (stderr %q), want %q", r.Replace(out), errOut, want)
	}
}
