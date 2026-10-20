package editor

import (
	"bytes"
	"runtime"
	"testing"
)

// Every read during a bracketed paste decodes the buffered input again;
// that must not copy the whole paste each time.
func TestDecodePartialPasteDoesNotCopy(t *testing.T) {
	buf := append([]byte("\x1b[200~"), bytes.Repeat([]byte("x"), 1<<20)...)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 10; i++ {
		if _, _, more := decodeKey(buf, false); !more {
			t.Fatal("an unterminated paste should wait for more input")
		}
	}
	runtime.ReadMemStats(&after)
	if d := after.TotalAlloc - before.TotalAlloc; d > 1<<20 {
		t.Fatalf("decoding an incomplete paste 10 times allocated %d bytes", d)
	}
}

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

// Key names with a backslash are names, not escape sequences: "ctrl-\"
// was parsed as the six keys c,t,r,l,-,\ (so typing c waited for them).
func TestParseKeySpecBackslash(t *testing.T) {
	cases := map[string]string{
		`ctrl-\`: `ctrl-\`,
		`alt-\`:  `alt-\`,
		`\`:      `\`,
		`\e\\`:   `alt-\`,
		`\c\`:    `ctrl-\`,
		`\cx`:    "ctrl-x",
		`\e[A`:   "up",
	}
	for spec, want := range cases {
		got, err := ParseKeySpec(spec)
		if err != nil || len(got) != 1 || got[0] != want {
			t.Errorf("ParseKeySpec(%q) = %q, %v; want %s", spec, got, err, want)
		}
	}
	// and the keys decode to the same names
	for in, want := range map[string]string{"\x1c": `ctrl-\`, "\x1b\\": `alt-\`} {
		if k, _, _ := decodeKey([]byte(in), true); k.Name() != want {
			t.Errorf("%q decodes to %q, want %q", in, k.Name(), want)
		}
	}
}

// A CSI sequence cut short by a byte that can't end it leaves that byte
// to be decoded as the next key: alt-[ then Enter used to swallow Enter.
func TestDecodeInterruptedCSI(t *testing.T) {
	cases := map[string][]string{
		"\x1b[\r":         {"alt-[", "enter"},
		"\x1b[\x1b[A":     {"alt-[", "up"},
		"\x1b[1;5\x1b[B":  {"", "down"},
		"\x1b[é":          {"alt-[", "é"},
		"\x1b[1;5C\x1b[D": {"ctrl-right", "left"},
	}
	for in, want := range cases {
		b := []byte(in)
		var got []string
		for len(b) > 0 {
			k, n, more := decodeKey(b, false)
			if more || n == 0 {
				t.Fatalf("%q: stuck at %q", in, b)
			}
			if k.Junk {
				got = append(got, "")
			} else {
				got = append(got, k.Name())
			}
			b = b[n:]
		}
		if sprintf("%q", got) != sprintf("%q", want) {
			t.Errorf("%q: got %q, want %q", in, got, want)
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
