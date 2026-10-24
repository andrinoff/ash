package repl

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrinoff/ash/internal/editor"
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
