package repl

import (
	"testing"
	"time"

	"github.com/andrinoff/ash/internal/shell"
)

func TestCommandPosition(t *testing.T) {
	cases := []struct {
		prefix string
		cmdPos bool
		cmd    string
	}{
		{"", true, ""},
		{"git ", false, "git"},
		{"ls | ", true, ""},
		{"FOO=1 ", true, ""},
		{"if ", true, ""},
		{"echo a; and ", true, ""},
		{"sudo ", false, "sudo"},
		{"(cd /tmp; ", true, ""},
	}
	for _, c := range cases {
		pos, cmd := commandPosition(c.prefix)
		if pos != c.cmdPos || cmd != c.cmd {
			t.Errorf("%q: got (%v, %q) want (%v, %q)", c.prefix, pos, cmd, c.cmdPos, c.cmd)
		}
	}
}

func TestExpandAbbr(t *testing.T) {
	// keep the history store away from the real ~/.local/share
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	sh := shell.New()
	sh.Init()
	sh.RunString(`abbr -a gco git checkout; abbr -a --set-cursor gcm 'git commit -m "%"'; abbr -a --position anywhere -- -h --help`, "", sh.StdIO())
	r := &REPL{sh: sh, hist: openHistory(sh)}
	r.hist.Add("make test", "/", time.Now(), 0, 0)
	cases := []struct {
		buf    string
		want   string
		cursor int
		ok     bool
	}{
		{"gco", "git checkout", 12, true},
		{"echo gco", "", 0, false},
		{"ls; gco", "ls; git checkout", 16, true},
		{"gcm", `git commit -m ""`, 15, true},
		{"ls -h", "ls --help", 9, true},
		{"sudo !!", "sudo make test", 14, true},
		{"echo !$", "echo test", 9, true},
	}
	for _, c := range cases {
		got, cur, ok := r.expandAbbr(c.buf, len(c.buf))
		if ok != c.ok || ok && (got != c.want || cur != c.cursor) {
			t.Errorf("%q: got (%q, %d, %v) want (%q, %d, %v)", c.buf, got, cur, ok, c.want, c.cursor, c.ok)
		}
	}
}

func TestBuiltinPrompt(t *testing.T) {
	ps := promptState{env: []string{"HOME=/home/me"}, dir: "/home/me/src/a/b/c/d", status: 1}
	p := builtinPrompt(ps, "insert")
	if p.Left == "" || p.Right == "" {
		t.Fatalf("expected left and right prompt, got %+v", p)
	}
}
