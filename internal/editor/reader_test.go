package editor

import (
	"io"
	"os"
	"testing"
	"time"
)

// Drawing waits while more input is already waiting, but when a command
// run by a key binding reads that input itself (`read` with the answer
// typed ahead), nothing more comes: the line must be drawn now, not left
// stale until the next key.
func TestDrawWhenBindingTakesWaitingInput(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	e := newTestEditor(80)
	e.fd = int(r.Fd())
	e.reader = &ttyReader{asked: true}
	w.WriteString("abc")
	if !e.handleInput(ttyData{[]byte("x"), true}) {
		t.Fatal("more input is waiting: drawing should wait for it")
	}
	functions["test-read-tty"] = func(e *Editor, k Key) {
		io.ReadFull(r, make([]byte, 3))
	}
	defer delete(functions, "test-read-tty")
	if err := e.Bind("default", "ctrl-o", "test-read-tty", ""); err != nil {
		t.Fatal(err)
	}
	e.reader.asked = true
	if e.handleInput(ttyData{[]byte{0x0f}, true}) {
		t.Fatal("the binding read the waiting input: the line must be drawn now")
	}
	if string(e.buf) != "x" || e.reader.asked {
		t.Fatalf("buf %q asked %v", string(e.buf), e.reader.asked)
	}
}

// The reader takes only the input it is asked for. Bytes typed after the
// key that ends the line stay in the terminal, for the command that runs
// next (`head -1`, a password prompt) instead of being swallowed as
// typeahead for the next prompt.
func TestReaderTakesOnlyWhatItIsAskedFor(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	w.WriteString("ls\rinput for the command\n")
	rd := startReader(int(r.Fd()))
	var got []byte
	for len(got) == 0 || got[len(got)-1] != '\r' {
		rd.ask(1)
		select {
		case d := <-rd.data:
			rd.asked = false
			got = append(got, d.b...)
			if !d.more {
				t.Fatal("more input was waiting")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no data")
		}
	}
	if left := rd.stop(); string(got) != "ls\r" || len(left) != 0 {
		t.Fatalf("read %q, left %q", got, left)
	}
	rest := make([]byte, 64)
	n, _ := r.Read(rest)
	if string(rest[:n]) != "input for the command\n" {
		t.Fatalf("the rest of the input was taken: %q", rest[:n])
	}
}

// A byte read for a request nobody collected comes back from stop.
func TestReaderStopReturnsUnclaimedInput(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	rd := startReader(int(r.Fd()))
	rd.ask(1)
	w.WriteString("xy")
	// wait for the answer (a fixed sleep can be too short on a busy machine)
	for deadline := time.Now().Add(5 * time.Second); len(rd.data) == 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if left := rd.stop(); string(left) != "x" {
		t.Fatalf("left %q", left)
	}
	rest := make([]byte, 8)
	if n, _ := r.Read(rest); string(rest[:n]) != "y" {
		t.Fatalf("rest %q", rest[:n])
	}
	// stopping while waiting for a request or for input takes nothing
	rd = startReader(int(r.Fd()))
	if left := rd.stop(); len(left) != 0 {
		t.Fatalf("left %q", left)
	}
	rd = startReader(int(r.Fd()))
	rd.ask(1)
	time.Sleep(20 * time.Millisecond)
	if left := rd.stop(); len(left) != 0 {
		t.Fatalf("left %q", left)
	}
}

func TestReadSize(t *testing.T) {
	e := newTestEditor(80)
	if n := e.readSize(); n != 1 {
		t.Fatalf("typing: %d", n)
	}
	e.input = []byte("\x1b[200~some paste")
	if n := e.readSize(); n <= 1 {
		t.Fatalf("paste: %d", n)
	}
}
