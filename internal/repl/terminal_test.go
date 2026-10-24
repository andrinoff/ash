package repl

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/shell"
	"github.com/andrinoff/ash/internal/term"
)

// session is a REPL running on a pseudo-terminal: the editor reads keys
// in raw mode, and everything the shell prints lands on the screen.
type session struct {
	master *os.File
	mu     sync.Mutex
	screen bytes.Buffer
}

// onTerminal attaches r to a new pseudo-terminal.
func onTerminal(t *testing.T, r *REPL) *session {
	t.Helper()
	m, s := openPTY(t)
	p := &session{master: m}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := m.Read(buf)
			if n > 0 {
				p.mu.Lock()
				p.screen.Write(buf[:n])
				p.mu.Unlock()
				if bytes.Contains(buf[:n], []byte("\x1b[6n")) {
					m.WriteString("\x1b[1;1R") // answer cursor position queries
				}
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { s.Close(); m.Close() })
	r.in, r.out, r.tty = s, s, true
	r.sh.Stdin, r.sh.Stdout, r.sh.Stderr = s, s, s
	r.ed = editor.New(s, s, r.editorConfig())
	r.sh.Editor = r.ed
	return p
}

// waitFor waits until text appears on the screen.
func (p *session) waitFor(t *testing.T, text string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		p.mu.Lock()
		ok := strings.Contains(p.screen.String(), text)
		p.mu.Unlock()
		if ok {
			return
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	t.Fatalf("%q never appeared on the screen: %q", text, p.screen.String())
}

func (p *session) typ(keys string) { p.master.WriteString(keys) }

// SIGHUP at the prompt (kill -HUP, with the terminal still there) ends
// the session the way ctrl-d does, ash_exit handlers included, and takes
// the terminal out of the editor's raw mode. It happens only once even if
// the fallback timer fires too.
func TestHangupAtPrompt(t *testing.T) {
	r := newTestREPL(t)
	onTerminal(t, r)
	fd := int(r.in.Fd())
	cooked, err := term.GetState(fd)
	if err != nil {
		t.Fatal(err)
	}
	r.saved = cooked
	term.SetStateNow(fd, term.Raw(cooked)) // as the editor has it
	out := filepath.Join(t.TempDir(), "bye")
	run(t, r.sh, "function bye --on-event ash_exit; echo bye >> "+shell.Quote(out)+"; end")
	var codes []int
	saved := exitProcess
	exitProcess = func(code int) { codes = append(codes, code) }
	defer func() { exitProcess = saved }()

	r.hangup(true)
	r.hangup(false)
	if len(codes) != 1 || codes[0] != 129 {
		t.Errorf("exit codes %v, want [129]", codes)
	}
	if b, _ := os.ReadFile(out); string(b) != "bye\n" {
		t.Errorf("ash_exit handlers wrote %q, want them run once", b)
	}
	if st, err := term.GetState(fd); err != nil || st.Lflag&unix.ICANON == 0 || st.Lflag&unix.ECHO == 0 {
		t.Errorf("the terminal was left in raw mode (%v)", err)
	}
}

// `exit` in shell code bound to a key leaves the shell right away, not
// after the next command line (which then didn't run).
func TestKeyBindingExit(t *testing.T) {
	r := newTestREPL(t)
	p := onTerminal(t, r)
	run(t, r.sh, `bind ctrl-q 'exit 3'`)
	done := make(chan int, 1)
	go func() { done <- r.loop() }()
	p.waitFor(t, "❯")
	p.typ("echo hi\x11")
	select {
	case st := <-done:
		if st != 3 {
			t.Errorf("exit status %d, want 3", st)
		}
	case <-time.After(5 * time.Second):
		p.typ("\r")
		<-done
		t.Fatal("the shell kept reading the command line after exit")
	}
}
