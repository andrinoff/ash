package editor

import (
	"strings"
	"testing"
)

// A key binding that replaces the command line (commandline -r) between
// two presses of a repeating command must not leave it stale offsets:
// yank-last-arg used to slice past the end of the new, shorter line.
func TestRepeatAfterExternalEdit(t *testing.T) {
	for _, fn := range []string{"yank-last-arg", "history-token-search-backward", "yank-pop"} {
		e := newTestEditor(80)
		e.cfg.History = fakeHist{"git commit --amend so", "ls /usr/local/bin/something", "echo so so"}
		e.cfg.RunCommand = func(string) { e.SetBuffer("", 0) }
		if err := e.Bind("default", "ctrl-g", "commandline -r ''", ""); err != nil {
			t.Fatal(err)
		}
		e.kill.push("one")
		e.kill.push("two")
		e.insert([]rune("echo some long text here so"))
		if fn == "yank-pop" {
			e.call("yank", Key{})
		} else {
			e.call(fn, Key{})
		}
		e.handleKey(Key{Rune: 'g', Mod: ModCtrl})
		e.call(fn, Key{})
		if fn == "yank-pop" && len(e.buf) != 0 {
			t.Errorf("yank-pop after the line was replaced: %q", string(e.buf))
		}
	}
}

// `read` (Simple) accepts whatever was typed: text that would be an
// unfinished command, like an apostrophe, must not turn Enter into a
// newline.
func TestSimpleExecuteIgnoresIncomplete(t *testing.T) {
	incomplete := func(s string) bool { return strings.Count(s, "'")%2 == 1 }
	e := newTestEditor(80)
	e.opts.Simple = true
	e.cfg.Incomplete = incomplete
	typeKeys(e, "don't")
	e.handleKey(Key{Code: KeyEnter})
	if !e.done || e.result != "don't" {
		t.Fatalf("read of don't: done %v buf %q", e.done, string(e.buf))
	}
	// the command line still continues an unfinished command
	e = newTestEditor(80)
	e.cfg.Incomplete = incomplete
	typeKeys(e, "echo don't")
	e.handleKey(Key{Code: KeyEnter})
	if e.done || string(e.buf) != "echo don't\n" {
		t.Fatalf("command line: done %v buf %q", e.done, string(e.buf))
	}
}

// alt-h and alt-w look up the command under the cursor; text with
// multi-byte characters before a separator used to pick the wrong word
// or panic.
func TestCommandAtCursorMultibyte(t *testing.T) {
	cases := []struct {
		buf  string
		pos  int // -1: end
		want string
	}{
		{"echo ééé | grep x", -1, "grep"},
		{"ééé;", -1, ""},
		{"ééé ; ls -la", -1, "ls"},
		{"ééé ; sudo ls -la", 7, "ls"},
		{"git status", 2, "git"},
	}
	for _, c := range cases {
		e := newTestEditor(80)
		e.buf = []rune(c.buf)
		e.pos = c.pos
		if c.pos < 0 {
			e.pos = len(e.buf)
		}
		if got := e.commandAtCursor(); got != c.want {
			t.Errorf("%q at %d: got %q, want %q", c.buf, e.pos, got, c.want)
		}
	}
}
