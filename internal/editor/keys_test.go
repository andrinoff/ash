package editor

import "testing"

func TestDecodeControlPunctuation(t *testing.T) {
	cases := map[string]string{
		"\x1c": `ctrl-\`,
		"\x1d": "ctrl-]",
		"\x1e": "ctrl-^",
		"\x1f": "ctrl-_",
		"\x1a": "ctrl-z",
		"\x01": "ctrl-a",
	}
	for in, want := range cases {
		k, n, more := decodeKey([]byte(in), false)
		if more || n != 1 || k.Name() != want {
			t.Errorf("%q: got %q (n=%d more=%v), want %q", in, k.Name(), n, more, want)
		}
	}
	for spec, want := range map[string]string{`\c_`: "ctrl-_", "ctrl-_": "ctrl-_", `\c]`: "ctrl-]"} {
		got, err := ParseKeySpec(spec)
		if err != nil || len(got) != 1 || got[0] != want {
			t.Errorf("ParseKeySpec(%q) = %v, %v; want %s", spec, got, err, want)
		}
	}
}

// ctrl-_ (and ctrl-/, which terminals send as the same byte) is undo.
func TestCtrlUnderscoreUndoes(t *testing.T) {
	e := newTestEditor(80)
	e.input = []byte("echo hi\x1f")
	e.processInput(false)
	if got := string(e.buf); got != "echo" {
		t.Fatalf("ctrl-_ didn't undo the last word: %q", got)
	}
}
