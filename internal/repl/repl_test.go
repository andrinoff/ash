package repl

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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
	r.in, r.out = os.Stdin, os.Stdout
	r.ed = editor.New(r.in, r.out, r.editorConfig())
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

// Expanding an abbreviation with --function runs the function while the
// user types; $status must still be the last command's (the prompt, a
// repaint and the command line being typed can all look at it).
func TestAbbrFunctionKeepsStatus(t *testing.T) {
	r := newTestREPL(t)
	sh := r.sh
	run(t, sh, `function f; echo expanded; end; abbr -a xx --function f`)
	sh.RunString("false | true | false", "", sh.StdIO())
	if got, _, ok := r.expandAbbr("xx", 2); !ok || got != "expanded" {
		t.Fatalf("expansion %q, %v", got, ok)
	}
	if st, ps := sh.Status(), sh.PipeStatus(); st != 1 || len(ps) != 3 || ps[0] != 1 || ps[1] != 0 {
		t.Errorf("status %d, pipestatus %v after expanding; want 1, [1 0 1]", st, ps)
	}
}

// The editor renders prompts in the background, and a slow one can still
// be rendering when the next command line's options are built (for
// example after enter was pressed quickly). Remembering it as the next
// placeholder must not race with that (go test -race).
func TestPromptPlaceholderRace(t *testing.T) {
	r := newTestREPL(t)
	o := r.readOpts()
	render := o.Prompt("insert")
	done := make(chan struct{})
	go func() {
		defer close(done)
		render()
	}()
	r.readOpts()
	<-done
	if p := r.readOpts().Placeholder; p.Left == "" {
		t.Errorf("the rendered prompt wasn't kept as the placeholder: %+v", p)
	}
}

// lineInput makes the editor read command lines from a pipe (it falls
// back to plain line reading when the input isn't a terminal) and returns
// the writing end.
func lineInput(t *testing.T, r *REPL) *os.File {
	t.Helper()
	in, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close(); in.Close(); out.Close() })
	r.in, r.out = in, out
	r.ed = editor.New(in, out, r.editorConfig())
	r.sh.Editor = r.ed
	return w
}

// stoppedJob starts a background job and stops it (with SIGTSTP, like
// ctrl-z would).
func stoppedJob(t *testing.T, sh *shell.Shell) {
	t.Helper()
	// a process group of its own: in an orphaned group (tests may run
	// without a terminal session) SIGTSTP would be discarded
	sh.JobControl = true
	run(t, sh, "sleep 100 &")
	pid, err := strconv.Atoi(sh.Get("last_pid"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		syscall.Kill(pid, syscall.SIGKILL)
		syscall.Kill(pid, syscall.SIGCONT)
	})
	syscall.Kill(pid, syscall.SIGTSTP)
	for i := 0; !sh.StoppedJobs(); i++ {
		if i == 500 {
			t.Fatal("the job didn't stop")
		}
		time.Sleep(10 * time.Millisecond)
		sh.NotifyJobs(io.Discard)
	}
}

// With stopped jobs the first exit only warns; the second one exits.
func TestExitTwiceWithStoppedJobs(t *testing.T) {
	r := newTestREPL(t)
	stoppedJob(t, r.sh)
	w := lineInput(t, r)
	done := make(chan int, 1)
	go func() { done <- r.loop() }()
	w.WriteString("exit 3\nexit 3\n")
	select {
	case st := <-done:
		if st != 3 {
			t.Errorf("exit status %d, want 3", st)
		}
	case <-time.After(5 * time.Second):
		w.Close() // end of input exits
		<-done
		t.Fatal("a second exit didn't leave the shell")
	}
}

// `read` shares the line editor with the prompt. Run from a key binding
// (the editor is suspended in the middle of a command line) it must get
// an editor of its own instead of taking over the suspended line, and a
// background job must not start a second read on the editor while the
// prompt is up.
func TestReadWhileEditorBusy(t *testing.T) {
	r := newTestREPL(t)
	w := lineInput(t, r)
	readLine := func() (string, error) {
		type result struct {
			line string
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			line, err := r.readLine("? ", "")
			ch <- result{line, err}
		}()
		select {
		case res := <-ch:
			return res.line, res.err
		case <-time.After(5 * time.Second):
			t.Fatal("read didn't return")
			return "", nil
		}
	}

	w.WriteString("idle\n")
	if line, err := readLine(); line != "idle" || err != nil {
		t.Errorf("read at an idle editor: %q, %v", line, err)
	}

	r.edMu.Lock() // the prompt is reading a line
	defer r.edMu.Unlock()
	if line, err := readLine(); err == nil {
		t.Errorf("a read from a background job while at the prompt got %q", line)
	}

	r.inBinding.Store(true)
	defer r.inBinding.Store(false)
	w.WriteString("bound\n")
	if line, err := readLine(); line != "bound" || err != nil {
		t.Errorf("read in a key binding: %q, %v", line, err)
	}
}

// fakeStarship puts a starship in $PATH that prints its arguments and
// selects it as the prompt provider.
func fakeStarship(t *testing.T, r *REPL) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '[%s]' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "starship"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, r.sh, "set -gx PATH "+shell.Quote(dir)+" $PATH")
	r.sh.SetPromptProvider("starship")
}

// terminalOutput sends what the REPL writes to the terminal to a file,
// and returns a function that reads what was written so far.
func terminalOutput(t *testing.T, r *REPL) func() string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	r.out, r.tty = f, true
	return func() string {
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		f.Truncate(0)
		f.Seek(0, 0)
		return string(b)
	}
}

// fish_title gets the running command as $argv[1], and no arguments at
// the prompt.
func TestTitleArguments(t *testing.T) {
	r := newTestREPL(t)
	written := terminalOutput(t, r)
	run(t, r.sh, `function fish_title; echo (count $argv) $argv; end`)
	r.setTitle("")
	if got, want := written(), "\x1b]2;0\x07"; got != want {
		t.Errorf("at the prompt: %q, want %q", got, want)
	}
	r.setTitle("make test")
	if got, want := written(), "\x1b]2;1 make test\x07"; got != want {
		t.Errorf("running a command: %q, want %q", got, want)
	}
}

// The editor marks every accepted line as a command that started (OSC
// 133;C), so each one must be marked finished, empty ones included.
func TestEmptyLineMarkedFinished(t *testing.T) {
	r := newTestREPL(t)
	w := lineInput(t, r)
	written := terminalOutput(t, r)
	w.WriteString("\n   \ntrue\n")
	w.Close()
	r.loop()
	out := written()
	if n := strings.Count(out, "\x1b]133;D\x07"); n != 2 {
		t.Errorf("%d unnumbered 133;D marks, want 2 for the empty lines: %q", n, out)
	}
	if n := strings.Count(out, "\x1b]133;D;0\x07"); n != 1 {
		t.Errorf("%d 133;D;0 marks, want 1 for true: %q", n, out)
	}
}

// starship's continuation prompt (prompt --continuation) is used for the
// second and later lines of a command.
func TestStarshipContinuation(t *testing.T) {
	r := newTestREPL(t)
	fakeStarship(t, r)
	if got, want := r.readOpts().Continuation, "[prompt][--continuation]"; got != want {
		t.Errorf("continuation %q, want %q", got, want)
	}
}
