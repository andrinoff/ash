package shell

import "testing"

func TestDeclaredWithoutValueStaysUnset(t *testing.T) {
	// export B and local x used to create empty variables: B= went into
	// the environment of child commands, and ${x-unset} said x was set.
	out, errOut, _ := runJobsScript(t, `
export A=1 B; echo "[$A][${B-unset}]"; env | grep -c '^B='; B=2; env | grep '^B='
f() { local x; echo "[${x-unset}]"; x=2; echo "[$x]"; }; x=1; f; echo $x
f() { local x; [ -v x ] && echo set || echo notset; x+=foo; echo $x; }; f
export X; export -p | grep -x 'export X'
`)
	want := "[1][unset]\n0\nB=2\n[unset]\n[2]\n1\nnotset\nfoo\nexport X\n"
	if out != want {
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
}
