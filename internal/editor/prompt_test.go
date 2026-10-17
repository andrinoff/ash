package editor

import (
	"testing"
	"time"
)

// Prompts render in the background; a slow render requested for the
// previous vi mode must not replace the prompt of the current mode.
func TestStalePromptIgnored(t *testing.T) {
	e := newTestEditor(80)
	e.keymap = "vi"
	e.promptCh = make(chan promptResult, 4)
	e.promptDone = make(chan struct{})
	defer close(e.promptDone)
	e.opts.Prompt = func(km string) func() Prompt {
		return func() Prompt {
			if km == "vicmd" {
				time.Sleep(100 * time.Millisecond)
				return Prompt{Left: "[N] "}
			}
			return Prompt{Left: "[I] "}
		}
	}
	e.viMode = "default"
	e.requestPrompt()
	e.viMode = "insert"
	e.requestPrompt()
	for i := 0; i < 2; i++ {
		select {
		case r := <-e.promptCh:
			e.setPrompt(r)
		case <-time.After(2 * time.Second):
			t.Fatal("prompt never arrived")
		}
	}
	if e.prompt.Left != "[I] " {
		t.Fatalf("stale prompt shown: %q", e.prompt.Left)
	}
}
