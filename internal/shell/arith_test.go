package shell

import "testing"

func arithEval(t *testing.T, sh *Shell, expr string) int64 {
	t.Helper()
	v, err := sh.arith(expr)
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return v
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
