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

func TestPopdAndDirsArguments(t *testing.T) {
	keepWd(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"a", "b", "c"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// popd ignored +N, -N and -n and popped the top instead; dirs
	// ignored -v, -p and +N.
	out, errOut, _ := runScript(t, `cd `+Quote(dir)+`
pushd a >/dev/null; pushd ../b >/dev/null; pushd ../c >/dev/null
dirs -v; dirs -p | wc -l | tr -d ' '; dirs +1; dirs -0
popd +1 >/dev/null; dirs; pwd
popd -0 >/dev/null; dirs
popd -n >/dev/null; dirs; pwd
popd +5; echo $?`)
	r := strings.NewReplacer(dir, "D")
	want := " 0  D/c\n 1  D/b\n 2  D/a\n 3  D\n4\nD/b\nD\n" +
		"D/c D/a D\nD/c\nD/c D/a\nD/c\nD/c\n1\n"
	if r.Replace(out) != want || !strings.Contains(errOut, "+5: directory stack index out of range") {
		t.Fatalf("got %q (stderr %q), want %q", r.Replace(out), errOut, want)
	}
}

func TestDirsTildeOnlyAtBoundary(t *testing.T) {
	keepWd(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"h", "hx"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	out, _, _ := runScript(t, `HOME=`+Quote(filepath.Join(dir, "h"))+`; cd `+Quote(filepath.Join(dir, "hx"))+`; dirs; cd ../h; dirs; HOME=/; cd /usr; dirs`)
	if want := filepath.Join(dir, "hx") + "\n~\n/usr\n"; out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}
