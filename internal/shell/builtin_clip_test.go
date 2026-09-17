package shell

import (
	"bytes"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type nopWriteCloser struct{ *bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }

// clipShell has fake clipboard programs that record what they're given.
func clipShell(t *testing.T) (*Shell, string, *bytes.Buffer) {
	t.Helper()
	sh, _ := newCompShell(t)
	bin := sh.Get("PATH")
	for name, src := range map[string]string{
		"pbcopy":  "#!/bin/sh\nexec /bin/cat > \"$0.got\"\n",
		"pbpaste": "#!/bin/sh\nprintf 'from the clipboard'\n",
		"tmux":    "#!/bin/sh\necho \"$*\" > \"$0.args\"; exec /bin/cat > \"$0.got\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(src), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tty := &bytes.Buffer{}
	orig := clipTTY
	clipTTY = func() (io.WriteCloser, error) { return nopWriteCloser{tty}, nil }
	t.Cleanup(func() { clipTTY = orig })
	for _, v := range []string{"SSH_TTY", "SSH_CONNECTION", "TMUX"} {
		sh.UnsetVar(v, setGlobal)
	}
	return sh, bin, tty
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClip(t *testing.T) {
	sh, bin, tty := clipShell(t)
	if _, errs, st := runIn(sh, "clip hello world", ""); st != 0 || errs != "" {
		t.Fatalf("status %d, %q", st, errs)
	}
	if got := readFile(t, filepath.Join(bin, "pbcopy.got")); got != "hello world" {
		t.Errorf("copied %q", got)
	}
	if _, _, st := runIn(sh, "printf 'a\\nb\\n' | clip", ""); st != 0 {
		t.Fatalf("from a pipe: status %d", st)
	}
	if got := readFile(t, filepath.Join(bin, "pbcopy.got")); got != "a\nb\n" {
		t.Errorf("copied %q", got)
	}
	if out, _, st := runIn(sh, "clip -o", ""); st != 0 || out != "from the clipboard" {
		t.Errorf("paste: %q, status %d", out, st)
	}
	if tty.Len() != 0 {
		t.Errorf("wrote to the terminal: %q", tty.String())
	}
	if _, errs, st := runIn(sh, "clip --bogus", ""); st != 2 || errs == "" {
		t.Errorf("bad option: status %d, %q", st, errs)
	}
}

func TestClipOverSSH(t *testing.T) {
	sh, bin, tty := clipShell(t)
	sh.SetVar("SSH_TTY", []string{"/dev/ttys009"}, setOpts{scope: setGlobal, export: 1})
	if _, _, st := runIn(sh, "clip remote text", ""); st != 0 {
		t.Fatalf("status %d", st)
	}
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("remote text")) + "\x07"
	if tty.String() != want {
		t.Errorf("terminal got %q, want %q", tty.String(), want)
	}
	if _, err := os.Stat(filepath.Join(bin, "pbcopy.got")); err == nil {
		t.Error("used the clipboard of the remote machine")
	}
	if _, errs, st := runIn(sh, "clip -o", ""); st != 1 || errs == "" {
		t.Errorf("paste over SSH: status %d, %q", st, errs)
	}

	// in tmux, through tmux's buffers
	tty.Reset()
	sh.SetVar("TMUX", []string{"/tmp/tmux-501/default,1,0"}, setOpts{scope: setGlobal, export: 1})
	if _, _, st := runIn(sh, "clip in tmux", ""); st != 0 {
		t.Fatalf("status %d", st)
	}
	if got := readFile(t, filepath.Join(bin, "tmux.args")); got != "load-buffer -w -\n" {
		t.Errorf("tmux got %q", got)
	}
	if got := readFile(t, filepath.Join(bin, "tmux.got")); got != "in tmux" || tty.Len() != 0 {
		t.Errorf("tmux buffer %q, terminal %q", got, tty.String())
	}
}
