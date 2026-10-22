package repl

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/shell"
)

// newTestREPL returns a REPL whose shell can't see the user's real
// configuration, history or universal variables.
func newTestREPL(t *testing.T) *REPL {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	sh := shell.New()
	sh.Init()
	r := &REPL{sh: sh, hist: openHistory(sh)}
	r.ed = editor.New(os.Stdin, os.Stdout, r.editorConfig())
	return r
}

func run(t *testing.T, sh *shell.Shell, src string) {
	t.Helper()
	if st := sh.RunString(src, "", sh.StdIO()); st != 0 {
		t.Fatalf("%q: status %d", src, st)
	}
}

// Prompt and abbreviation functions run while the line editor has the
// terminal in raw mode. External commands they start must stay in the
// shell's process group: a foreground job of its own would be handed the
// terminal and, when it finished, the shell would put back cooked mode
// under the editor's feet.
func TestPromptCommandsStayInShellGroup(t *testing.T) {
	r := newTestREPL(t)
	sh := r.sh
	sh.JobControl = true // as in an interactive shell on a terminal
	run(t, sh, `function fish_prompt; command sh -c 'ps -o pgid= -p $$'; end
function pg; command sh -c 'ps -o pgid= -p $$'; end
abbr -a pgid --function pg`)
	want := strconv.Itoa(syscall.Getpgrp())

	p := r.promptFunc()("insert")()
	if got := strings.TrimSpace(p.Left); got != want {
		t.Errorf("prompt command ran in process group %q, want the shell's %q", got, want)
	}
	if got, _, ok := r.expandAbbr("pgid", 4); !ok || strings.TrimSpace(got) != want {
		t.Errorf("abbreviation function command ran in process group %q (ok=%v), want the shell's %q", got, ok, want)
	}
}
