package shell

import "testing"

// An external command that doesn't read its stdin must leave the rest of
// a heredoc for the commands after it, as it would with a file.
func TestHeredocSharedWithExternals(t *testing.T) {
	cases := []struct{ src, want string }{
		{"while read line; do echo \"got $line\"; /usr/bin/true; done <<EOF\na\nb\nc\nEOF\n", "got a\ngot b\ngot c\n"},
		{"while read l; do echo $l; /usr/bin/true; done <<< \"x\ny\"", "x\ny\n"},
		{"{ read a; echo first=$a; cat; } <<EOF\none\ntwo\nthree\nEOF\n", "first=one\ntwo\nthree\n"},
	}
	for _, c := range cases {
		out, errOut, _ := runScript(t, c.src)
		if out != c.want {
			t.Errorf("%q:\ngot  %q (stderr %q)\nwant %q", c.src, out, errOut, c.want)
		}
	}
}
