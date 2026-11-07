package editor

import "testing"

// Outside ReadLine, and inside another Suspend (a key binding's command
// that applies plugin actions), the terminal is already in its normal
// mode: Suspend just runs f.
func TestSuspendWithoutReader(t *testing.T) {
	e := newTestEditor(80)
	calls := 0
	e.Suspend(func() {
		calls++
		e.Suspend(func() { calls++ })
	})
	if calls != 2 {
		t.Fatalf("f ran %d times", calls)
	}
}
