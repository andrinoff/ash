package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestOpenLockedFollowsReplacements replaces the history file over and over
// while openLocked waits for its lock, as shells deleting commands do. It
// must end up with the current file, locked: an entry appended to one that
// has been replaced is lost.
func TestOpenLockedFollowsReplacements(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history")
	// replace puts a new file in place of the old one, locked first, as
	// rewrite does.
	replace := func() *os.File {
		t.Helper()
		f, err := os.CreateTemp(dir, ".history.*")
		if err != nil {
			t.Fatal(err)
		}
		if !lockFile(f, time.Second) {
			f.Close()
			t.Skip("no flock(2) here")
		}
		if err := os.Rename(f.Name(), path); err != nil {
			t.Fatal(err)
		}
		return f
	}
	cur := replace()
	done := make(chan *os.File, 1)
	go func() {
		f, err := openLocked(path, os.O_RDWR|os.O_APPEND, 10*time.Second)
		if err != nil {
			t.Error(err)
		}
		done <- f
	}()
	for i := 0; i < 20; i++ {
		time.Sleep(3 * time.Millisecond)
		next := replace()
		cur.Close() // unlocks the file that was just replaced
		cur = next
	}
	cur.Close()
	f := <-done
	if f == nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if now, err := os.Stat(path); err != nil || !os.SameFile(fi, now) {
		t.Errorf("openLocked returned a file that has been replaced (%v)", err)
	}
}
