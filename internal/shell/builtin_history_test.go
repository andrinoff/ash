package shell

import (
	"strings"
	"testing"
)

// placedHistory is a history stand-in that knows where each command last
// ran and how it ended, newest entry first.
type placedHistory []placedEntry

type placedEntry struct {
	cmd, dir string
	status   int
}

func (h placedHistory) Recent(n int) []string {
	return h.Filter(func(string, int) bool { return true }, n)
}

func (h placedHistory) Search(q string, prefix bool) []string {
	var out []string
	for _, e := range h {
		if prefix && strings.HasPrefix(e.cmd, q) || !prefix && strings.Contains(e.cmd, q) {
			out = append(out, e.cmd)
		}
	}
	return out
}

func (h placedHistory) Delete(cmd string) int           { return 0 }
func (h placedHistory) Clear()                          {}
func (h placedHistory) Import(kind string) (int, error) { return 0, nil }
func (h placedHistory) Filter(keep func(string, int) bool, n int) []string {
	var out []string
	for _, e := range h {
		if keep(e.dir, e.status) {
			out = append(out, e.cmd)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// runHistory runs the history builtin with args in sh and returns what it
// printed and its status.
func runHistory(t *testing.T, sh *Shell, args string) (string, string, int) {
	t.Helper()
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	io := &IO{In: &Stream{R: strings.NewReader("")}, Out: &Stream{W: out}, Err: &Stream{W: errb}}
	st := sh.RunString("history "+args, "test", io)
	return out.String(), errb.String(), st
}

func TestHistoryHereAndFailed(t *testing.T) {
	sh := New()
	sh.Init()
	here := sh.Dir()
	sh.History = placedHistory{
		{"make test", here, 2},
		{"git push", "/elsewhere", 1},
		{"git status", here, 0},
		{"make", "/elsewhere", 0},
		{"git stash", here, 0},
		{"git commit", here, 1},
	}
	tests := []struct {
		args, out string
	}{
		{"", "make test\ngit push\ngit status\nmake\ngit stash\ngit commit\n"},
		{"--here", "make test\ngit status\ngit stash\ngit commit\n"},
		{"--failed", "make test\ngit push\ngit commit\n"},
		{"--here --failed", "make test\ngit commit\n"},
		{"--failed --here", "make test\ngit commit\n"},
		{"search --here", "make test\ngit status\ngit stash\ngit commit\n"},
		// Counts and the other options still apply.
		{"--here -n 2", "make test\ngit status\n"},
		{"--here 3", "make test\ngit status\ngit stash\n"},
		{"--failed -1", "make test\n"},
		{"--here --reverse", "git commit\ngit stash\ngit status\nmake test\n"},
		// So do searches.
		{"--here git", "git status\ngit stash\ngit commit\n"},
		{"--failed git", "git push\ngit commit\n"},
		{"--here --prefix make", "make test\n"},
		{"--here --exact make", ""},
		{"--failed --exact 'git push'", "git push\n"},
		{"--here --failed git -n 1", "git commit\n"},
		{"--failed status", ""},
	}
	for _, tt := range tests {
		out, errs, st := runHistory(t, sh, tt.args)
		if out != tt.out || errs != "" || st != 0 {
			t.Errorf("history %s: got %q (stderr %q, status %d), want %q", tt.args, out, errs, st, tt.out)
		}
	}
}

func TestHistoryHereErrors(t *testing.T) {
	sh := New()
	sh.Init()
	sh.History = placedHistory{{"rm -rf build", sh.Dir(), 0}}
	// Clearing or deleting "only here" isn't a thing: don't clear it all.
	for _, args := range []string{"clear --here", "delete --failed 'rm -rf build'", "clear-session --failed"} {
		sub := strings.Fields(args)[0]
		_, errs, st := runHistory(t, sh, args)
		if want := "history " + sub + ": --here and --failed only work when listing commands\n"; errs != want || st != 2 {
			t.Errorf("history %s: stderr %q, status %d; want %q, 2", args, errs, st, want)
		}
	}
	if out, _, _ := runHistory(t, sh, ""); out != "rm -rf build\n" {
		t.Errorf("the history is now %q", out)
	}

	// A history that doesn't keep directories and statuses can't help.
	sh.History = &integHistory{entries: []string{"ls"}}
	out, errs, st := runHistory(t, sh, "--here")
	if out != "" || st != 1 || !strings.HasPrefix(errs, "history: ") {
		t.Errorf("history --here without directories: %q, stderr %q, status %d", out, errs, st)
	}
	if out, _, st := runHistory(t, sh, ""); out != "ls\n" || st != 0 {
		t.Errorf("history: %q, status %d", out, st)
	}
}
