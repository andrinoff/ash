package shell

import "testing"

func TestEchoEscapedBackslashC(t *testing.T) {
	// \\c is a backslash and a c, not the \c that ends the output.
	out, _, _ := runScript(t, `echo -e 'x\\cy' z; echo -e 'a\cb' c; echo; echo -e 'p\\\cq'; echo end`)
	if want := "x\\cy z\na\np\\end\n"; out != want { // what bash prints
		t.Fatalf("got %q, want %q", out, want)
	}
}
