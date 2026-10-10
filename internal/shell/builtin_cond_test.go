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

func TestDoubleBracketArithmeticOperands(t *testing.T) {
	out, errOut, _ := runScript(t, `
x=5
[[ x -eq 5 ]]; echo $?
[[ 1+1 -eq 2 ]]; echo $?
[[ "" -eq 0 ]]; echo $?
[[ $unset -gt 0 ]]; echo $?
[[ 08 -eq 8 ]]; echo $?
[ "" -eq 0 ] 2>/dev/null; echo $?
`)
	if want := "0\n0\n0\n1\n0\n2\n"; out != want {
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
}
