package shell

import "testing"

func TestTestLargeIntegers(t *testing.T) {
	// Comparing through float64 made these equal.
	out, errOut, _ := runScript(t, `
[ 9223372036854775807 -gt 9223372036854775806 ]; echo $?
[ 1700000000000000001 -eq 1700000000000000000 ]; echo $?
[[ 9007199254740993 -ne 9007199254740992 ]]; echo $?
[ 1.5 -lt 2 ]; echo $?
[ nan -eq nan ] 2>/dev/null; echo $?
`)
	if want := "0\n1\n0\n0\n2\n"; out != want {
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
}
