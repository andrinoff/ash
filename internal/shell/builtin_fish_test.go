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

func TestVaredStartsFromTheValue(t *testing.T) {
	sh := New()
	sh.Init()
	var prompts []string
	sh.SetReadLine(func(prompt, initial string) (string, error) {
		prompts = append(prompts, prompt+"|"+initial)
		return initial + "XY", nil
	})
	out := &lockedBuffer{}
	io := &IO{In: &Stream{TTY: true}, Out: &Stream{W: out}, Err: &Stream{W: &lockedBuffer{}}}
	sh.RunString(`set x hello; vared x; echo $x; read -e -i def -p 'p> ' y; echo $y`, "test.fish", io)
	if want := "helloXY\ndefXY\n"; out.String() != want {
		t.Fatalf("got %q, want %q (prompts %q)", out.String(), want, prompts)
	}
}

func TestFunctionsCopyWontOverwrite(t *testing.T) {
	out, errOut, _ := runFish(t, `
function foo; echo foo; end
function bar; echo bar; end
functions -c foo bar; echo $status
bar
`)
	if want := "1\nbar\n"; out != want || errOut == "" {
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
}
