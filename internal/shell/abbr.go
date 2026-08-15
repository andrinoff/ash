package shell

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

type Abbr struct {
	Name      string
	Expansion string
	Position  string // "command" or "anywhere"
	Regex     *regexp.Regexp
	Function  string
	SetCursor string // marker, "" if unset
	Command   string // --command: only for this command
}

type abbrTable struct {
	mu    sync.RWMutex
	list  []*Abbr
	index map[string]*Abbr
}

func newAbbrTable() *abbrTable { return &abbrTable{index: map[string]*Abbr{}} }

func (t *abbrTable) add(a *Abbr) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if old, ok := t.index[a.Name]; ok {
		for i, x := range t.list {
			if x == old {
				t.list = append(t.list[:i], t.list[i+1:]...)
				break
			}
		}
	}
	t.index[a.Name] = a
	t.list = append(t.list, a)
}

func (t *abbrTable) remove(name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	a, ok := t.index[name]
	if !ok {
		return false
	}
	delete(t.index, name)
	for i, x := range t.list {
		if x == a {
			t.list = append(t.list[:i], t.list[i+1:]...)
			break
		}
	}
	return true
}

// Abbrs returns the defined abbreviations.
func (sh *Shell) Abbrs() []*Abbr {
	t := sh.shared.abbrs
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]*Abbr(nil), t.list...)
}

// ExpandAbbr returns the expansion for word, which is in command position
// if cmdPos is set. cmd is the current command name (for --command).
func (sh *Shell) ExpandAbbr(word string, cmdPos bool, cmd string) (string, bool) {
	t := sh.shared.abbrs
	t.mu.RLock()
	var match *Abbr
	if a, ok := t.index[word]; ok && a.Regex == nil {
		match = a
	} else {
		for _, a := range t.list {
			if a.Regex != nil && a.Regex.MatchString(word) {
				match = a
				break
			}
		}
	}
	t.mu.RUnlock()
	if match == nil {
		return "", false
	}
	if match.Command != "" {
		if cmdPos || cmd != match.Command {
			return "", false
		}
	} else if match.Position != "anywhere" && !cmdPos {
		return "", false
	}
	if match.Function != "" {
		buf := &lockedBuffer{}
		st, ok := sh.CallFunction(match.Function, []string{word}, &IO{In: &Stream{}, Out: &Stream{W: buf}, Err: sh.StdIO().Err})
		if !ok || st != 0 {
			return "", false
		}
		return strings.TrimRight(buf.String(), "\n"), true
	}
	return match.Expansion, true
}

// AbbrCursorMarker returns the --set-cursor marker for an abbreviation.
func (sh *Shell) AbbrCursorMarker(word string) string {
	t := sh.shared.abbrs
	t.mu.RLock()
	defer t.mu.RUnlock()
	if a, ok := t.index[word]; ok {
		return a.SetCursor
	}
	for _, a := range t.list {
		if a.Regex != nil && a.Regex.MatchString(word) {
			return a.SetCursor
		}
	}
	return ""
}

func bAbbr(sh *Shell, io *IO, args []string) int {
	t := sh.shared.abbrs
	a := args[1:]
	action := ""
	ab := &Abbr{Position: "command"}
	var regex string
	var rest []string
	for i := 0; i < len(a); i++ {
		o := a[i]
		val := func() string {
			if j := strings.IndexByte(o, '='); j > 0 && strings.HasPrefix(o, "--") {
				return o[j+1:]
			}
			if i+1 < len(a) {
				i++
				return a[i]
			}
			return ""
		}
		name := o
		if j := strings.IndexByte(o, '='); j > 0 && strings.HasPrefix(o, "--") {
			name = o[:j]
		}
		switch name {
		case "-a", "--add":
			action = "add"
		case "-e", "--erase":
			action = "erase"
		case "-r", "--rename":
			action = "rename"
		case "-s", "--show":
			action = "show"
		case "-l", "--list":
			action = "list"
		case "-q", "--query":
			action = "query"
		case "-p", "--position":
			ab.Position = val()
		case "--regex":
			regex = val()
		case "-f", "--function":
			ab.Function = val()
		case "--set-cursor":
			if strings.Contains(o, "=") {
				ab.SetCursor = val()
			} else {
				ab.SetCursor = "%"
			}
		case "-c", "--command":
			ab.Command = val()
		case "-g", "--global", "-U", "--universal":
		case "--":
			rest = append(rest, a[i+1:]...)
			i = len(a)
		default:
			rest = append(rest, o)
		}
	}
	if action == "" {
		if len(rest) == 0 {
			action = "show"
		} else {
			action = "add"
		}
	}
	switch action {
	case "show":
		for _, x := range sh.Abbrs() {
			var b strings.Builder
			b.WriteString("abbr -a")
			if x.Position == "anywhere" {
				b.WriteString(" --position anywhere")
			}
			if x.Command != "" {
				b.WriteString(" --command " + Quote(x.Command))
			}
			if x.SetCursor != "" {
				b.WriteString(" --set-cursor=" + Quote(x.SetCursor))
			}
			if x.Regex != nil {
				b.WriteString(" --regex " + Quote(x.Regex.String()))
			}
			b.WriteString(" -- " + Quote(x.Name))
			if x.Function != "" {
				b.WriteString(" --function " + Quote(x.Function))
			} else {
				b.WriteString(" " + Quote(x.Expansion))
			}
			fmt.Fprintln(io.Out, b.String())
		}
		return 0
	case "list":
		for _, x := range sh.Abbrs() {
			fmt.Fprintln(io.Out, x.Name)
		}
		return 0
	case "query":
		for _, n := range rest {
			t.mu.RLock()
			_, ok := t.index[n]
			t.mu.RUnlock()
			if !ok {
				return 1
			}
		}
		if len(rest) == 0 {
			return 1
		}
		return 0
	case "erase":
		status := 0
		for _, n := range rest {
			if !t.remove(n) {
				status = 4
			}
		}
		return status
	case "rename":
		if len(rest) != 2 {
			fmt.Fprintln(io.Err, "abbr --rename: expected two names")
			return 2
		}
		t.mu.Lock()
		x, ok := t.index[rest[0]]
		t.mu.Unlock()
		if !ok {
			return 1
		}
		t.remove(rest[0])
		cp := *x
		cp.Name = rest[1]
		t.add(&cp)
		return 0
	}
	if len(rest) == 0 {
		fmt.Fprintln(io.Err, "abbr: missing name")
		return 2
	}
	ab.Name = rest[0]
	if strings.ContainsAny(ab.Name, " \t\n") && regex == "" {
		fmt.Fprintf(io.Err, "abbr: '%s' cannot contain spaces\n", ab.Name)
		return 2
	}
	if ab.Function == "" {
		if len(rest) < 2 {
			fmt.Fprintf(io.Err, "abbr: missing expansion for '%s'\n", ab.Name)
			return 2
		}
		ab.Expansion = strings.Join(rest[1:], " ")
	}
	if regex != "" {
		re, err := regexp.Compile("^(?:" + regex + ")$")
		if err != nil {
			fmt.Fprintf(io.Err, "abbr: invalid regex: %v\n", err)
			return 2
		}
		ab.Regex = re
	}
	if ab.Position != "command" && ab.Position != "anywhere" {
		fmt.Fprintf(io.Err, "abbr: invalid position '%s'\n", ab.Position)
		return 2
	}
	t.add(ab)
	return 0
}
