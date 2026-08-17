package editor

import (
	"strings"

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
// sequences (prompts).
func textWidth(s string) int {
	return ansi.StringWidthWc(s)
}

// cutWidth returns the part of an escape-containing string between
// display columns left and right.
func cutWidth(s string, left, right int) string {
	return ansi.CutWc(s, left, right)
}

// stripANSI removes escape sequences.
func stripANSI(s string) string { return ansi.Strip(s) }

// truncateWidth shortens s (plain text) to at most w cells, adding an
// ellipsis when something was cut.
func truncateWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		rw := runeWidth(r)
		if n+rw > w-1 {
			break
		}
		b.WriteString(displayRune(r))
		n += rw
	}
	b.WriteString("…")
	return b.String()
}

func plainWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}
