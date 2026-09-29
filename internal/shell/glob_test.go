package shell

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/andrinoff/ash/internal/syntax"
)

// globTree makes a small directory tree for glob tests.
func globTree(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func globIn(dir, pat string) []string {
	sh := New()
	sh.dir = dir
	return sh.glob(pat)
}

// expandIn expands the arguments of the command src with dir as the
// current directory.
func expandIn(t *testing.T, dir, src string) []string {
	t.Helper()
	sh := New()
	sh.dir = dir
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	args, err := sh.expandFields(f.Stmts[0].Cmd.(*syntax.CallExpr).Args, sh.StdIO(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return args
}

func TestGlobEscapedStarIsNotDoubleStar(t *testing.T) {
	dir := globTree(t, "c*d", "cxd", "sub/c*e")
	for _, c := range []struct {
		src  string
		want []string
	}{
		{`echo c'*'*`, []string{"echo", "c*d"}},
		{`echo c\**`, []string{"echo", "c*d"}},
		{`echo "c*"*`, []string{"echo", "c*d"}},
		{`echo c**`, []string{"echo", "c*d", "cxd", "sub/c*e"}},
	} {
		if got := expandIn(t, dir, c.src); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.src, got, c.want)
		}
	}
}

// Quoted ! ^ and - inside brackets are literal, as in bash.
func TestQuotedBracketSpecials(t *testing.T) {
	src := `for s in a b '!' '^' - z m; do
		case $s in [\!a]) r1=y;; *) r1=n;; esac
		case $s in [\^a]) r2=y;; *) r2=n;; esac
		case $s in [a\-z]) r3=y;; *) r3=n;; esac
		case $s in ["!"a]) r4=y;; *) r4=n;; esac
		case $s in [a"-"z]) r5=y;; *) r5=n;; esac
		p='!'; case $s in ["$p"a]) r6=y;; *) r6=n;; esac
		[[ $s == [\!a] ]] && r7=y || r7=n
		x=$s; y=${x#[\!a]}; [ -z "$y" ] && r8=y || r8=n
		echo "$s $r1 $r2 $r3 $r4 $r5 $r6 $r7 $r8"
	done`
	want := "a y y y y y y y y\nb n n n n n n n n\n! y n n y n y y y\n^ n y n n n n n n\n- n n y n y n n n\nz n n y n y n n n\nm n n n n n n n n\n"
	if out, errOut, _ := runScript(t, src); out != want {
		t.Errorf("got\n%s(stderr %q), want\n%s", out, errOut, want)
	}
}

// The expected order is fish 4.8's for the same file names.
func TestNaturalSortIgnoresCase(t *testing.T) {
	got := []string{"file10", "File3", "file2", "file01", "file1", "file001", "FILE1", "Apricot", "apple", "aZ", "ab", "a-b", "a_b", "10", "9", "_x", "é", "z"}
	want := []string{"9", "10", "_x", "a-b", "a_b", "ab", "apple", "Apricot", "aZ", "FILE1", "file001", "file01", "file1", "file2", "File3", "file10", "z", "é"}
	NaturalSort(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestGlobTrailingDoubleStar(t *testing.T) {
	dir := globTree(t, "a.txt", "sub/s.txt", "sub/deep/d.txt", "sub/.dot", ".hid/h.txt")
	if err := os.Symlink("sub", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		pat  string
		want []string
	}{
		{"**", []string{"a.txt", "link", "sub", "sub/deep", "sub/deep/d.txt", "sub/s.txt"}},
		{"sub/**", []string{"sub/deep", "sub/deep/d.txt", "sub/s.txt"}},
		{".hid/**", []string{".hid/h.txt"}},
		{"**/*.txt", []string{"a.txt", "sub/deep/d.txt", "sub/s.txt"}},
		{"**/", []string{"sub/", "sub/deep/"}},
		{"**.txt", []string{"a.txt", "sub/deep/d.txt", "sub/s.txt"}},
		{dir + "/sub/**", []string{dir + "/sub/deep", dir + "/sub/deep/d.txt", dir + "/sub/s.txt"}},
	} {
		if got := globIn(dir, c.pat); !reflect.DeepEqual(got, c.want) {
			t.Errorf("glob %q = %q, want %q", c.pat, got, c.want)
		}
	}
}
