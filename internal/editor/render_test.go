package editor

import (
	"os"
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

func TestWidthsEmojiSequences(t *testing.T) {
	cases := []struct {
		s    string
		want []int
	}{
		{"a❤️b", []int{1, 2, 0, 1}},
		{"👍🏽x", []int{2, 0, 1}},
		{"👨‍👩‍👧", []int{2, 0, 0, 0, 0}},
		{"1️⃣", []int{2, 0, 0}},
		{"éx", []int{1, 0, 1}},
		{"漢字", []int{2, 2}},
		{"a\tb\x01", []int{1, 4, 1, 2}},
		{"\x1b👍", []int{2, 2}},
	}
	for _, c := range cases {
		got := widths([]rune(c.s))
		if sprintf("%v", got) != sprintf("%v", c.want) {
			t.Errorf("widths(%q) = %v, want %v", c.s, got, c.want)
		}
	}
	if w := textWidth("\x1b[33m☁️ aws\x1b[0m > "); w != 9 {
		t.Errorf("prompt width %d, want 9", w)
	}
}

// Emoji sequences take the cells terminals draw them in, and the cursor
// moves over them as one character.
func TestLayoutEmojiSequences(t *testing.T) {
	e := newTestEditor(12)
	e.buf = []rune("❤️👍🏽abcdefgh")
	e.pos = len(e.buf)
	f := e.layout(Prompt{Left: "> "}, false)
	if got := plainLines(f); len(got) != 2 || got[0] != "> ❤️👍🏽abcdef" || got[1] != "gh" {
		t.Fatalf("got %q", got)
	}
	if f.row != 1 || f.col != 2 {
		t.Fatalf("cursor %d,%d", f.row, f.col)
	}
	e.pos = 0
	e.call("forward-char", Key{})
	e.call("forward-char", Key{})
	if e.pos != 4 {
		t.Fatalf("forward-char stopped inside an emoji sequence: pos %d", e.pos)
	}
	f = e.layout(Prompt{Left: "> "}, false)
	if f.row != 0 || f.col != 6 {
		t.Fatalf("cursor %d,%d", f.row, f.col)
	}
	e.call("backward-char", Key{})
	if e.pos != 2 {
		t.Fatalf("backward-char: pos %d", e.pos)
	}
	e.call("delete-char", Key{})
	e.call("backward-delete-char", Key{})
	if got := string(e.buf); got != "abcdefgh" {
		t.Fatalf("deleting: %q", got)
	}
}

// An accepted line that exactly fills the row must not leave an empty
// row (for the cursor) between it and the command's output.
func TestFinalLayoutExactFill(t *testing.T) {
	e := newTestEditor(10)
	e.buf = []rune("12345678")
	e.pos = len(e.buf)
	if f := e.layout(Prompt{Left: "> "}, false); len(f.lines) != 2 || f.row != 1 {
		t.Fatalf("editing: %d lines, cursor row %d", len(f.lines), f.row)
	}
	f := e.layout(Prompt{Left: "> "}, true)
	if got := plainLines(f); len(got) != 1 || got[0] != "> 12345678" || f.row != 0 {
		t.Fatalf("final: %q, cursor row %d", got, f.row)
	}
	out := captureOutput(t, e, func() {
		e.prompt = Prompt{Left: "> "}
		e.finalRender()
	})
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("final render moved down %d lines: %q", n, out)
	}
}

// captureOutput returns what e writes to the terminal during f.
func captureOutput(t *testing.T, e *Editor, f func()) string {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer tmp.Close()
	e.out = tmp
	f()
	b, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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
