package shell

import "testing"

// testEditor is a minimal Editor for the commandline builtin.
type testEditor struct {
	buf    string
	cursor int
}

func (e *testEditor) Buffer() (string, int)                 { return e.buf, e.cursor }
func (e *testEditor) SetBuffer(b string, c int)             { e.buf, e.cursor = b, c }
func (e *testEditor) QueueFunctions(...string)              {}
func (e *testEditor) Bind(mode, key, cmd, set string) error { return nil }
func (e *testEditor) Unbind(mode, key string) bool          { return true }
func (e *testEditor) Bindings() []string                    { return nil }
func (e *testEditor) SetKeymap(string)                      {}
func (e *testEditor) Keymap() string                        { return "emacs" }
func (e *testEditor) Repaint()                              {}
func (e *testEditor) PagerActive() bool                     { return false }
func (e *testEditor) Reading() bool                         { return true }

func TestCommandlineTokensOfEveryProcess(t *testing.T) {
	buf := "git commit -m 'a msg' | grep foo; echo hi"
	for _, c := range []struct {
		cursor    int
		cmd, want string
	}{
		// -o used to keep only the tokens after the last | ; or &
		{len(buf), "commandline -o", "git\ncommit\n-m\na msg\ngrep\nfoo\necho\nhi\n"},
		{len("git commit -m 'a msg' | gr"), "commandline -opc", "gr\n"},
		{len("git commit -m 'a msg' | gr"), "commandline -oc", "git\ncommit\n-m\na msg\ngr\n"},
	} {
		sh := New()
		sh.Init()
		sh.Editor = &testEditor{buf: buf, cursor: c.cursor}
		out := &lockedBuffer{}
		io := &IO{In: &Stream{}, Out: &Stream{W: out}, Err: &Stream{W: out}}
		sh.RunString(c.cmd, "test.fish", io)
		if out.String() != c.want {
			t.Errorf("%s at %d: got %q, want %q", c.cmd, c.cursor, out.String(), c.want)
		}
	}
}
