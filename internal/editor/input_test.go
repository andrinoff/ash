package editor

import (
	"testing"
	"time"
)

func setTimeouts(t *testing.T, seq, paste time.Duration) {
	oldSeq, oldPaste := seqTimeout, pasteTimeout
	seqTimeout, pasteTimeout = seq, paste
	t.Cleanup(func() { seqTimeout, pasteTimeout = oldSeq, oldPaste })
}

func feed(e *Editor, s string) {
	e.input = append(e.input, s...)
	e.processInput(false)
}

func timerFired(e *Editor) bool {
	if e.escTimer == nil {
		return false
	}
	select {
	case <-e.escTimer.C:
		return true
	default:
		return false
	}
}

// A paste that keeps arriving must not time out: when it did, the part
// received so far was inserted and the rest typed in as keys, so its
// newlines ran the command line.
func TestSlowPasteIsNotCut(t *testing.T) {
	setTimeouts(t, 100*time.Millisecond, 400*time.Millisecond)
	e := newTestEditor(80)
	feed(e, "\x1b[200~echo one")
	for _, part := range []string{"\necho two", "\necho three"} {
		time.Sleep(250 * time.Millisecond)
		feed(e, part)
		if timerFired(e) {
			t.Fatal("paste timed out while it was still arriving")
		}
	}
	feed(e, "\x1b[201~")
	if got := string(e.buf); got != "echo one\necho two\necho three" || e.done {
		t.Fatalf("buf %q done %v", got, e.done)
	}
}

// The paste timeout applies once the start marker is complete, even if
// the marker itself arrived in pieces.
func TestSplitPasteStartUsesPasteTimeout(t *testing.T) {
	setTimeouts(t, 50*time.Millisecond, 400*time.Millisecond)
	e := newTestEditor(80)
	feed(e, "\x1b[20")
	time.Sleep(20 * time.Millisecond)
	feed(e, "0~ab")
	time.Sleep(150 * time.Millisecond)
	if timerFired(e) {
		t.Fatal("the partial paste timed out with the escape sequence timeout")
	}
	feed(e, "\ncd\x1b[201~")
	if got := string(e.buf); got != "ab\ncd" || e.done {
		t.Fatalf("buf %q done %v", got, e.done)
	}
}
