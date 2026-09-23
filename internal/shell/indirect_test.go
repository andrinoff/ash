package shell

import "testing"

// fish's $$name expands the variables that $name names.
func TestFishIndirect(t *testing.T) {
	out, errOut, _ := runFish(t, `set a b; set b 1 2; set p b c; set c 3
echo $$a; echo "$$a"; echo $$p; echo $$p[2]; echo x$$a`)
	if want := "1 2\n1 2\n1 2 3\n3\nx1 x2\n"; out != want {
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
}
