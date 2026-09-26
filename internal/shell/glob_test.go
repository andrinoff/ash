package shell

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
