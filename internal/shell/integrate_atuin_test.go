package shell

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// atuinTest installs a fake atuin: `history start` prints an id, `search`
// prints $LOG.reply on stderr (where atuin puts the pick); every call logs
// the variables atuin reads.
func atuinTest(t *testing.T) (*integTest, *integEditor) {
	it := newIntegTest(t)
	it.tool("atuin", `printf 'session=%s log=%s shell=%s query=%s\n' "$ATUIN_SESSION" "${ATUIN_LOG-}" "${ATUIN_SHELL-}" "${ATUIN_QUERY-}" >> "$LOG"
case "$1 $2" in
"history start") echo "id-$4" ;;
search*) cat "$LOG.reply" >&2 ;;
esac
exit "$(cat "$LOG.status" 2>/dev/null || echo 0)"`)
	ed := &integEditor{}
	it.sh.Editor = ed
	it.mustRun("integrate atuin")
	return it, ed
}

func TestAtuinRecords(t *testing.T) {
	it, ed := atuinTest(t)
	session := it.sh.Get("ATUIN_SESSION")
	if !regexp.MustCompile(`^[0-9a-f]{12}7[0-9a-f]{3}[89ab][0-9a-f]{15}$`).MatchString(session) {
		t.Errorf("ATUIN_SESSION=%q is not a UUIDv7", session)
	}
	if want := []string{"default ctrl-r __ash_atuin_search", "insert ctrl-r __ash_atuin_search"}; !equalLines(ed.binds, want) {
		t.Errorf("binds %q", ed.binds)
	}

	it.sh.RunPreExec("ls -la")
	it.sh.RunPostExec("ls -la", 2, 1500*time.Millisecond)
	want := []string{
		"atuin [history] [start] [--] [ls -la]",
		"session=" + session + " log= shell= query=",
		"atuin [history] [end] [--exit] [2] [--duration] [1500000000] [--] [id-ls -la]",
		"session=" + session + " log=error shell= query=",
	}
	if calls := it.calls(); !equalLines(calls, want) {
		t.Errorf("calls\n%q\nwant\n%q", calls, want)
	}

	// Nothing for blank lines or in private mode, and no end without a
	// start.
	it.sh.RunPreExec("  ")
	it.sh.RunPostExec("  ", 0, time.Second)
	it.mustRun("fish_private_mode=1")
	it.sh.RunPreExec("secret")
	it.sh.RunPostExec("secret", 0, time.Second)
	if calls := it.calls(); calls != nil {
		t.Errorf("recorded %q", calls)
	}

	// A failed start (no id) skips the end.
	it.mustRun("fish_private_mode=")
	it.write("log.status", "1")
	it.sh.RunPreExec("x")
	it.sh.RunPostExec("x", 0, time.Second)
	if calls := it.calls(); len(calls) != 2 {
		t.Errorf("calls after a failed start: %q", calls)
	}
}

func TestAtuinSearch(t *testing.T) {
	it, ed := atuinTest(t)
	session := it.sh.Get("ATUIN_SESSION")
	cases := []struct {
		name, reply, status string
		buf                 string
		queued              []string
		st                  int
	}{
		{"pick", "git status\n", "", "git status", nil, 0},
		{"pick and run", "__atuin_accept__:make test\n", "", "make test", []string{"execute"}, 0},
		{"cancel", "", "", "git", nil, 1},
		{"error", "", "2", "git", nil, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ed.buf, ed.cursor, ed.queued = "git", 3, nil
			it.write("log.reply", c.reply)
			os.Remove(filepath.Join(it.home, "log.status"))
			if c.status != "" {
				it.write("log.status", c.status)
			}
			if _, _, st := it.run("__ash_atuin_search"); st != c.st {
				t.Errorf("status %d, want %d", st, c.st)
			}
			if ed.buf != c.buf || ed.cursor != len(c.buf) && c.st == 0 || !equalLines(ed.queued, c.queued) {
				t.Errorf("buffer %q cursor %d queued %q", ed.buf, ed.cursor, ed.queued)
			}
			want := []string{"atuin [search] [-i] [--] [git]", "session=" + session + " log=error shell=bash query=git"}
			if calls := it.calls(); !equalLines(calls, want) {
				t.Errorf("calls %q, want %q", calls, want)
			}
		})
	}
}
