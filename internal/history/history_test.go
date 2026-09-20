package history

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// open opens a history file in a new temporary directory and waits for it.
func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ash", "history")
	s := Open(path)
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func reopen(t *testing.T, path string) *Store {
	t.Helper()
	s := Open(path)
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return s
}

func add(t *testing.T, s *Store, cmds ...string) {
	t.Helper()
	for i, c := range cmds {
		if err := s.Add(Entry{Cmd: c, When: time.Unix(1700000000+int64(i), 0)}); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func equal(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

func TestOpenMissing(t *testing.T) {
	s, path := open(t)
	if s.Len() != 0 || len(s.Recent(0)) != 0 || len(s.Entries()) != 0 {
		t.Errorf("history of a missing file isn't empty: %q", s.Recent(0))
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("opening created the file (%v)", err)
	}
}

func TestOpenDirectory(t *testing.T) {
	s := Open(t.TempDir())
	if s.Err() == nil {
		t.Error("no error reading a directory")
	}
	if s.Len() != 0 {
		t.Error("history isn't empty")
	}
}

func TestAddAndReload(t *testing.T) {
	s, path := open(t)
	entries := []Entry{
		{Cmd: "git status", When: time.Unix(1700000000, 500_000_000), Dir: "/src/ash", Duration: 1500 * time.Microsecond},
		{Cmd: "for i in 1 2\n\techo $i\\n\nend", When: time.Unix(1700000005, 0), Dir: "/tmp/a\tb", Status: 1, Duration: 2 * time.Second},
		{Cmd: "make test", When: time.Unix(1700000010, 0), Dir: "/src/ash", Status: 2},
	}
	for _, e := range entries {
		if err := s.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	want := "# ash history v1\n" +
		"1700000000\t0\t1\t/src/ash\tgit status\n" +
		`1700000005	1	2000	/tmp/a\tb	for i in 1 2\n\techo $i\\n\nend` + "\n" +
		"1700000010\t2\t0\t/src/ash\tmake test\n"
	if got := readFile(t, path); got != want {
		t.Errorf("file:\n%s\nwant:\n%s", got, want)
	}
	wantEntries := []Entry{
		{Cmd: "make test", When: time.Unix(1700000010, 0), Dir: "/src/ash", Status: 2},
		{Cmd: "for i in 1 2\n\techo $i\\n\nend", When: time.Unix(1700000005, 0), Dir: "/tmp/a\tb", Status: 1, Duration: 2 * time.Second},
		{Cmd: "git status", When: time.Unix(1700000000, 0), Dir: "/src/ash", Duration: time.Millisecond},
	}
	for _, st := range []*Store{s, reopen(t, path)} {
		if got := st.Entries(); !reflect.DeepEqual(got, wantEntries) {
			t.Errorf("entries = %+v\nwant %+v", got, wantEntries)
		}
		if st.Len() != 3 {
			t.Errorf("Len = %d, want 3", st.Len())
		}
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v (%v), want 0600", fi.Mode(), err)
	}
}

func TestAddIgnored(t *testing.T) {
	s, path := open(t)
	add(t, s, "", "   ", " secret", "\t", "\n")
	if s.Len() != 0 {
		t.Errorf("added %q", s.Recent(0))
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("file was created (%v)", err)
	}
	add(t, s, "ls   ", "echo a\\ ")
	equal(t, "Recent", s.Recent(0), []string{"echo a\\ ", "ls"})
}

func TestAddDefaultTime(t *testing.T) {
	s, _ := open(t)
	before := time.Now().Truncate(time.Second)
	if err := s.Add(Entry{Cmd: "ls"}); err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	if w := s.Entries()[0].When; w.Before(before) || w.After(after) {
		t.Errorf("When = %v, want between %v and %v", w, before, after)
	}
}

func TestDedup(t *testing.T) {
	s, path := open(t)
	if err := s.Add(Entry{Cmd: "a", When: time.Unix(1, 0), Dir: "/x", Status: 1}); err != nil {
		t.Fatal(err)
	}
	add(t, s, "b")
	if err := s.Add(Entry{Cmd: "a", When: time.Unix(1700000005, 0), Dir: "/y"}); err != nil {
		t.Fatal(err)
	}
	equal(t, "Recent", s.Recent(0), []string{"a", "b"})
	if n := strings.Count(readFile(t, path), "\ta\n"); n != 2 {
		t.Errorf("file has %d occurrences of a, want 2", n)
	}
	r := reopen(t, path)
	equal(t, "Recent after reopening", r.Recent(0), []string{"a", "b"})
	if e := r.Entries()[0]; e.Dir != "/y" || e.Status != 0 || e.When.Unix() != 1700000005 {
		t.Errorf("kept %+v, want the newest occurrence", e)
	}
}

func TestRecent(t *testing.T) {
	s, _ := open(t)
	add(t, s, "c1", "c2", "c3", "c4", "c5")
	tests := []struct {
		n    int
		want []string
	}{
		{0, []string{"c5", "c4", "c3", "c2", "c1"}},
		{-1, []string{"c5", "c4", "c3", "c2", "c1"}},
		{2, []string{"c5", "c4"}},
		{5, []string{"c5", "c4", "c3", "c2", "c1"}},
		{10, []string{"c5", "c4", "c3", "c2", "c1"}},
	}
	for _, tt := range tests {
		equal(t, fmt.Sprintf("Recent(%d)", tt.n), s.Recent(tt.n), tt.want)
	}
}

func TestSearch(t *testing.T) {
	s, _ := open(t)
	add(t, s,
		"git commit -m 'Fix bug'",
		"echo café",
		"Git Push",
		"echo CAFÉ",
		"grep TODO src",
		"git status",
		"ls",
	)
	tests := []struct {
		q      string
		prefix bool
		want   []string
	}{
		{"git", false, []string{"git status", "Git Push", "git commit -m 'Fix bug'"}},
		{"Git", false, []string{"Git Push"}},
		{"git", true, []string{"git status", "Git Push", "git commit -m 'Fix bug'"}},
		{"GIT", true, nil},
		{"push", true, nil},
		{"push", false, []string{"Git Push"}},
		{"café", false, []string{"echo CAFÉ", "echo café"}},
		{"CAFÉ", false, []string{"echo CAFÉ"}},
		{"echo caf", true, []string{"echo CAFÉ", "echo café"}},
		{"fix", false, []string{"git commit -m 'Fix bug'"}},
		{"todo", false, []string{"grep TODO src"}},
		{"o s", false, []string{"grep TODO src"}},
		{"s", true, nil},
		{"", false, []string{"ls", "git status", "grep TODO src", "echo CAFÉ", "Git Push", "echo café", "git commit -m 'Fix bug'"}},
		{"zzz", false, nil},
	}
	for _, tt := range tests {
		equal(t, fmt.Sprintf("Search(%q, %v)", tt.q, tt.prefix), s.Search(tt.q, tt.prefix), tt.want)
	}
}

func TestSuggest(t *testing.T) {
	s, _ := open(t)
	for i, e := range []Entry{
		{Cmd: "git status", Dir: "/a"},
		{Cmd: "git stash pop", Dir: "/b"},
		{Cmd: "git stash", Dir: "/a", Status: 1},
		{Cmd: "git st", Dir: "/a"},
		{Cmd: "make test", Dir: "/b", Status: 2},
		{Cmd: "make", Dir: "/a"},
		{Cmd: "écho ü", Dir: "/b"},
		{Cmd: "echo hi", Dir: "/b"},
	} {
		e.When = time.Unix(1700000000+int64(i), 0)
		if err := s.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct{ prefix, dir, want string }{
		{"git st", "/a", "git status"},    // succeeded here
		{"git st", "/b", "git stash pop"}, // succeeded here
		{"git st", "/c", "git stash pop"}, // newest that succeeded
		{"git sta", "/a", "git status"},   // "git stash" failed here
		{"git stash", "/a", "git stash pop"},
		{"git stash p", "/a", "git stash pop"},
		{"make", "/b", "make test"}, // only a failed one is longer
		{"make", "", "make test"},
		{"git", "", "git st"},
		{"git", "/b", "git stash pop"},
		{"éc", "", "écho ü"},
		{"echo hi", "/b", ""}, // not longer than the prefix
		{"", "/a", ""},
		{"x", "/a", ""},
		{"git status --short", "/a", ""},
	}
	for _, tt := range tests {
		if got := s.Suggest(tt.prefix, tt.dir); got != tt.want {
			t.Errorf("Suggest(%q, %q) = %q, want %q", tt.prefix, tt.dir, got, tt.want)
		}
	}
}

func TestCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	data := header +
		"1700000000\t0\t0\t/\tgood one\n" +
		"garbage line\n" +
		"1700000001\t0\t0\t/\tbad\ttab\n" +
		"1700000002\tx\t0\t/\tbad status\n" +
		"\n" +
		"# a comment\n" +
		"1700000003\t0\t0\t/\tgood two\n" +
		"1700000004\t0" // cut short by a crash
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	s := reopen(t, path)
	equal(t, "Recent", s.Recent(0), []string{"good two", "good one"})
	add(t, s, "after")
	equal(t, "Recent after adding", s.Recent(0), []string{"after", "good two", "good one"})
	if got := readFile(t, path); !strings.HasSuffix(got, "1700000004\t0\n1700000000\t0\t0\t\tafter\n") {
		t.Errorf("the partial line wasn't ended before appending:\n%s", got)
	}
	equal(t, "Recent after reopening", reopen(t, path).Recent(0), []string{"after", "good two", "good one"})
}

func TestMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	a, b := reopen(t, path), reopen(t, path)
	add(t, a, "a1")
	add(t, b, "b1")
	add(t, a, "a2")
	equal(t, "a", a.Recent(0), []string{"a2", "a1"})
	equal(t, "b", b.Recent(0), []string{"b1"})

	a.Merge()
	equal(t, "a after merging", a.Recent(0), []string{"a2", "b1", "a1"})
	b.Merge()
	equal(t, "b after merging", b.Recent(0), []string{"a2", "b1", "a1"})
	a.Merge()
	equal(t, "a after merging again", a.Recent(0), []string{"a2", "b1", "a1"})

	add(t, b, "b2", "a1")
	a.Merge()
	equal(t, "a after b's adds", a.Recent(0), []string{"a1", "b2", "a2", "b1"})
	equal(t, "reopened", reopen(t, path).Recent(0), []string{"a1", "b2", "a2", "b1"})

	// b rewrites the file; a notices and reads it again.
	if n := b.Delete("a2"); n != 1 {
		t.Errorf("Delete = %d, want 1", n)
	}
	add(t, b, "b3")
	a.Merge()
	equal(t, "a after b's delete", a.Recent(0), []string{"b3", "a1", "b2", "b1"})
}

func TestDelete(t *testing.T) {
	s, path := open(t)
	add(t, s, "a", "b", "a", "c", "a", "a b")
	if n := s.Delete("a"); n != 3 {
		t.Errorf("Delete(a) = %d, want 3", n)
	}
	equal(t, "Recent", s.Recent(0), []string{"a b", "c", "b"})
	want := header + "1700000001\t0\t0\t\tb\n" + "1700000003\t0\t0\t\tc\n" + "1700000005\t0\t0\t\ta b\n"
	if got := readFile(t, path); got != want {
		t.Errorf("file:\n%s\nwant:\n%s", got, want)
	}
	if n := s.Delete("zzz"); n != 0 {
		t.Errorf("Delete(zzz) = %d, want 0", n)
	}
	if n := s.Delete("b"); n != 1 {
		t.Errorf("Delete(b) = %d, want 1", n)
	}
	add(t, s, "d")
	equal(t, "reopened", reopen(t, path).Recent(0), []string{"d", "a b", "c"})
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestDeleteKeepsOtherShellsEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	a := reopen(t, path)
	add(t, a, "x", "y")
	b := reopen(t, path)
	add(t, b, "from b")
	if n := a.Delete("x"); n != 1 {
		t.Errorf("Delete = %d, want 1", n)
	}
	equal(t, "a", a.Recent(0), []string{"from b", "y"})
	equal(t, "reopened", reopen(t, path).Recent(0), []string{"from b", "y"})
}

func TestDeleteMissingFile(t *testing.T) {
	s, path := open(t)
	if n := s.Delete("x"); n != 0 {
		t.Errorf("Delete = %d, want 0", n)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Delete created the file (%v)", err)
	}
}

func TestDeleteSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	link := filepath.Join(dir, "history")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	s := reopen(t, link)
	add(t, s, "a", "b")
	s.Delete("a")
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the history file is no longer a symlink (%v)", err)
	}
	equal(t, "reopened", reopen(t, link).Recent(0), []string{"b"})
}

func TestClear(t *testing.T) {
	s, path := open(t)
	add(t, s, "a", "b")
	s.Clear()
	if s.Len() != 0 {
		t.Errorf("Len = %d after Clear", s.Len())
	}
	if got := readFile(t, path); got != header {
		t.Errorf("file after Clear: %q", got)
	}
	add(t, s, "c")
	equal(t, "reopened", reopen(t, path).Recent(0), []string{"c"})

	// Clearing a history that has no file yet creates an empty one, so that
	// the next shell doesn't import another shell's history into it.
	fresh, path2 := open(t)
	fresh.Clear()
	if got := readFile(t, path2); got != header {
		t.Errorf("file after Clear: %q", got)
	}
}

// TestUnwritable checks that the history still works in memory when its
// file can't be written.
func TestUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions don't apply to root")
	}
	s, path := open(t)
	add(t, s, "a", "b", "a")
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	if n := s.Delete("a"); n != 1 {
		t.Errorf("Delete = %d, want 1", n)
	}
	equal(t, "Recent", s.Recent(0), []string{"b"})
	if got := strings.Count(readFile(t, path), "\ta\n"); got != 2 {
		t.Errorf("file has %d occurrences of a, want 2", got)
	}
	s.Clear()
	if s.Len() != 0 {
		t.Errorf("Len = %d after Clear", s.Len())
	}

	fresh := reopen(t, filepath.Join(dir, "sub", "history"))
	if err := fresh.Add(Entry{Cmd: "c"}); err == nil {
		t.Error("no error adding to an unwritable file")
	}
	equal(t, "Recent", fresh.Recent(0), []string{"c"})
}

func TestCompaction(t *testing.T) {
	s := Open("")
	for i := 0; i < 1000; i++ {
		add(t, s, fmt.Sprintf("cmd %d", i%10))
	}
	s.mu.RLock()
	slots := len(s.items)
	s.mu.RUnlock()
	if slots > 200 {
		t.Errorf("%d slots for 10 commands", slots)
	}
	equal(t, "Recent", s.Recent(3), []string{"cmd 9", "cmd 8", "cmd 7"})
	if s.Len() != 10 {
		t.Errorf("Len = %d, want 10", s.Len())
	}
	if got := s.Suggest("cmd 3", ""); got != "" {
		t.Errorf("Suggest = %q", got)
	}
	if got := s.Suggest("cmd ", ""); got != "cmd 9" {
		t.Errorf("Suggest = %q, want cmd 9", got)
	}
}

func TestMemoryOnly(t *testing.T) {
	s := Open("")
	if !s.Loaded() || s.Err() != nil {
		t.Fatal("in-memory history isn't ready")
	}
	add(t, s, "a", "b", "a")
	equal(t, "Recent", s.Recent(0), []string{"a", "b"})
	if n := s.Delete("a"); n != 1 {
		t.Errorf("Delete = %d, want 1", n)
	}
	s.Merge()
	equal(t, "Recent", s.Recent(0), []string{"b"})
	s.Clear()
	if s.Len() != 0 {
		t.Errorf("Len = %d after Clear", s.Len())
	}
}

// TestConcurrentShells has several stores append to one file at once, as
// shells do.
func TestConcurrentShells(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	const shells, cmds = 4, 200
	var wg sync.WaitGroup
	for i := 0; i < shells; i++ {
		s := Open(path)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < cmds; j++ {
				if err := s.Add(Entry{Cmd: fmt.Sprintf("shell %d command %d\nsecond line", i, j)}); err != nil {
					t.Error(err)
					return
				}
				if j%50 == 0 {
					s.Merge()
				}
			}
		}()
	}
	wg.Wait()
	data := readFile(t, path)
	if got := strings.Count(data, "\n"); got != shells*cmds+1 {
		t.Errorf("%d lines, want %d", got, shells*cmds+1)
	}
	if strings.Count(data, header) != 1 {
		t.Errorf("header written more than once")
	}
	if n := reopen(t, path).Len(); n != shells*cmds {
		t.Errorf("Len = %d, want %d", n, shells*cmds)
	}
}

// TestConcurrentUse is for the race detector.
func TestConcurrentUse(t *testing.T) {
	s, _ := open(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				cmd := fmt.Sprintf("git commit -m %d", j%20)
				if err := s.Add(Entry{Cmd: cmd, Dir: "/x"}); err != nil {
					t.Error(err)
				}
				s.Search("commit", false)
				s.Suggest("git", "/x")
				s.Fuzzy("gcm", 5)
				s.Recent(10)
				s.Entries()
				if j%25 == 0 {
					s.Merge()
				}
				if j%40 == 0 {
					s.Delete(cmd)
				}
			}
		}()
	}
	wg.Wait()
}

func TestFileCompaction(t *testing.T) {
	if needsCompaction(compactMin-1, 10) || needsCompaction(3000, 2000) || !needsCompaction(3000, 1500) {
		t.Fatal("needsCompaction decides wrongly")
	}
	path := filepath.Join(t.TempDir(), "history")
	s := Open(path)
	s.Wait()
	// 2500 records of 50 commands, each run 50 times
	for i := 0; i < 2500; i++ {
		s.Add(Entry{Cmd: fmt.Sprintf("cmd %d", i%50), When: time.Unix(int64(1_700_000_000+i), 0), Dir: "/d", Status: i % 3})
	}
	before, _ := os.Stat(path)

	s2 := Open(path)
	s2.Wait()
	if err := s2.compactFile(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if after.Size()*10 > before.Size() {
		t.Errorf("file went from %d to %d bytes", before.Size(), after.Size())
	}
	// A fresh read sees the same history, newest copies kept.
	s3 := Open(path)
	es := s3.Entries()
	if len(es) != 50 {
		t.Fatalf("%d entries after compaction", len(es))
	}
	if es[0].Cmd != "cmd 49" || es[0].When.Unix() != 1_700_000_000+2499 || es[0].Status != 2499%3 {
		t.Errorf("newest entry %+v", es[0])
	}
	// The stores that were open still work, and take the rewrite in.
	s.Add(Entry{Cmd: "after"})
	s3.Merge()
	if got := s3.Recent(1); len(got) != 1 || got[0] != "after" {
		t.Errorf("after compaction: %q", got)
	}
}
