package editor

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Mod is a set of key modifiers.
type Mod uint8

const (
	ModCtrl Mod = 1 << iota
	ModAlt
	ModShift
	ModSuper
)

// KeyCode identifies non-character keys.
type KeyCode uint16

const (
	KeyNone KeyCode = iota
	KeyEnter
	KeyTab
	KeyBackspace
	KeyEscape
	KeySpace
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPgUp
	KeyPgDn
	KeyInsert
	KeyDelete
	KeyF1
)

// Key is one decoded key press.
type Key struct {
	Code  KeyCode
	Rune  rune
	Mod   Mod
	Paste string // bracketed paste contents
	Raw   string // original bytes, for self-insert of odd sequences
	Focus int    // 1 focus in, -1 focus out
	Junk  bool   // a terminal response we should ignore
}

func (k Key) IsPaste() bool { return k.Paste != "" || k.Raw == "\x1b[200~" }

// Name returns the canonical binding name of a key, e.g. "ctrl-a",
// "alt-left", "f5", "a", "shift-tab".
func (k Key) Name() string {
	var b strings.Builder
	if k.Mod&ModCtrl != 0 {
		b.WriteString("ctrl-")
	}
	if k.Mod&ModAlt != 0 {
		b.WriteString("alt-")
	}
	if k.Mod&ModShift != 0 {
		b.WriteString("shift-")
	}
	if k.Mod&ModSuper != 0 {
		b.WriteString("super-")
	}
	switch k.Code {
	case KeyNone:
		if k.Rune == ' ' {
			b.WriteString("space")
		} else {
			b.WriteRune(k.Rune)
		}
	case KeyEnter:
		b.WriteString("enter")
	case KeyTab:
		b.WriteString("tab")
	case KeyBackspace:
		b.WriteString("backspace")
	case KeyEscape:
		b.WriteString("escape")
	case KeySpace:
		b.WriteString("space")
	case KeyUp:
		b.WriteString("up")
	case KeyDown:
		b.WriteString("down")
	case KeyLeft:
		b.WriteString("left")
	case KeyRight:
		b.WriteString("right")
	case KeyHome:
		b.WriteString("home")
	case KeyEnd:
		b.WriteString("end")
	case KeyPgUp:
		b.WriteString("pageup")
	case KeyPgDn:
		b.WriteString("pagedown")
	case KeyInsert:
		b.WriteString("insert")
	case KeyDelete:
		b.WriteString("delete")
	default:
		if k.Code >= KeyF1 {
			b.WriteString("f" + strconv.Itoa(int(k.Code-KeyF1)+1))
		}
	}
	return b.String()
}

// decodeKey decodes one key from the front of buf. It returns the key,
// the number of bytes used, and needMore=true if buf ends in the middle
// of a sequence (the caller decides after a short timeout).
func decodeKey(buf []byte, final bool) (Key, int, bool) {
	if len(buf) == 0 {
		return Key{}, 0, true
	}
	c := buf[0]
	switch {
	case c == 0x1b:
		return decodeEscape(buf, final)
	case c == '\r' || c == '\n':
		return Key{Code: KeyEnter, Raw: string(c)}, 1, false
	case c == '\t':
		return Key{Code: KeyTab, Raw: "\t"}, 1, false
	case c == 0x7f:
		return Key{Code: KeyBackspace, Raw: "\x7f"}, 1, false
	case c == 0:
		return Key{Rune: ' ', Mod: ModCtrl, Raw: "\x00"}, 1, false
	case c < 0x20:
		r := rune(c) + 'a' - 1
		if c > 0x1a {
			// ctrl-\ ctrl-] ctrl-^ ctrl-_ (ctrl-/ sends the last one too)
			r = rune(c) + '@'
		}
		return Key{Rune: r, Mod: ModCtrl, Raw: string(c)}, 1, false
	}
	if !utf8.FullRune(buf) {
		if final {
			return Key{Rune: utf8.RuneError}, 1, false
		}
		return Key{}, 0, true
	}
	r, n := utf8.DecodeRune(buf)
	return Key{Rune: r, Raw: string(buf[:n])}, n, false
}

func decodeEscape(buf []byte, final bool) (Key, int, bool) {
	if len(buf) == 1 {
		if final {
			return Key{Code: KeyEscape, Raw: "\x1b"}, 1, false
		}
		return Key{}, 0, true
	}
	switch buf[1] {
	case '[':
		return decodeCSI(buf, final)
	case 'O':
		if len(buf) < 3 {
			if final {
				return Key{Rune: 'O', Mod: ModAlt}, 2, false
			}
			return Key{}, 0, true
		}
		k := Key{Raw: string(buf[:3])}
		switch buf[2] {
		case 'A':
			k.Code = KeyUp
		case 'B':
			k.Code = KeyDown
		case 'C':
			k.Code = KeyRight
		case 'D':
			k.Code = KeyLeft
		case 'H':
			k.Code = KeyHome
		case 'F':
			k.Code = KeyEnd
		case 'P', 'Q', 'R', 'S':
			k.Code = KeyF1 + KeyCode(buf[2]-'P')
		case 'M':
			k.Code = KeyEnter
		default:
			k.Junk = true
		}
		return k, 3, false
	case ']', 'P', '_', '^':
		// OSC / DCS / APC / PM: a terminal response, skip to ST or BEL
		for i := 2; i < len(buf); i++ {
			if buf[i] == 0x07 {
				return Key{Junk: true}, i + 1, false
			}
			if buf[i] == 0x1b && i+1 < len(buf) && buf[i+1] == '\\' {
				return Key{Junk: true}, i + 2, false
			}
		}
		if final {
			return Key{Junk: true}, len(buf), false
		}
		return Key{}, 0, true
	case 0x1b:
		// ESC ESC: alt-escape, or alt + an escape sequence
		k, n, more := decodeEscape(buf[1:], final)
		if more {
			return k, 0, true
		}
		if k.Code == KeyEscape {
			return Key{Code: KeyEscape, Mod: ModAlt}, 2, false
		}
		k.Mod |= ModAlt
		return k, n + 1, false
	}
	// alt + key
	k, n, more := decodeKey(buf[1:], final)
	if more {
		return k, 0, true
	}
	k.Mod |= ModAlt
	k.Raw = string(buf[:n+1])
	return k, n + 1, false
}

func decodeCSI(buf []byte, final bool) (Key, int, bool) {
	// ESC [ params intermediates final
	i := 2
	for i < len(buf) && buf[i] >= 0x30 && buf[i] <= 0x3f {
		i++
	}
	for i < len(buf) && buf[i] >= 0x20 && buf[i] <= 0x2f {
		i++
	}
	if i >= len(buf) {
		if final {
			return Key{Rune: '[', Mod: ModAlt}, 2, false
		}
		return Key{}, 0, true
	}
	fin := buf[i]
	if fin < 0x40 || fin > 0x7e {
		// Not a final byte: the sequence was cut short (alt-[, then a
		// key). Leave that byte for the next key instead of eating it.
		if i == 2 {
			return Key{Rune: '[', Mod: ModAlt, Raw: "\x1b["}, 2, false
		}
		return Key{Junk: true}, i, false
	}
	n := i + 1
	params := string(buf[2:i])
	raw := string(buf[:n])
	k := Key{Raw: raw}
	if strings.HasPrefix(params, "?") || strings.HasPrefix(params, ">") || strings.Contains(params, "$") {
		k.Junk = true // DA, mode reports, kitty flags reply
		return k, n, false
	}
	parts := strings.Split(params, ";")
	num := func(i int) int {
		if i < len(parts) {
			s := parts[i]
			if j := strings.IndexByte(s, ':'); j >= 0 {
				s = s[:j]
			}
			v, _ := strconv.Atoi(s)
			return v
		}
		return 0
	}
	mods := func(v int) Mod {
		if v <= 1 {
			return 0
		}
		v--
		var m Mod
		if v&1 != 0 {
			m |= ModShift
		}
		if v&2 != 0 {
			m |= ModAlt
		}
		if v&4 != 0 {
			m |= ModCtrl
		}
		if v&8 != 0 {
			m |= ModSuper
		}
		return m
	}
	switch fin {
	case 'A', 'B', 'C', 'D', 'H', 'F':
		k.Code = map[byte]KeyCode{'A': KeyUp, 'B': KeyDown, 'C': KeyRight, 'D': KeyLeft, 'H': KeyHome, 'F': KeyEnd}[fin]
		if len(parts) > 1 {
			k.Mod = mods(num(1))
		}
	case 'P', 'Q', 'S':
		k.Code = KeyF1 + KeyCode(fin-'P')
		if len(parts) > 1 {
			k.Mod = mods(num(1))
		}
	case 'R':
		// cursor position report (or F3 with modifiers on some terminals)
		if len(parts) == 2 && num(0) > 1 {
			k.Junk = true
		} else {
			k.Code = KeyF1 + 2
			k.Mod = mods(num(1))
		}
	case 'Z':
		k.Code = KeyTab
		k.Mod = ModShift
	case 'I':
		k.Focus = 1
		k.Junk = true
	case 'O':
		k.Focus = -1
		k.Junk = true
	case '~':
		code := num(0)
		if len(parts) > 1 {
			k.Mod = mods(num(1))
		}
		switch code {
		case 1, 7:
			k.Code = KeyHome
		case 2:
			k.Code = KeyInsert
		case 3:
			k.Code = KeyDelete
		case 4, 8:
			k.Code = KeyEnd
		case 5:
			k.Code = KeyPgUp
		case 6:
			k.Code = KeyPgDn
		case 11, 12, 13, 14, 15:
			k.Code = KeyF1 + KeyCode(code-11)
		case 17, 18, 19, 20, 21:
			k.Code = KeyF1 + 5 + KeyCode(code-17)
		case 23, 24:
			k.Code = KeyF1 + 10 + KeyCode(code-23)
		case 27:
			// xterm modifyOtherKeys: CSI 27 ; mod ; code ~
			k = keyFromCodepoint(num(2), mods(num(1)))
			k.Raw = raw
		case 200:
			// bracketed paste: read until ESC [ 201 ~
			end := bytes.Index(buf[n:], []byte("\x1b[201~"))
			if end < 0 {
				if final {
					return Key{Paste: string(buf[n:])}, len(buf), false
				}
				return Key{}, 0, true
			}
			return Key{Paste: string(buf[n : n+end])}, n + end + 6, false
		default:
			k.Junk = true
		}
	case 'u':
		// kitty keyboard protocol: CSI codepoint ; mods u
		k2 := keyFromCodepoint(num(0), mods(num(1)))
		k2.Raw = raw
		return k2, n, false
	case 'M', 'm', 't', 'T', 'c', 'n', 'y', 'x':
		k.Junk = true
	default:
		k.Junk = true
	}
	return k, n, false
}

func keyFromCodepoint(cp int, m Mod) Key {
	switch cp {
	case 13:
		return Key{Code: KeyEnter, Mod: m}
	case 9:
		return Key{Code: KeyTab, Mod: m}
	case 27:
		return Key{Code: KeyEscape, Mod: m}
	case 127, 8:
		return Key{Code: KeyBackspace, Mod: m}
	case 32:
		if m == 0 {
			return Key{Rune: ' '}
		}
		return Key{Rune: ' ', Mod: m}
	}
	r := rune(cp)
	if m&ModShift != 0 && r >= 'a' && r <= 'z' && m&(ModCtrl|ModAlt) == 0 {
		return Key{Rune: r - 32}
	}
	return Key{Rune: r, Mod: m}
}

// ParseKeySpec parses a key binding specification. It accepts fish 4
// names ("ctrl-o", "alt-left", "shift-tab", "f1"), comma or space
// separated chords ("ctrl-x,ctrl-e"), and legacy escapes ("\co", "\e[A",
// "\cl" or the raw control characters the shell already produced).
func ParseKeySpec(spec string) ([]string, error) {
	if spec == "" {
		return nil, errorf("empty key")
	}
	// legacy/raw byte sequences: "\e[A", "\cx", raw control characters
	// (but "ctrl-\" is a key name)
	if spec[0] == '\\' || strings.ContainsRune(spec, 0x1b) || spec[0] < 0x20 || spec[0] == 0x7f {
		raw := unescapeKey(spec)
		var out []string
		b := []byte(raw)
		for len(b) > 0 {
			k, n, more := decodeKey(b, true)
			if more || n == 0 {
				return nil, errorf("cannot parse key %q", spec)
			}
			b = b[n:]
			out = append(out, k.Name())
		}
		return out, nil
	}
	if spec == "," || spec == "-" {
		return []string{spec}, nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ' ' }) {
		name, err := normalizeKeyName(part)
		if err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, errorf("empty key")
	}
	return out, nil
}

func normalizeKeyName(s string) (string, error) {
	if utf8.RuneCountInString(s) == 1 {
		return s, nil
	}
	lower := strings.ToLower(s)
	var mods []string
	for {
		matched := false
		for _, m := range []string{"ctrl-", "c-", "alt-", "a-", "m-", "meta-", "shift-", "s-", "super-"} {
			if strings.HasPrefix(lower, m) && len(lower) > len(m) {
				switch m {
				case "c-":
					m = "ctrl-"
				case "a-", "m-", "meta-":
					m = "alt-"
				case "s-":
					m = "shift-"
				}
				mods = append(mods, m)
				lower = lower[len(strings.SplitN(lower, "-", 2)[0])+1:]
				s = s[len(s)-len(lower):]
				matched = true
				break
			}
		}
		if !matched {
			break
		}
	}
	base := lower
	switch base {
	case "return", "ret", "cr":
		base = "enter"
	case "esc":
		base = "escape"
	case "bs", "bspace":
		base = "backspace"
	case "del":
		base = "delete"
	case "pgup", "page-up":
		base = "pageup"
	case "pgdn", "pagedown", "page-down":
		base = "pagedown"
	case "ins":
		base = "insert"
	case "spc":
		base = "space"
	}
	if utf8.RuneCountInString(s) == 1 {
		base = s
		if len(mods) > 0 {
			base = strings.ToLower(base)
		}
	}
	known := map[string]bool{"enter": true, "tab": true, "backspace": true, "escape": true, "space": true, "up": true, "down": true, "left": true, "right": true, "home": true, "end": true, "pageup": true, "pagedown": true, "insert": true, "delete": true}
	if !known[base] && utf8.RuneCountInString(base) != 1 && !(strings.HasPrefix(base, "f") && isDigits(base[1:])) {
		return "", errorf("unknown key %q", s)
	}
	// canonical modifier order: ctrl, alt, shift, super
	order := []string{"ctrl-", "alt-", "shift-", "super-"}
	var b strings.Builder
	for _, o := range order {
		for _, m := range mods {
			if m == o {
				b.WriteString(o)
				break
			}
		}
	}
	b.WriteString(base)
	return b.String(), nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// unescapeKey turns "\cx", "\e", "\t", "\r", "\n", "\x7f" notation into bytes.
func unescapeKey(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'e', 'E':
			b.WriteByte(0x1b)
		case 'c':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i] & 0x1f)
			}
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte(0x08)
		case 'x':
			v, n := 0, 0
			for n < 2 && i+1 < len(s) && isHex(s[i+1]) {
				i++
				v = v*16 + hexVal(s[i])
				n++
			}
			b.WriteByte(byte(v))
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	}
	return int(c-'A') + 10
}

type editorError struct{ s string }

func (e *editorError) Error() string { return e.s }

func errorf(format string, args ...any) error {
	return &editorError{s: sprintf(format, args...)}
}
