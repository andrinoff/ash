package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSourceArgsRestored(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "args.sh")
	if err := os.WriteFile(p, []byte("echo \"in: $# [$1] [$2]\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// With no positional parameters before, the file's arguments used
	// to stay behind after it returned.
	out, errOut, _ := runScript(t, `. `+Quote(p)+` x y; echo "after: $# [$1]"
set -- a b; . `+Quote(p)+` p; echo "after: $# [$1]"
f() { . `+Quote(p)+` q; echo "f: $# [$1]"; }; f m n
begin; . `+Quote(p)+` r; end; echo "block: $# [$1]"`)
	want := "in: 2 [x] [y]\nafter: 0 []\nin: 1 [p] []\nafter: 2 [a]\nin: 1 [q] []\nf: 2 [m]\nin: 1 [r] []\nblock: 2 [a]\n"
	if out != want {
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
}
