package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChildActions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	sh := New()
	sh.Init()
	cleanup := sh.SetupActionFile()
	defer cleanup()
	dir := t.TempDir()
	// cd in the main shell moves the whole process; put it back before the
	// temp dirs go away (cleanups run last-registered first)
	orig, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(orig) })
	sub := filepath.Join(dir, "with space")
	os.Mkdir(sub, 0o755)
	out := &lockedBuffer{}
	io := &IO{In: &Stream{}, Out: &Stream{W: out}, Err: &Stream{W: out}}

	// a program writes a bare directory (yazi --cwd-file, LAZYGIT_NEW_DIR_FILE)
	os.WriteFile(sh.ActionFile(), []byte(sub+"\n"), 0o600)
	if !sh.ApplyChildActions(io) || sh.Dir() != sub {
		t.Fatalf("bare path: dir is %q", sh.Dir())
	}
	if _, err := os.Stat(sh.ActionFile()); !os.IsNotExist(err) {
		t.Fatal("the action file should be removed after use")
	}

	// shell-quoted requests (nnn writes cd '...')
	os.WriteFile(sh.ActionFile(), []byte("cd '"+dir+"'\nexport FOO=bar\ncommandline 'git push'\nmessage done here\n"), 0o600)
	sh.ApplyChildActions(io)
	if sh.Dir() != dir || sh.Get("FOO") != "bar" || sh.TakeNextBuffer() != "git push" {
		t.Fatalf("got dir=%q FOO=%q", sh.Dir(), sh.Get("FOO"))
	}
	if !strings.Contains(out.String(), "done here") {
		t.Fatalf("message not printed: %q", out.String())
	}

	// nothing that needs expansion or runs code
	os.WriteFile(sh.ActionFile(), []byte("cd $(touch "+filepath.Join(dir, "pwned")+")\nrm -rf /tmp/x\nexport X=$(id)\n"), 0o600)
	sh.ApplyChildActions(io)
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("an action ran code")
	}
	if sh.IsSet("X") {
		t.Fatal("an action expanded a command substitution")
	}

	// the ash script side: a function writes the file, the REPL applies it
	if st := sh.RunString(`echo `+Quote(sub)+` > $ASH_ACTION_FILE`, "", io); st != 0 {
		t.Fatalf("status %d", st)
	}
	sh.ApplyChildActions(io)
	if sh.Dir() != sub {
		t.Fatalf("after echo > $ASH_ACTION_FILE dir is %q", sh.Dir())
	}
}
