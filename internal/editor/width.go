package editor

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

const tabWidth = 4

// runeWidth is the number of terminal cells r occupies when drawn by the
// editor. Control characters are drawn in caret notation (^X).
func runeWidth(r rune) int {
	switch {
	case r == '\t':
		return tabWidth
	case r < 0x20 || r == 0x7f:
		return 2
	}
	return runewidth.RuneWidth(r)
}

// widths returns the number of cells each rune of rs takes when drawn by
// the editor. An emoji sequence (❤️ with its variation selector, 👍🏽,
// 👨‍👩‍👧) is as wide as terminals draw the whole sequence; that width is
// counted on its first rune and the rest take none, so the cursor never
// stops inside it. Other text is measured rune by rune.
func widths(rs []rune) []int {
	ws := make([]int, len(rs))
	var s string // rs as a string, built on the first sequence
	off := 0     // byte offset of rs[i] in s
	for i := 0; i < len(rs); {
		r := rs[i]
		ws[i] = runeWidth(r)
		n := utf8.RuneLen(r)
		if n < 0 {
			n = utf8.RuneLen(utf8.RuneError)
		}
		if i+1 == len(rs) || rs[i+1] < utf8.RuneSelf || r < 0x20 || r == 0x7f {
			off += n
			i++
			continue
		}
		if s == "" {
			s = string(rs)
		}
		c, w := ansi.FirstGraphemeCluster(s[off:], ansi.GraphemeWidth)
		k := utf8.RuneCountInString(c)
		if k > 1 && (ws[i] == 2 || strings.ContainsRune(c, '️')) {
			ws[i] = w
		} else {
			for j := i + 1; j < i+k; j++ {
				ws[j] = runeWidth(rs[j])
			}
		}
		off += len(c)
		i += k
	}
	return ws
}

// displayRune returns what is actually written for r.
func displayRune(r rune) string {
	switch {
	case r == '\t':
		return strings.Repeat(" ", tabWidth)
	case r < 0x20:
		return "^" + string(rune(r+'@'))
	case r == 0x7f:
		return "^?"
	}
	return string(r)
}

// textWidth is the display width of a string that may contain escape
// sequences (prompts), measured by grapheme cluster like terminals draw
// it (☁️ takes two cells).
func textWidth(s string) int {
	return ansi.StringWidth(s)
}

// cutWidth returns the part of an escape-containing string between
// display columns left and right. Escape sequences before left are kept,
// so the styles in effect carry over.
func cutWidth(s string, left, right int) string {
	// Not ansi.Cut: its WcWidth variant cuts the left side with Truncate
	// instead of TruncateLeft and so returns the first columns again.
	s = ansi.Truncate(s, right, "")
	if left > 0 {
		s = ansi.TruncateLeft(s, left, "")
	}
	return s
}

// stripANSI removes escape sequences.
func stripANSI(s string) string { return ansi.Strip(s) }

// truncateWidth shortens s (plain text) to at most w cells, adding an
// ellipsis when something was cut.
func truncateWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if plainWidth(s) <= w {
		return s
	}
	rs := []rune(s)
	ws := widths(rs)
	var b strings.Builder
	n := 0
	for i, r := range rs {
		if n+ws[i] > w-1 {
			break
		}
		b.WriteString(displayRune(r))
		n += ws[i]
	}
	b.WriteString("…")
	return b.String()
}

// printable returns s as the editor draws it: control characters in
// caret notation and tabs as spaces, so plainWidth is its real width.
func printable(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		b.WriteString(displayRune(r))
	}
	return b.String()
}

func plainWidth(s string) int {
	n := 0
	for _, w := range widths([]rune(s)) {
		n += w
	}
	return n
}
