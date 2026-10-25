package editor

import "testing"

// viEditor returns an editor in vi normal mode with buf and the cursor at pos.
func viEditor(buf string, pos int) *Editor {
	e := newTestEditor(80)
	e.keymap = "vi"
	e.viMode = "default"
	e.buf = []rune(buf)
	e.pos = pos
	return e
}

// viKeys feeds keys through the normal key dispatch; ESC is the escape key
// and other control characters are ctrl keys.
func viKeys(e *Editor, keys string) {
	for _, r := range keys {
		switch {
		case r == 0x1b:
			e.handleKey(Key{Code: KeyEscape})
		case r < 0x20:
			k, _, _ := decodeKey([]byte{byte(r)}, true)
			e.handleKey(k)
		default:
			e.handleKey(Key{Rune: r})
		}
	}
}

func TestViTillAdjacent(t *testing.T) {
	cases := []struct {
		buf  string
		pos  int
		keys string
		want string
	}{
		// the target right after the cursor: only the cursor character goes
		{"foo(a)(b)", 4, "dt)", "foo()(b)"},
		{`echo "ab" x`, 7, `ct"Z` + "\x1b", `echo "aZ" x`},
		// T with the target right before the cursor deletes nothing
		{"(a)", 1, "dT(", "(a)"},
		{"x(abc)", 4, "dT(", "x(c)"},
		{"a,b,c", 0, "dt,", ",b,c"},
	}
	for _, c := range cases {
		e := viEditor(c.buf, c.pos)
		viKeys(e, c.keys)
		if got := string(e.buf); got != c.want {
			t.Errorf("%q at %d, %q: got %q, want %q", c.buf, c.pos, c.keys, got, c.want)
		}
	}
}

func TestViOperatorCount(t *testing.T) {
	cases := []struct {
		buf  string
		pos  int
		keys string
		want string
	}{
		{"one two three four", 0, "2dw", "three four"},
		{"one two three four", 0, "d2w", "three four"},
		{"one two three four five six seven", 0, "2d2w", "five six seven"},
		{"a.b.c.d", 0, "2df.", "c.d"},
		{"one two three four", 0, "2cwX\x1b", "X three four"},
		{"one two three four", 0, "dw", "two three four"},
	}
	for _, c := range cases {
		e := viEditor(c.buf, c.pos)
		viKeys(e, c.keys)
		if got := string(e.buf); got != c.want {
			t.Errorf("%q, %q: got %q, want %q", c.buf, c.keys, got, c.want)
		}
	}
	// a count before f/t is the occurrence to find
	e := viEditor("a.b.c.d", 0)
	viKeys(e, "2f.")
	if e.pos != 3 {
		t.Errorf("2f.: pos %d, want 3", e.pos)
	}
	e.pos = 6
	viKeys(e, "2T.")
	if e.pos != 4 {
		t.Errorf("2T.: pos %d, want 4", e.pos)
	}
}

func TestViChangeWord(t *testing.T) {
	cases := []struct {
		buf  string
		pos  int
		keys string
		want string
	}{
		{"git commit", 0, "cwX\x1b", "X commit"},
		// on the last character of a word only that character changes
		{"git commit", 2, "cwX\x1b", "giX commit"},
		{"git commit -m", 9, "cwX\x1b", "git commiX -m"},
		{"one two three", 0, "c2wX\x1b", "X three"},
		{"one two three", 2, "c2wX\x1b", "onX three"},
		{"foo.bar baz", 3, "cwX\x1b", "fooXbar baz"},
		{"foo.bar baz", 0, "cWX\x1b", "X baz"},
		{"foo.bar baz", 6, "cWX\x1b", "foo.baX baz"},
		// on a blank, cw changes the blanks like dw
		{"a   b", 1, "cwX\x1b", "aXb"},
	}
	for _, c := range cases {
		e := viEditor(c.buf, c.pos)
		viKeys(e, c.keys)
		if got := string(e.buf); got != c.want {
			t.Errorf("%q at %d, %q: got %q, want %q", c.buf, c.pos, c.keys, got, c.want)
		}
	}
}

func TestViEscapeMovesLeft(t *testing.T) {
	e := viEditor("", 0)
	e.viMode = "insert"
	viKeys(e, "abcd")
	e.handleKey(Key{Code: KeyLeft})
	e.handleKey(Key{Code: KeyLeft})
	viKeys(e, "\x1b")
	if e.viMode != "default" || e.pos != 1 {
		t.Fatalf("mode %s pos %d, want default 1", e.viMode, e.pos)
	}
	// x now deletes the character before where the insert cursor was
	viKeys(e, "x")
	if got := string(e.buf); got != "acd" {
		t.Fatalf("x: %q", got)
	}
	// at the start of a line the cursor stays put
	e = viEditor("ab\ncd", 3)
	e.viMode = "insert"
	viKeys(e, "\x1b")
	if e.pos != 3 {
		t.Fatalf("start of line: pos %d", e.pos)
	}
	e = viEditor("ab", 0)
	viKeys(e, "i\x1b")
	if e.pos != 0 {
		t.Fatalf("start of buffer: pos %d", e.pos)
	}
}

// A binding that leaves insert mode with -m default moves the cursor only
// as its functions say (fish's `bind -M insert -m default j,k
// backward-char` moved it twice); only escape steps back by itself. At the
// end of the line the cursor still goes onto the last character.
func TestViModeBindingDoesNotStepBack(t *testing.T) {
	cases := []struct {
		fns        string
		left, want int
	}{
		{"backward-char", 1, 2},
		{"repaint-mode", 1, 3},
		{"repaint-mode", 0, 3},
	}
	for _, c := range cases {
		e := viEditor("", 0)
		e.viMode = "insert"
		if err := e.Bind("insert", "j,k", c.fns, "default"); err != nil {
			t.Fatal(err)
		}
		viKeys(e, "abcd")
		for i := 0; i < c.left; i++ {
			e.handleKey(Key{Code: KeyLeft})
		}
		viKeys(e, "jk")
		if string(e.buf) != "abcd" || e.viMode != "default" || e.pos != c.want {
			t.Errorf("%s after %d left: %q in %s at %d, want %d", c.fns, c.left, string(e.buf), e.viMode, e.pos, c.want)
		}
	}
}

// A and dd act on the cursor's line of a multi-line command.
func TestViLineCommands(t *testing.T) {
	cases := []struct {
		buf  string
		pos  int
		keys string
		want string
		at   int
	}{
		{"for i in 1 2\necho $i\nend", 14, "AX\x1b", "for i in 1 2\necho $iX\nend", 20},
		{"for i in 1 2\n  echo $i\nend", 15, "dd", "for i in 1 2\nend", 13},
		{"one\n  two", 6, "dd", "one", 0},
		{"a\n\nb", 2, "dd", "a\nb", 2},
		{"one", 1, "dd", "", 0},
		{"one two", 1, "AX\x1b", "one twoX", 7},
	}
	for _, c := range cases {
		e := viEditor(c.buf, c.pos)
		viKeys(e, c.keys)
		if got := string(e.buf); got != c.want || e.pos != c.at {
			t.Errorf("%q at %d, %q: got %q at %d, want %q at %d", c.buf, c.pos, c.keys, got, e.pos, c.want, c.at)
		}
	}
}

// The cursor stays on the text after R and goes to the start of the
// selection after visual ~, as in vi.
func TestViCursorAfterReplaceAndToggle(t *testing.T) {
	e := viEditor("abc", 1)
	viKeys(e, "R12\x1b")
	if got := string(e.buf); got != "a12" || e.pos != 2 {
		t.Fatalf("R12<esc>: %q at %d, want a12 at 2", got, e.pos)
	}
	viKeys(e, "x")
	if got := string(e.buf); got != "a1" {
		t.Fatalf("x after R: %q", got)
	}
	e = viEditor("abc def", 4)
	viKeys(e, "ve~")
	if got := string(e.buf); got != "abc DEF" || e.pos != 4 || e.viMode != "default" {
		t.Fatalf("ve~: %q at %d (%s)", got, e.pos, e.viMode)
	}
}

// aw takes the blanks after the word, or the ones before it when the word
// ends the line, so daw on the last argument leaves no trailing space.
func TestViAroundWord(t *testing.T) {
	cases := []struct {
		buf  string
		pos  int
		keys string
		want string
	}{
		{"echo hello", 7, "daw", "echo"},
		{"echo hello world", 7, "daw", "echo world"},
		{"echo hello  world", 7, "daw", "echo world"},
		{"echo hello", 1, "daw", "hello"},
		{"ls -la\t", 4, "daW", "ls "},
		{"ls -la\t", 4, "daw", "ls -"},
		{"a b.c", 3, "daW", "a"},
	}
	for _, c := range cases {
		e := viEditor(c.buf, c.pos)
		viKeys(e, c.keys)
		if got := string(e.buf); got != c.want {
			t.Errorf("%q at %d, %q: got %q, want %q", c.buf, c.pos, c.keys, got, c.want)
		}
	}
}

// A line taken from the history search opened with / leaves the cursor on
// its last character, as normal mode requires.
func TestViHistorySearchCursor(t *testing.T) {
	e := histSearchEditor(fakeHist{"git status"})
	e.keymap = "vi"
	e.viMode = "default"
	viKeys(e, "/")
	e.handleKey(Key{Paste: "git"})
	e.handleKey(Key{Code: KeyEnter})
	if string(e.buf) != "git status" || e.pos != 9 {
		t.Fatalf("buf %q pos %d", string(e.buf), e.pos)
	}
	viKeys(e, "x")
	if got := string(e.buf); got != "git statu" {
		t.Fatalf("x: %q", got)
	}
}

func TestViRepeatTill(t *testing.T) {
	// ; after t must not get stuck in front of the character
	e := viEditor("a.b.c.d", 0)
	viKeys(e, "t.")
	if e.pos != 0 {
		t.Fatalf("t.: pos %d", e.pos)
	}
	viKeys(e, ";")
	if e.pos != 2 {
		t.Fatalf("t.;: pos %d, want 2", e.pos)
	}
	viKeys(e, ";")
	if e.pos != 4 {
		t.Fatalf("t.;;: pos %d, want 4", e.pos)
	}
	viKeys(e, ",")
	if e.pos != 2 {
		t.Fatalf("t.;;,: pos %d, want 2", e.pos)
	}
}
