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

// viKeys feeds keys through the normal key dispatch; ESC is the escape key.
func viKeys(e *Editor, keys string) {
	for _, r := range keys {
		if r == 0x1b {
			e.handleKey(Key{Code: KeyEscape})
			continue
		}
		e.handleKey(Key{Rune: r})
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
