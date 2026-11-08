package editor

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestClearScreenDeletesImages(t *testing.T) {
	// ctrl-l left fastfetch's logo on the screen: images drawn with the
	// kitty graphics protocol aren't text, so erasing the screen keeps them.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEditor(80)
	e.out = w
	e.clearScreen()
	w.Close()
	got, _ := io.ReadAll(r)
	if !strings.HasPrefix(string(got), "\x1b_Ga=d,d=A") || !strings.Contains(string(got), "\x1b[2J") {
		t.Fatalf("clear-screen wrote %q", got)
	}
}

func TestClearImagesInTmux(t *testing.T) {
	// tmux drops the kitty graphics sequence unless it's wrapped to pass
	// through, and inside tmux the images are on the outer terminal.
	t.Setenv("TMUX", "/tmp/tmux-501/default,1,0")
	if got := ClearImagesSequence(); got != "\x1bPtmux;\x1b\x1b_Ga=d,d=A,q=2\x1b\x1b\\\x1b\\" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("TMUX", "")
	if got := ClearImagesSequence(); got != "\x1b_Ga=d,d=A,q=2\x1b\\" {
		t.Fatalf("outside tmux: got %q", got)
	}
}
