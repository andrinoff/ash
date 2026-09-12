package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRewriteKeepsMode checks that deleting, clearing and importing keep
// the history file's permissions and owner, as fish does: root rewriting
// someone's history (sudo keeps HOME on macOS) mustn't take it from them.
func TestRewriteKeepsMode(t *testing.T) {
	s, path := open(t)
	add(t, s, "a", "b", "c")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		// Root can give the file away, and must give it back.
		if err := os.Chown(path, 1, 1); err != nil {
			t.Fatal(err)
		}
		if before, err = os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(t.TempDir(), "bash_history")
	writeFile(t, src, "echo imported\n")
	for _, op := range []struct {
		name string
		do   func()
	}{
		{"Delete", func() { s.Delete("a") }},
		{"ImportFile", func() { s.ImportFile("bash", src) }},
		{"Clear", s.Clear},
	} {
		op.do()
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if after.Mode() != before.Mode() {
			t.Errorf("after %s: mode %v, want %v", op.name, after.Mode(), before.Mode())
		}
		if !sameOwner(before, after) {
			t.Errorf("after %s: the owner changed", op.name)
		}
	}
}

// TestRewriteDevNull has the history file be a symlink to /dev/null, which
// is how some people turn history off. Adding works and keeps nothing, and
// a rewrite must fail rather than put a file in place of /dev/null (which
// root could do).
func TestRewriteDevNull(t *testing.T) {
	if _, err := os.Stat(os.DevNull); err != nil || os.DevNull != "/dev/null" {
		t.Skip("no /dev/null")
	}
	path := filepath.Join(t.TempDir(), "history")
	if err := os.Symlink(os.DevNull, path); err != nil {
		t.Skip(err)
	}
	s := reopen(t, path)
	add(t, s, "a", "b")
	equal(t, "Recent", s.Recent(0), []string{"b", "a"})
	equal(t, "reopened", reopen(t, path).Recent(0), nil)
	src := filepath.Join(t.TempDir(), "bash_history")
	writeFile(t, src, "echo imported\n")
	if _, err := s.ImportFile("bash", src); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("ImportFile into /dev/null: %v", err)
	}
	s.Clear()
	if s.Len() != 0 {
		t.Errorf("Len = %d after Clear", s.Len())
	}
	if fi, err := os.Stat(os.DevNull); err != nil || fi.Mode().IsRegular() {
		t.Fatalf("/dev/null has been replaced (%v)", err)
	}
}

func sameOwner(a, b os.FileInfo) bool {
	ua, ga, oka := fileOwner(a)
	ub, gb, okb := fileOwner(b)
	return oka == okb && ua == ub && ga == gb
}

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
