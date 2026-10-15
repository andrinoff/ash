package editor

import (
	"strings"
	"testing"
)

func TestCutWidth(t *testing.T) {
	cases := []struct {
		s           string
		left, right int
		want        string
	}{
		{"0123456789abcdef", 0, 10, "0123456789"},
		{"0123456789abcdef", 10, 16, "abcdef"},
		{"0123456789abcdef", 4, 8, "4567"},
		{"\x1b[1m0123456789\x1b[0mabc", 10, 13, "\x1b[1m\x1b[0mabc"},
		// a wide character split by left goes right: [0, 3) can't hold it
		{"ab漢字cd", 0, 3, "ab"},
		{"ab漢字cd", 3, 8, "漢字cd"},
	}
	for _, c := range cases {
		if got := cutWidth(c.s, c.left, c.right); got != c.want {
			t.Errorf("cutWidth(%q, %d, %d) = %q, want %q", c.s, c.left, c.right, got, c.want)
		}
	}
}

// A prompt line wider than the terminal wraps onto as many rows as it
// needs; the input starts after its last part.
func TestLayoutLongPromptWraps(t *testing.T) {
	e := newTestEditor(10)
	e.buf = []rune("ls")
	e.pos = 2
	f := e.layout(Prompt{Left: "\x1b[1m0123456789abcdefghijXYZ\x1b[0m> "}, false)
	got := plainLines(f)
	want := []string{"0123456789", "abcdefghij", "XYZ> ls"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
	if f.row != 2 || f.col != 7 {
		t.Fatalf("cursor at %d,%d", f.row, f.col)
	}
	// the bold style carries over to the wrapped part
	if !strings.HasPrefix(f.lines[1], "\x1b[1m") {
		t.Fatalf("style lost on the wrapped row: %q", f.lines[1])
	}
}
