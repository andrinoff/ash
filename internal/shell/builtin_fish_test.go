package shell

import (
	"strconv"
	"strings"
	"testing"
)

func TestRandomHugeRange(t *testing.T) {
	// These used to panic in Int63n because hi-lo+1 overflowed int64.
	out, errOut, st := runScript(t, `
random 0 9223372036854775807
random -9223372036854775808 9223372036854775807
random 9223372036854775806 9223372036854775807
random -9223372036854775808 4611686018427387904 9223372036854775807
random 5 5
random 0 2 10
`)
	if st != 0 || errOut != "" {
		t.Fatalf("status %d, stderr %q", st, errOut)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("got %q", out)
	}
	n := make([]int64, len(lines))
	for i, l := range lines {
		v, err := strconv.ParseInt(l, 10, 64)
		if err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		n[i] = v
	}
	if n[0] < 0 || n[2] < 9223372036854775806 || n[4] != 5 || n[5] < 0 || n[5] > 10 || n[5]%2 != 0 {
		t.Fatalf("out of range: %v", n)
	}
	switch n[3] {
	case -9223372036854775808, -4611686018427387904, 0, 4611686018427387904:
	default:
		t.Fatalf("random START STEP END gave %d", n[3])
	}
}
