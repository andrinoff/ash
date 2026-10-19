package editor

import "testing"

func typeKeys(e *Editor, s string) {
	for _, r := range s {
		e.handleKey(Key{Rune: r})
	}
}

// Keys that start a multi-key binding but don't finish it must still do
// what they do on their own (here: insert themselves).
func TestUnfinishedChordKeepsKeys(t *testing.T) {
	e := newTestEditor(80)
	e.keymap = "vi"
	if err := e.Bind("insert", "j,k", "vi-normal", ""); err != nil {
		t.Fatal(err)
	}
	typeKeys(e, "ajx")
	if got := string(e.buf); got != "ajx" {
		t.Fatalf("j before x was lost: %q", got)
	}
	// j then a pause: the chord times out and j is typed
	typeKeys(e, "j")
	if len(e.pendingKeys) != 1 {
		t.Fatalf("j should wait for a possible k")
	}
	e.flushChord()
	if got := string(e.buf); got != "ajxj" {
		t.Fatalf("j lost after the chord timeout: %q", got)
	}
	// j j k: the first j is typed, the second starts the chord again
	typeKeys(e, "jjk")
	if got := string(e.buf); got != "ajxjj" || e.viMode != "default" {
		t.Fatalf("jjk: buf %q mode %s", got, e.viMode)
	}
}

// A paste right after a key that may start a binding goes after that key.
func TestPasteAfterPendingKey(t *testing.T) {
	e := newTestEditor(80)
	e.keymap = "vi"
	if err := e.Bind("insert", "j,k", "vi-normal", ""); err != nil {
		t.Fatal(err)
	}
	typeKeys(e, "aj")
	e.handleKey(Key{Paste: "foo"})
	if got := string(e.buf); got != "ajfoo" || len(e.pendingKeys) != 0 {
		t.Fatalf("buf %q pending %v", got, e.pendingKeys)
	}
}

// A bound prefix of a longer binding runs when the next key doesn't
// continue it (ctrl-x is copy-commandline, ctrl-x ctrl-e edits).
func TestUnfinishedChordRunsPrefixBinding(t *testing.T) {
	e := newTestEditor(80)
	ran := ""
	functions["test-fn"] = func(e *Editor, k Key) { ran += "fn;" }
	defer delete(functions, "test-fn")
	if err := e.Bind("default", "ctrl-g", "test-fn", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.Bind("default", "ctrl-g,ctrl-h", "end-of-line", ""); err != nil {
		t.Fatal(err)
	}
	e.handleKey(Key{Rune: 'g', Mod: ModCtrl})
	if ran != "" {
		t.Fatal("ctrl-g ran before the chord was decided")
	}
	e.handleKey(Key{Rune: 'z'})
	if ran != "fn;" || string(e.buf) != "z" {
		t.Fatalf("ran %q buf %q", ran, string(e.buf))
	}
	e.handleKey(Key{Rune: 'g', Mod: ModCtrl})
	e.flushChord()
	if ran != "fn;fn;" {
		t.Fatalf("ctrl-g after the timeout: ran %q", ran)
	}
}
