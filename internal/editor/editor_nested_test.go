package editor

import (
	"os"
	"testing"
)

// A key binding that runs `read` calls ReadLine while another ReadLine
// is in progress; the line being edited must survive it.
func TestNestedReadLineKeepsCommandLine(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	w.WriteString("bob\n")
	w.Close()
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	e := New(r, out, Config{})
	outer := ReadOpts{Continuation: "> ", Prompt: func(string) func() Prompt {
		return func() Prompt { return Prompt{Left: "$ "} }
	}}
	// state of the outer ReadLine, in the middle of a key binding
	e.reading = true
	e.opts = outer
	e.prompt = Prompt{Left: "$ "}
	e.insert([]rune("echo outer"))
	e.pos = 4
	undo := len(e.undo)

	line, err := e.ReadLine(ReadOpts{Simple: true, Prompt: func(string) func() Prompt {
		return func() Prompt { return Prompt{Left: "name? "} }
	}})
	if line != "bob" || err != nil {
		t.Fatalf("nested read: %q %v", line, err)
	}
	if got := string(e.buf); got != "echo outer" || e.pos != 4 {
		t.Fatalf("outer line clobbered: %q pos %d", got, e.pos)
	}
	if e.prompt.Left != "$ " || e.opts.Simple || e.opts.Continuation != "> " || len(e.undo) != undo {
		t.Fatalf("outer state clobbered: prompt %q opts %+v undo %d", e.prompt.Left, e.opts, len(e.undo))
	}
}
