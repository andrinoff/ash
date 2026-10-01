package shell

import (
	"strings"
	"testing"
)

func arithEval(t *testing.T, sh *Shell, expr string) int64 {
	t.Helper()
	v, err := sh.arith(expr)
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return v
}

func TestArithSubscriptEvaluatedOnce(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{`i=0; a=(5 6 7); echo $(( a[i++] )) $i`, "5 1\n"},
		{`i=0; a=(5 6 7); echo $(( a[i++] + 1 )) $i`, "6 1\n"},
		{`i=0; a=(5 6 7); echo $(( a[i++] = 9 )) $i ${a[@]}`, "9 1 9 6 7\n"},
		{`i=0; a=(5 6 7); (( a[i++] += 1 )); echo $i ${a[@]}`, "1 6 6 7\n"},
		{`i=0; a=(5 6 7); echo $(( a[++i]++ )) $i ${a[@]}`, "6 1 5 7 7\n"},
		{`i=0; echo $(( i++, i++, i )) $i`, "2 2\n"},
		{`x=3; echo $(( x == 3 ? 1 : 0 )) $(( x = x * 2 )) $x`, "1 6 6\n"},
	} {
		out, errOut, _ := runScript(t, c.src)
		if out != c.want {
			t.Errorf("%s: got %q (stderr %q), want %q", c.src, out, errOut, c.want)
		}
	}
}

// Expected values are bash's.
func TestArithConstants(t *testing.T) {
	sh := New()
	for _, c := range []struct {
		expr string
		want int64
	}{
		{"0x1F + 010 + 2#1010", 31 + 8 + 10},
		{"16#ff + 16#FF", 510},
		{"36#z + 36#Z", 70},
		{"64#a", 10},
		{"64#A", 36},
		{"64#@", 62},
		{"64#_", 63},
		{"64#10", 64},
		{"0xffffffffffffffff", -1},
		{"9223372036854775807 + 1", -9223372036854775807 - 1},
		{"9223372036854775808", -9223372036854775807 - 1},
		{"18446744073709551616", 0},
	} {
		if got := arithEval(t, sh, c.expr); got != c.want {
			t.Errorf("%s = %d, want %d", c.expr, got, c.want)
		}
	}
	for _, bad := range []string{"08", "2#2", "65#1", "1#1", "0xg", "1e3"} {
		if v, err := sh.arith(bad); err == nil {
			t.Errorf("%s = %d, want an error", bad, v)
		}
	}
	out, errOut, _ := runScript(t, `x=$(( -9223372036854775807 - 1 )); echo $x $(( x + 1 )) $(( x / -1 ))`)
	if want := "-9223372036854775808 -9223372036854775807 -9223372036854775808\n"; out != want {
		t.Errorf("got %q (stderr %q), want %q", out, errOut, want)
	}
}

// Deep nesting used to overflow the Go stack, a fatal error that took
// the whole shell down. It must be an ordinary error instead.
func TestArithDeepNestingIsAnError(t *testing.T) {
	sh := New()
	const n = 200000
	for _, expr := range []string{
		strings.Repeat("(", n) + "1" + strings.Repeat(")", n),
		strings.Repeat("- ", n) + "1",
		strings.Repeat("!", n) + "1",
		strings.Repeat("2**", n) + "1",
		strings.Repeat("x=", n) + "1",
		strings.Repeat("0?0:", n) + "1",
		strings.Repeat("a[", n) + "0" + strings.Repeat("]", n),
	} {
		if _, err := sh.arith(expr); err == nil || !strings.Contains(err.Error(), "nested too deeply") {
			t.Errorf("%.20s...: got %v", expr, err)
		}
	}
	// The limit holds across variables whose values are expressions.
	sh.SetVar("inner", []string{strings.Repeat("(", 3000) + "1" + strings.Repeat(")", 3000)}, setOpts{})
	sh.SetVar("outer", []string{strings.Repeat("(", 3000) + "inner" + strings.Repeat(")", 3000)}, setOpts{})
	if v, err := sh.arith("inner"); err != nil || v != 1 {
		t.Errorf("inner: %d %v", v, err)
	}
	if _, err := sh.arith("outer"); err == nil {
		t.Errorf("outer: no error")
	}
	// Ordinary nesting still works.
	if v, err := sh.arith(strings.Repeat("(", 500) + "1" + strings.Repeat(")", 500)); err != nil || v != 1 {
		t.Errorf("500 parentheses: %d %v", v, err)
	}
}

func TestArithPowerLargeExponent(t *testing.T) {
	sh := New()
	for _, c := range []struct {
		expr string
		want int64
	}{
		{"2 ** 10", 1024},
		{"2 ** 62", 1 << 62},
		{"3 ** 40", -6289078614652622815}, // wraps like bash
		{"0 ** 0", 1},
		{"1 ** 9223372036854775807", 1},
		{"(-1) ** 9223372036854775807", -1},
		{"2 ** 9223372036854775807", 0},
		{"2 ** 3 ** 2", 512},
	} {
		if got := arithEval(t, sh, c.expr); got != c.want {
			t.Errorf("%s = %d, want %d", c.expr, got, c.want)
		}
	}
}
