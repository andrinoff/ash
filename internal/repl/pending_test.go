package repl

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/andrinoff/ash/internal/history"
	"github.com/andrinoff/ash/internal/shell"
)

func TestHistoryDetailsForTheBrowser(t *testing.T) {
	h := &storeHistory{history.Open(filepath.Join(t.TempDir(), "history"))}
	then := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	h.Add("make", "/src", then, 2, time.Second)
	h.Add("ls", "/tmp", then.Add(time.Minute), 0, 0)

	w, ok := historyBackend(h).(shell.HistoryWalker)
	if !ok {
		t.Fatal("the history doesn't give the browser any details")
	}
	type row struct {
		cmd, dir string
		when     time.Time
		status   int
	}
	var got []row
	w.EachRecent(func(cmd string, when time.Time, dir string, status int) bool {
		got = append(got, row{cmd, dir, when.UTC(), status})
		return true
	})
	want := []row{{"ls", "/tmp", then.Add(time.Minute), 0}, {"make", "/src", then, 2}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	n := 0
	w.EachRecent(func(string, time.Time, string, int) bool { n++; return false })
	if n != 1 {
		t.Errorf("didn't stop when asked: %d calls", n)
	}
}

func TestHistoryMergeBuiltin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	mine := &storeHistory{history.Open(path)}
	other := history.Open(path)
	mine.Wait()
	other.Wait()
	sh := shell.New()
	sh.Init()
	sh.History = mine

	other.Add(history.Entry{Cmd: "from another shell"})
	if got := mine.Recent(1); len(got) > 0 && got[0] == "from another shell" {
		t.Fatal("seen before merging")
	}
	sh.RunString("history merge", "", sh.StdIO())
	if got := mine.Recent(1); len(got) == 0 || got[0] != "from another shell" {
		t.Fatalf("after history merge: %q", got)
	}
}
