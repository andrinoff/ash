package editor

import (
	"strings"
	"testing"
)

func TestDecodeKey(t *testing.T) {
	cases := []struct {
		in   string
		name string
		n    int
	}{
		{"a", "a", 1},
		{"é", "é", 2},
		{"\x01", "ctrl-a", 1},
		{"\x7f", "backspace", 1},
		{"\r", "enter", 1},
		{"\t", "tab", 1},
		{"\x1b[A", "up", 3},
		{"\x1bOB", "down", 3},
		{"\x1b[1;5C", "ctrl-right", 6},
		{"\x1b[1;3D", "alt-left", 6},
		{"\x1b[3~", "delete", 4},
		{"\x1b[3;5~", "ctrl-delete", 6},
		{"\x1b[Z", "shift-tab", 3},
		{"\x1bb", "alt-b", 2},
		{"\x1b.", "alt-.", 2},
		{"\x1b\x7f", "alt-backspace", 2},
		{"\x1b[15~", "f5", 5},
		{"\x1bOP", "f1", 3},
		{"\x1b[97;5u", "ctrl-a", 7},
		{"\x1b[13;2u", "shift-enter", 7},
		{"\x1b[27;5;13~", "ctrl-enter", 10},
	}
	for _, c := range cases {
		k, n, more := decodeKey([]byte(c.in), false)
		if more {
			t.Errorf("%q: unexpected needMore", c.in)
			continue
		}
		if k.Name() != c.name || n != c.n {
			t.Errorf("%q: got %q (%d bytes), want %q (%d)", c.in, k.Name(), n, c.name, c.n)
		}
	}
}

func TestDecodeKeyPartial(t *testing.T) {
	for _, in := range []string{"\x1b", "\x1b[", "\x1b[1;5", "\xe2\x9d"} {
		if _, _, more := decodeKey([]byte(in), false); !more {
			t.Errorf("%q: expected needMore", in)
		}
	}
	k, n, _ := decodeKey([]byte("\x1b"), true)
	if k.Code != KeyEscape || n != 1 {
		t.Errorf("lone ESC with final: got %v", k)
	}
}

func TestDecodePaste(t *testing.T) {
	in := "\x1b[200~echo hi\nthere\x1b[201~x"
	k, n, more := decodeKey([]byte(in), false)
	if more || k.Paste != "echo hi\nthere" || in[n:] != "x" {
		t.Fatalf("paste: %+v n=%d more=%v", k, n, more)
	}
	if _, _, more := decodeKey([]byte("\x1b[200~unfinished"), false); !more {
		t.Fatal("unfinished paste should wait for more")
	}
}

func TestDecodeJunk(t *testing.T) {
	for _, in := range []string{"\x1b[?62;22c", "\x1b]11;rgb:0000/0000/0000\x07", "\x1b[12;40R", "\x1b[?1u", "\x1b[2;2$y", "\x1bP>|kitty\x1b\\"} {
		k, n, more := decodeKey([]byte(in), false)
		if more || !k.Junk || n != len(in) {
			t.Errorf("%q: expected junk consuming everything, got %+v n=%d", in, k, n)
		}
	}
}

func TestParseKeySpec(t *testing.T) {
	cases := map[string]string{
		"ctrl-o":        "ctrl-o",
		"\x0c":          "ctrl-l",
		`\cl`:           "ctrl-l",
		`\e[A`:          "up",
		`\eb`:           "alt-b",
		"alt-left":      "alt-left",
		"M-x":           "alt-x",
		"ctrl-x,ctrl-e": "ctrl-x,ctrl-e",
		"shift-tab":     "shift-tab",
		"alt-shift-a":   "alt-shift-a",
		"enter":         "enter",
		"esc":           "escape",
		"f12":           "f12",
		"a":             "a",
	}
	for in, want := range cases {
		got, err := ParseKeySpec(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%q: got %v want %s", in, got, want)
		}
	}
	if _, err := ParseKeySpec("ctrl-nosuchkey"); err == nil {
		t.Error("expected error for unknown key")
	}
}

func newTestEditor(cols int) *Editor {
	e := &Editor{cols: cols, rows: 40, keymap: "emacs", viMode: "insert", binds: map[string]map[string]binding{}}
	e.hist.matchStart = -1
	e.vi.visualStart = -1
	return e
}

func plainLines(f frame) []string {
	out := make([]string, len(f.lines))
	for i, l := range f.lines {
		out[i] = stripANSI(l)
	}
	return out
}

func TestLayoutWrap(t *testing.T) {
	e := newTestEditor(10)
	e.buf = []rune("echo abcdefghij")
	e.pos = len(e.buf)
	f := e.layout(Prompt{Left: "> "}, false)
	got := plainLines(f)
	want := []string{"> echo abc", "defghij"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", got, want)
	}
	if f.row != 1 || f.col != 7 {
		t.Fatalf("cursor at %d,%d", f.row, f.col)
	}
}

func TestLayoutExactFill(t *testing.T) {
	e := newTestEditor(10)
	e.buf = []rune("12345678")
	e.pos = len(e.buf)
	f := e.layout(Prompt{Left: "> "}, false)
	// the line is exactly full: the cursor goes to the next row
	if f.row != 1 || f.col != 0 {
		t.Fatalf("cursor at %d,%d lines %q", f.row, f.col, plainLines(f))
	}
}

func TestLayoutWideRunes(t *testing.T) {
	e := newTestEditor(10)
	e.buf = []rune("ab漢字漢字x")
	e.pos = 4
	f := e.layout(Prompt{Left: "> "}, false)
	got := plainLines(f)
	// "> ab漢字漢" is 2+2+2+2+2 = 10 columns; 字 wraps
	if len(got) != 2 || got[1] != "字x" {
		t.Fatalf("got %q", got)
	}
	if f.row != 0 || f.col != 8 {
		t.Fatalf("cursor %d,%d", f.row, f.col)
	}
}

func TestLayoutMultilinePromptAndRight(t *testing.T) {
	e := newTestEditor(30)
	e.buf = []rune("ls")
	e.pos = 2
	f := e.layout(Prompt{Left: "\n~/dir\n❯ ", Right: "12:00"}, false)
	got := plainLines(f)
	if len(got) != 3 || got[0] != "" || got[1] != "~/dir" {
		t.Fatalf("got %q", got)
	}
	if !strings.HasPrefix(got[2], "❯ ls") || !strings.HasSuffix(got[2], "12:00") || plainWidth(got[2]) != 30 {
		t.Fatalf("right prompt line %q (width %d)", got[2], plainWidth(got[2]))
	}
	if f.row != 2 || f.col != 4 {
		t.Fatalf("cursor %d,%d", f.row, f.col)
	}
}

func TestLayoutContinuation(t *testing.T) {
	e := newTestEditor(40)
	e.opts.Continuation = ". "
	e.buf = []rune("for i in 1\necho $i\nend")
	e.pos = len(e.buf)
	f := e.layout(Prompt{Left: "$ "}, false)
	got := plainLines(f)
	want := []string{"$ for i in 1", ". echo $i", ". end"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if f.row != 2 || f.col != 5 {
		t.Fatalf("cursor %d,%d", f.row, f.col)
	}
}

func TestLayoutSuggestion(t *testing.T) {
	e := newTestEditor(40)
	e.buf = []rune("git st")
	e.pos = len(e.buf)
	e.sugg = []rune("atus")
	f := e.layout(Prompt{Left: "$ "}, false)
	if got := plainLines(f)[0]; got != "$ git status" {
		t.Fatalf("got %q", got)
	}
	if f.col != 8 {
		t.Fatalf("cursor col %d", f.col)
	}
	// the final rendering drops the suggestion
	f = e.layout(Prompt{Left: "$ "}, true)
	if got := plainLines(f)[0]; got != "$ git st" {
		t.Fatalf("final got %q", got)
	}
}

func TestEditingOps(t *testing.T) {
	e := newTestEditor(80)
	e.insert([]rune("echo hello world"))
	e.call("backward-kill-word", Key{})
	if string(e.buf) != "echo hello " {
		t.Fatalf("backward-kill-word: %q", string(e.buf))
	}
	e.call("yank", Key{})
	if string(e.buf) != "echo hello world" {
		t.Fatalf("yank: %q", string(e.buf))
	}
	e.call("beginning-of-line", Key{})
	e.call("forward-word", Key{})
	if e.pos != 4 {
		t.Fatalf("forward-word pos %d", e.pos)
	}
	e.call("kill-line", Key{})
	if string(e.buf) != "echo" {
		t.Fatalf("kill-line: %q", string(e.buf))
	}
	e.call("undo", Key{})
	if string(e.buf) != "echo hello world" {
		t.Fatalf("undo: %q", string(e.buf))
	}
	e.call("redo", Key{})
	if string(e.buf) != "echo" {
		t.Fatalf("redo: %q", string(e.buf))
	}
	e.buf, e.pos = []rune("cd ~/projects/ash/internal"), 26
	e.call("backward-kill-path-component", Key{})
	if string(e.buf) != "cd ~/projects/ash/" {
		t.Fatalf("path component: %q", string(e.buf))
	}
	e.buf, e.pos = []rune("ab"), 2
	e.call("transpose-chars", Key{})
	if string(e.buf) != "ba" {
		t.Fatalf("transpose: %q", string(e.buf))
	}
	e.buf, e.pos = []rune("one two"), 0
	e.call("upcase-word", Key{})
	if string(e.buf) != "ONE two" {
		t.Fatalf("upcase: %q", string(e.buf))
	}
}

type fakeHist []string

func (h fakeHist) Search(q string, prefix bool) []string {
	var out []string
	for _, s := range h {
		if prefix && strings.HasPrefix(s, q) || !prefix && strings.Contains(s, q) {
			out = append(out, s)
		}
	}
	return out
}

func TestHistoryNavigation(t *testing.T) {
	e := newTestEditor(80)
	e.cfg.History = fakeHist{"git push", "ls -la", "git status"}
	e.insert([]rune("git"))
	e.call("up-or-search", Key{})
	if string(e.buf) != "git push" {
		t.Fatalf("first up: %q", string(e.buf))
	}
	e.call("up-or-search", Key{})
	if string(e.buf) != "git status" {
		t.Fatalf("second up: %q", string(e.buf))
	}
	e.call("up-or-search", Key{})
	if string(e.buf) != "git status" {
		t.Fatalf("past the end: %q", string(e.buf))
	}
	e.call("down-or-search", Key{})
	e.call("down-or-search", Key{})
	if string(e.buf) != "git" {
		t.Fatalf("back to typed text: %q", string(e.buf))
	}
}

func TestViMotions(t *testing.T) {
	e := newTestEditor(80)
	e.keymap = "vi"
	e.buf = []rune("echo hello world")
	e.pos = len(e.buf)
	e.setViMode("default")
	press := func(keys string) {
		for _, r := range keys {
			e.viKey(Key{Rune: r})
		}
	}
	press("0")
	if e.pos != 0 {
		t.Fatalf("0: %d", e.pos)
	}
	press("w")
	if e.pos != 5 {
		t.Fatalf("w: %d", e.pos)
	}
	press("dw")
	if string(e.buf) != "echo world" {
		t.Fatalf("dw: %q", string(e.buf))
	}
	press("$")
	if e.pos != len(e.buf)-1 {
		t.Fatalf("$: %d", e.pos)
	}
	press("0cw")
	if e.viMode != "insert" {
		t.Fatalf("cw should enter insert mode")
	}
	for _, r := range "print" {
		e.selfInsert(r)
	}
	if string(e.buf) != "print world" {
		t.Fatalf("cw: %q", string(e.buf))
	}
	e.setViMode("default")
	press("0fwD")
	if string(e.buf) != "print " {
		t.Fatalf("fw D: %q", string(e.buf))
	}
	press("u")
	if string(e.buf) != "print world" {
		t.Fatalf("u: %q", string(e.buf))
	}
	e.buf, e.pos = []rune(`echo "a b c" done`), 8
	press(`di"`)
	if string(e.buf) != `echo "" done` {
		t.Fatalf(`di": %q`, string(e.buf))
	}
}

func TestCompleteCommonPrefix(t *testing.T) {
	e := newTestEditor(80)
	items := func(texts ...string) []Candidate {
		var out []Candidate
		for _, s := range texts {
			out = append(out, Candidate{Text: s})
		}
		return out
	}
	var offer []Candidate
	e.cfg.Complete = func(s string, cur int) Completion {
		start := strings.LastIndexByte(s[:cur], ' ') + 1
		return Completion{Start: start, End: cur, Items: offer}
	}

	// One candidate is a prefix of the next: the common part is the
	// whole first one.
	offer = items("cherry", "cherry-pick")
	e.insert([]rune("git ch"))
	e.call("complete", Key{})
	if got := string(e.buf); got != "git cherry" {
		t.Fatalf("prefix of another: %q", got)
	}

	// Nothing in common beyond what was typed: the menu opens.
	offer = items("checkout", "cherry-pick")
	e.buf, e.pos = []rune("git che"), 7
	e.call("complete", Key{})
	if e.pager == nil || string(e.buf) != "git che" {
		t.Fatalf("expected the menu, buf %q", string(e.buf))
	}
	e.pager = nil

	// A multi-byte common prefix is never cut inside a character.
	offer = items("héllo", "hé")
	e.buf, e.pos = []rune("echo h"), 6
	e.call("complete", Key{})
	if got := string(e.buf); got != "echo hé" {
		t.Fatalf("multi-byte: %q", got)
	}
	if got := commonPrefix("aé", "aè"); got != "a" {
		t.Fatalf("commonPrefix split a rune: %q", got)
	}
}

func TestExplainLine(t *testing.T) {
	e := newTestEditor(80)
	e.rows = 12
	var asked string
	e.cfg.Explain = func(buf string) string {
		asked = buf
		return "tar  manipulate tape archives\n  -x  Extract\n"
	}
	e.insert([]rune("tar -x"))
	e.call("explain-line", Key{})
	if asked != "tar -x" || e.message != "tar  manipulate tape archives\n  -x  Extract" {
		t.Fatalf("asked %q, message %q", asked, e.message)
	}
	if string(e.buf) != "tar -x" {
		t.Errorf("the line changed: %q", string(e.buf))
	}

	// more than half the screen: cut, saying how much is left
	e.cfg.Explain = func(string) string { return strings.Repeat("row\n", 20) }
	e.call("explain-line", Key{})
	lines := strings.Split(e.message, "\n")
	if len(lines) != 6 || !strings.Contains(lines[5], "15 more") {
		t.Errorf("long explanation: %q", lines)
	}

	// nothing typed: nothing to explain
	e.cfg.Explain = func(string) string { t.Error("explained an empty line"); return "" }
	e.buf, e.pos, e.message = nil, 0, ""
	e.call("explain-line", Key{})
	if e.message != "" {
		t.Errorf("empty line: %q", e.message)
	}
	if got := emacsKeys["alt-w"]; got != "explain-line" {
		t.Errorf("alt-w is bound to %q", got)
	}
}

func TestCtrlZUndoesUnlessTheLineIsEmpty(t *testing.T) {
	e := newTestEditor(80)
	asked := 0
	e.cfg.ResumeJob = func() string { asked++; return "fg %1" }
	e.insert([]rune("vim notes.md"))
	e.call(emacsKeys["ctrl-z"], Key{})
	if string(e.buf) != "" || asked != 0 {
		t.Fatalf("with text, ctrl-z should undo: %q (asked %d)", string(e.buf), asked)
	}
	// An empty line with no stopped jobs: nothing to resume, nothing to undo.
	e2 := newTestEditor(80)
	e2.cfg.ResumeJob = func() string { return "" }
	e2.call(emacsKeys["ctrl-z"], Key{})
	if string(e2.buf) != "" {
		t.Fatalf("empty line: %q", string(e2.buf))
	}
}
