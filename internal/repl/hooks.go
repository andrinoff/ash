package repl

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/shell"
)

// expandAbbr expands the word before the cursor: abbreviations in
// command position, and !! / !$ history shortcuts anywhere.
func (r *REPL) expandAbbr(buf string, cursor int) (string, int, bool) {
	start := cursor
	for start > 0 && !strings.ContainsRune(" \t\n;|&(", rune(buf[start-1])) {
		start--
	}
	tok := buf[start:cursor]
	if tok == "" {
		return "", 0, false
	}
	var exp string
	switch tok {
	case "!!":
		recent := r.hist.Recent(1)
		if len(recent) == 0 {
			return "", 0, false
		}
		exp = recent[0]
	case "!$":
		recent := r.hist.Recent(1)
		if len(recent) == 0 {
			return "", 0, false
		}
		words := shell.CommandTokens(recent[0])
		if len(words) == 0 {
			return "", 0, false
		}
		exp = shell.Quote(words[len(words)-1])
	default:
		cmdPos, cmd := commandPosition(buf[:start])
		var ok bool
		exp, ok = r.sh.ExpandAbbr(tok, cmdPos, cmd)
		if !ok {
			return "", 0, false
		}
		if marker := r.sh.AbbrCursorMarker(tok); marker != "" {
			if i := strings.Index(exp, marker); i >= 0 {
				ns := buf[:start] + exp[:i] + exp[i+len(marker):] + buf[cursor:]
				return ns, start + i, true
			}
		}
	}
	return buf[:start] + exp + buf[cursor:], start + len(exp), true
}

var commandKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true,
	"until": true, "and": true, "or": true, "not": true, "begin": true, "!": true,
	"time": true, "{": true, "exec": true,
}

// commandPosition reports whether the next word would be a command name,
// and the command of the current process if not.
func commandPosition(prefix string) (bool, string) {
	i := strings.LastIndexAny(prefix, ";|&(\n")
	seg := prefix[i+1:]
	words := strings.Fields(seg)
	for len(words) > 0 && (commandKeywords[words[0]] || strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "=")) {
		words = words[1:]
	}
	if len(words) == 0 {
		return true, ""
	}
	return false, words[0]
}

// suggest returns the fish-style autosuggestion for buf.
func (r *REPL) suggest(buf string) string {
	if strings.TrimSpace(buf) == "" {
		return ""
	}
	s := r.hist.Suggest(buf, r.sh.Dir())
	if s == "" || !strings.HasPrefix(s, buf) {
		return ""
	}
	if !r.suggestionOK(s) {
		return ""
	}
	return s
}

func (r *REPL) searchHistory(query, scope string, limit int) []editor.SearchResult {
	return r.hist.Fuzzy(query, limit, r.sh.HomeDir())
}

// editCommand opens the command line in $VISUAL or $EDITOR.
func (r *REPL) editCommand(buf string) (string, bool) {
	sh := r.sh
	ed := sh.Get("VISUAL")
	if ed == "" {
		ed = sh.Get("EDITOR")
	}
	if ed == "" {
		ed = "vi"
	}
	f, err := os.CreateTemp("", "ash-cmdline-*.ash")
	if err != nil {
		return "", false
	}
	name := f.Name()
	defer os.Remove(name)
	f.WriteString(buf + "\n")
	f.Close()
	sh.RunString(ed+" "+shell.Quote(name), "", sh.StdIO())
	b, err := os.ReadFile(name)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// relTime formats how long ago t was, compactly.
func relTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return itoa(int(d.Hours())) + "h"
	case d < 365*24*time.Hour:
		return itoa(int(d.Hours()/24)) + "d"
	}
	return itoa(int(d.Hours()/24/365)) + "y"
}

func itoa(n int) string { return strconv.Itoa(n) }

func shortDir(dir, home string) string {
	d := tildePath(dir, home)
	if len(d) > 30 {
		d = "…/" + filepath.Base(dir)
	}
	return d
}
