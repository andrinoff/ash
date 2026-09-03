package shell

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/andrinoff/ash/internal/syntax"
)

// Child processes can't change their parent shell: not its directory, not
// its variables. ash gives every program it runs a way around that. The
// interactive shell exports $ASH_ACTION_FILE, a path to a file that doesn't
// exist yet. A program (or a TUI like yazi, lf or lazygit) may write
// requests there, and after the command finishes the shell carries them
// out:
//
//	/some/dir             a bare directory: cd there
//	cd DIR                cd there
//	export NAME=VALUE     set and export a variable
//	set NAME VALUE...     set a global variable (fish style)
//	unset NAME            remove a variable
//	commandline TEXT      put TEXT on the next command line
//	message TEXT          print TEXT before the next prompt
//
// Arguments use shell quoting. Nothing else is run, so a program can't
// make the shell execute code this way.

type childActions struct {
	path   string
	dir    string
	buffer string
	fix    commandFix
}

// commandFix remembers a typo the shell knew how to fix.
type commandFix struct {
	bad, good string
}

// SetupActionFile creates the private directory for $ASH_ACTION_FILE and
// exports it. It returns a cleanup function.
func (sh *Shell) SetupActionFile() func() {
	dir, err := os.MkdirTemp("", "ash-")
	if err != nil {
		return func() {}
	}
	a := sh.shared.actions
	a.dir = dir
	a.path = filepath.Join(dir, "actions")
	sh.SetVar("ASH_ACTION_FILE", []string{a.path}, setOpts{scope: setGlobal, export: 1})
	return func() { os.RemoveAll(dir) }
}

// ActionFile returns the path in $ASH_ACTION_FILE, or "".
func (sh *Shell) ActionFile() string { return sh.shared.actions.path }

// ApplyChildActions reads and removes the action file, carrying out what it
// asks for. It reports whether anything happened.
func (sh *Shell) ApplyChildActions(io *IO) bool {
	a := sh.shared.actions
	if a.path == "" {
		return false
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		return false
	}
	os.Remove(a.path)
	did := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if sh.applyAction(line, io) {
			did = true
		}
	}
	return did
}

func (sh *Shell) applyAction(line string, io *IO) bool {
	// A bare path, as yazi --cwd-file and $LAZYGIT_NEW_DIR_FILE write it.
	if strings.HasPrefix(line, "/") || strings.HasPrefix(line, "~") {
		return sh.actionCd(sh.expandTildeString(line), io)
	}
	words, ok := literalWords(line)
	if !ok || len(words) == 0 {
		return false
	}
	switch words[0] {
	case "cd":
		if len(words) == 2 {
			return sh.actionCd(sh.expandTildeString(words[1]), io)
		}
	case "export":
		for _, w := range words[1:] {
			if name, val, ok := strings.Cut(w, "="); ok && validVarName(name) {
				sh.SetVar(name, []string{val}, setOpts{scope: setGlobal, export: 1})
			}
		}
		return true
	case "set":
		args := words[1:]
		for len(args) > 0 && strings.HasPrefix(args[0], "-") {
			args = args[1:]
		}
		if len(args) > 0 && validVarName(args[0]) {
			sh.SetVar(args[0], append([]string{}, args[1:]...), setOpts{scope: setGlobal})
			return true
		}
	case "unset":
		for _, n := range words[1:] {
			sh.UnsetVar(n, setGlobal)
		}
		return true
	case "commandline", "buffer":
		sh.shared.actions.buffer = strings.Join(words[1:], " ")
		return true
	case "message", "echo":
		io.Err.WriteString(strings.Join(words[1:], " ") + "\n")
		return true
	}
	return false
}

func (sh *Shell) actionCd(dir string, io *IO) bool {
	if dir == "" || dir == sh.dir {
		return false
	}
	if st, err := os.Stat(sh.absPath(dir)); err != nil || !st.IsDir() {
		return false
	}
	return sh.changeDir(dir, io, false) == 0
}

// literalWords parses a line into words, refusing anything that would
// need expansion (so an action file can't run code).
func literalWords(line string) ([]string, bool) {
	f, err := syntax.Parse(line)
	if err != nil || len(f.Stmts) != 1 {
		return nil, false
	}
	call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(call.Redirs) > 0 || f.Stmts[0].Background {
		return nil, false
	}
	var out []string
	for _, w := range call.Args {
		s, ok := w.LitString()
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// TakeNextBuffer returns (and forgets) text a child asked to put on the
// next command line.
func (sh *Shell) TakeNextBuffer() string {
	a := sh.shared.actions
	b := a.buffer
	a.buffer = ""
	return b
}

// noteFix records a typo and its likely correction.
func (sh *Shell) noteFix(bad, good string) {
	if sh.subshell || bad == "" || good == "" {
		return
	}
	sh.shared.actions.fix = commandFix{bad, good}
}

// TakeFix returns (and forgets) the last typo the shell could correct.
func (sh *Shell) TakeFix() (bad, good string) {
	a := sh.shared.actions
	f := a.fix
	a.fix = commandFix{}
	return f.bad, f.good
}
