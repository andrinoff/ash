package repl

import (
	"io"

	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andrinoff/ash/internal/shell"
)

func TestSearchScopesSetting(t *testing.T) {
	r := newScopesREPL(t)
	base := r.sh.HomeDir()
	mkdirs(t, base, "plain", "repo/.git")
	tests := []struct {
		dir    string
		scopes []string
		want   []string
	}{
		// the tab names work, and the order is the list's
		{"repo", []string{"repo", "here", "all"}, []string{"repository", "directory", "all"}},
		{"repo", []string{"failed", "all"}, []string{"failed", "all"}},
		// the repository goes where there's none, and unknown names are skipped
		{"plain", []string{"repo", "all", "nope"}, []string{"all"}},
		// nothing that applies: all of them, as if it weren't set
		{"plain", []string{"repo"}, []string{"all", "directory", "failed"}},
	}
	for _, tt := range tests {
		r.cd(t, filepath.Join(base, tt.dir))
		setList(t, r, "ash_search_scopes", tt.scopes...)
		if got := r.searchScopes(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("in %s with %q: %q, want %q", tt.dir, tt.scopes, got, tt.want)
		}
	}
}

func TestNotifyLong(t *testing.T) {
	r := newScopesREPL(t)
	notified := func(line string, dur time.Duration) string {
		t.Helper()
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		r.out = pw
		r.notifyLong(line, 1, dur)
		pw.Close()
		b, _ := io.ReadAll(pr)
		return string(b)
	}
	r.sh.SetString("ash_notify_after", "5")
	if got := notified("make test", 6*time.Second); !strings.Contains(got, "\x1b]9;make failed (1) after") {
		t.Errorf("long command: %q", got)
	}
	if got := notified("make test", 4*time.Second); got != "" {
		t.Errorf("quick command: %q", got)
	}
	// programs you sit in front of are left out, by default and by setting
	if got := notified("nvim main.go", time.Minute); got != "" {
		t.Errorf("editor: %q", got)
	}
	setList(t, r, "ash_notify_ignore", "make")
	if got := notified("make test", time.Minute); got != "" {
		t.Errorf("ignored by the setting: %q", got)
	}
	if got := notified("nvim main.go", time.Minute); got == "" {
		t.Error("nvim, which the setting no longer lists, sent nothing")
	}
	// inside tmux it's wrapped to get through to the terminal
	setList(t, r, "ash_notify_ignore")
	r.sh.SetString("TMUX", "/tmp/tmux-501/default,1,0")
	if got := notified("go build", time.Minute); !strings.HasPrefix(got, "\x1bPtmux;\x1b\x1b]9;go failed") {
		t.Errorf("in tmux: %q", got)
	}
}

// setList sets a global list variable, or erases it given no values.
func setList(t *testing.T, r *REPL, name string, vals ...string) {
	t.Helper()
	src := "set -e " + name
	if len(vals) > 0 {
		src = "set -g " + name
		for _, v := range vals {
			src += " " + shell.Quote(v)
		}
	}
	if st := r.sh.RunString(src, "", r.sh.StdIO()); st != 0 {
		t.Fatalf("%s: status %d", src, st)
	}
}
