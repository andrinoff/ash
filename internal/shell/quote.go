package shell

import (
	"os"
	"strings"
)

// Quote returns s quoted so the shell reads it back as one word.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("@%+=:,./-_", c) >= 0 || c >= 0x80) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	if !strings.ContainsAny(s, "'") {
		return "'" + s + "'"
	}
	if !strings.ContainsAny(s, "\"\\$`") {
		return "\"" + s + "\""
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// unescapeEcho processes backslash escapes the way `echo -e` does.
func unescapeEcho(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'a':
			b.WriteByte(7)
		case 'b':
			b.WriteByte(8)
		case 'e', 'E':
			b.WriteByte(27)
		case 'f':
			b.WriteByte(12)
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte(11)
		case '\\':
			b.WriteByte('\\')
		case '0':
			v, n := 0, 0
			for n < 3 && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '7' {
				i++
				v = v*8 + int(s[i]-'0')
				n++
			}
			b.WriteByte(byte(v))
		case 'x':
			v, n := 0, 0
			for n < 2 && i+1 < len(s) && isHex(s[i+1]) {
				i++
				v = v*16 + hexVal(s[i])
				n++
			}
			if n == 0 {
				b.WriteString("\\x")
			} else {
				b.WriteByte(byte(v))
			}
		case 'u', 'U':
			max := 4
			if s[i] == 'U' {
				max = 8
			}
			v, n := 0, 0
			for n < max && i+1 < len(s) && isHex(s[i+1]) {
				i++
				v = v*16 + hexVal(s[i])
				n++
			}
			if n == 0 {
				b.WriteByte('\\')
				b.WriteByte(s[i])
			} else {
				b.WriteRune(rune(v))
			}
		case 'c':
			return b.String()
		default:
			b.WriteByte('\\')
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

// envOr returns an environment variable or a default.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
