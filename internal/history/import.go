package history

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// importers parse the history files of other shells into entries, in file
// order, with a zero time where the file has none.
var importers = map[string]func(string) []item{
	"fish": parseFish,
	"zsh":  parseZsh,
	"bash": parseBash,
}

// autoImportKinds are the shells AutoImport looks for, in order.
var autoImportKinds = []string{"fish", "zsh", "bash"}

// SourcePath returns where a shell of the given kind (fish, zsh or bash)
// keeps its history, looking up HOME, XDG_DATA_HOME, HISTFILE and ZDOTDIR
// with getenv: $XDG_DATA_HOME/fish/fish_history or
// ~/.local/share/fish/fish_history, $HISTFILE or ${ZDOTDIR:-~}/.zsh_history,
// and ~/.bash_history.
func SourcePath(kind string, getenv func(string) string) (string, error) {
	if _, ok := importers[kind]; !ok {
		return "", unknownKind(kind)
	}
	if kind == "fish" {
		if d := getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
			return filepath.Join(d, "fish", "fish_history"), nil
		}
	}
	if kind == "zsh" {
		if f := getenv("HISTFILE"); f != "" {
			return f, nil
		}
		if d := getenv("ZDOTDIR"); d != "" {
			return filepath.Join(d, ".zsh_history"), nil
		}
	}
	home := getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", err
		}
	}
	switch kind {
	case "fish":
		return filepath.Join(home, ".local", "share", "fish", "fish_history"), nil
	case "zsh":
		return filepath.Join(home, ".zsh_history"), nil
	}
	return filepath.Join(home, ".bash_history"), nil
}

func unknownKind(kind string) error {
	return fmt.Errorf("unknown kind of history %q (want fish, zsh or bash)", kind)
}

// Import adds the history of another shell (fish, zsh or bash), read from
// where SourcePath says that shell keeps it, according to the environment.
// It returns how many commands it added.
func (s *Store) Import(kind string) (int, error) {
	path, err := SourcePath(kind, os.Getenv)
	if err != nil {
		return 0, err
	}
	return s.ImportFile(kind, path)
}

// ImportFile adds the commands in a fish, zsh or bash history file to the
// history, and returns how many it added. Commands already in the history
// are skipped, and so are older occurrences of the same command. Imported
// commands keep their times (where the file has none, they get their
// neighbours', or the file's modification time), and take their place in
// the history file in order of time.
func (s *Store) ImportFile(kind, path string) (int, error) {
	parse, ok := importers[kind]
	if !ok {
		return 0, unknownKind(kind)
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	fi, err := f.Stat()
	if err == nil && fi.IsDir() {
		err = fmt.Errorf("%s is a directory", path)
	}
	var data string
	if err == nil {
		data, err = readAll(f, fi.Size())
	}
	f.Close()
	if err != nil {
		return 0, err
	}
	recs := prepareImport(parse(data), fi.ModTime().Unix())
	<-s.loaded
	s.wmu.Lock()
	defer s.wmu.Unlock()
	n := 0
	err = s.rewrite(true, func(old []item) ([]item, bool) {
		var merged []item
		merged, n = mergeImport(old, recs)
		return merged, n > 0
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// AutoImport imports the history of the shell the user comes from (fish if
// it has a history, else zsh, else bash) if the history file didn't exist
// when the store was opened, and returns how many commands it added. It
// only ever does so once. It takes a moment, so call it in the background.
func (s *Store) AutoImport() (int, error) {
	<-s.loaded
	s.mu.Lock()
	fresh := s.fresh
	s.fresh = false
	s.mu.Unlock()
	if !fresh {
		return 0, nil
	}
	for _, kind := range autoImportKinds {
		path, err := SourcePath(kind, os.Getenv)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
			return s.ImportFile(kind, path)
		}
	}
	return 0, nil
}

// prepareImport makes parsed entries ready to import: it drops the ones Add
// would ignore, gives a time to those without one, sorts them by time and
// keeps the newest occurrence of each command.
func prepareImport(recs []item, mtime int64) []item {
	out := recs[:0]
	for _, it := range recs {
		it.cmd = trimCommand(it.cmd)
		if it.cmd != "" && it.cmd[0] != ' ' {
			out = append(out, it)
		}
	}
	recs = out
	last := mtime
	for _, it := range recs {
		if it.when > 0 {
			last = it.when
			break
		}
	}
	for i := range recs {
		if recs[i].when > 0 {
			last = recs[i].when
		} else {
			recs[i].when = last
		}
	}
	slices.SortStableFunc(recs, func(a, b item) int {
		switch {
		case a.when < b.when:
			return -1
		case a.when > b.when:
			return 1
		}
		return 0
	})
	seen := make(map[string]struct{}, len(recs))
	w := len(recs)
	for r := len(recs) - 1; r >= 0; r-- {
		if _, dup := seen[recs[r].cmd]; dup {
			continue
		}
		seen[recs[r].cmd] = struct{}{}
		w--
		recs[w] = recs[r]
	}
	return recs[w:]
}

// mergeImport merges the entries of imp that aren't in old into old, in
// order of time, and returns the result and how many it added.
func mergeImport(old, imp []item) ([]item, int) {
	have := make(map[string]struct{}, len(old))
	for i := range old {
		have[old[i].cmd] = struct{}{}
	}
	var add []item
	for _, it := range imp {
		if _, dup := have[it.cmd]; !dup {
			add = append(add, it)
		}
	}
	if len(add) == 0 {
		return old, 0
	}
	out := make([]item, 0, len(old)+len(add))
	i, j := 0, 0
	for i < len(old) && j < len(add) {
		if add[j].when <= old[i].when {
			out = append(out, add[j])
			j++
		} else {
			out = append(out, old[i])
			i++
		}
	}
	out = append(out, old[i:]...)
	out = append(out, add[j:]...)
	return out, len(add)
}

// nextLine splits off the first line of data.
func nextLine(data string) (line, rest string) {
	if i := strings.IndexByte(data, '\n'); i >= 0 {
		return data[:i], data[i+1:]
	}
	return data, ""
}

// parseFish parses fish's history file, a subset of YAML in which an entry
// is a "- cmd: <command>" line followed by indented "when: <unix seconds>"
// and "paths:" lines (and the list of paths). Commands have backslashes
// escaped as \\ and newlines as \n.
func parseFish(data string) []item {
	var recs []item
	cur := -1
	for data != "" {
		var line string
		line, data = nextLine(data)
		switch {
		case strings.HasPrefix(line, "- cmd:"):
			cur = -1
			if strings.HasPrefix(line, "- cmd:    when:") {
				continue // debris from fish 1.x rewriting a fish 2 file
			}
			v := strings.TrimLeft(line[len("- cmd:"):], " \t")
			for strings.HasPrefix(v, "- cmd: ") {
				v = v[len("- cmd: "):] // likewise
			}
			recs = append(recs, item{cmd: unescapeFish(v)})
			cur = len(recs) - 1
		case strings.HasPrefix(line, " "):
			if v, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "when:"); ok && cur >= 0 {
				if when, ok := parseInt(strings.TrimSpace(v)); ok {
					recs[cur].when = when
				}
			}
		default:
			cur = -1
		}
	}
	return recs
}

// unescapeFish undoes fish's escaping of history commands: \\ is a
// backslash, \n a newline, and other backslashes stand for themselves.
func unescapeFish(s string) string {
	i := strings.IndexByte(s, '\\')
	if i < 0 {
		return s
	}
	b := make([]byte, 0, len(s))
	for i >= 0 && i+1 < len(s) {
		b = append(b, s[:i]...)
		switch s[i+1] {
		case '\\':
			b = append(b, '\\')
			s = s[i+2:]
		case 'n':
			b = append(b, '\n')
			s = s[i+2:]
		default:
			b = append(b, '\\')
			s = s[i+1:]
		}
		i = strings.IndexByte(s, '\\')
	}
	return string(append(b, s...))
}

// parseZsh parses a zsh history file, with or without EXTENDED_HISTORY
// (": <start>:<elapsed seconds>;<command>"). A line ending in a backslash
// continues on the next one, and the file is metafied.
func parseZsh(data string) []item {
	var recs []item
	for data != "" {
		var line string
		line, data = nextLine(data)
		if strings.HasSuffix(line, `\`) && data != "" {
			var b strings.Builder
			for strings.HasSuffix(line, `\`) && data != "" {
				b.WriteString(line[:len(line)-1])
				b.WriteByte('\n')
				line, data = nextLine(data)
			}
			b.WriteString(line)
			line = b.String()
		}
		// zsh saves a command that ends in a backslash with a space after
		// it, so that it doesn't read as a continuation.
		if t := strings.TrimRight(line, " "); len(t) < len(line) && strings.HasSuffix(t, `\`) {
			line = line[:len(line)-1]
		}
		var it item
		if when, elapsed, cmd, ok := zshExtended(line); ok {
			it.when, it.dur, line = when, max(elapsed, 0)*1000, cmd
		} else if strings.HasPrefix(line, `\:`) {
			line = line[1:] // zsh escapes a leading colon in plain files
		}
		it.cmd = unmetafy(line)
		recs = append(recs, it)
	}
	return recs
}

// zshExtended parses a line in the EXTENDED_HISTORY format.
func zshExtended(line string) (when, elapsed int64, cmd string, ok bool) {
	rest, ok := strings.CutPrefix(line, ": ")
	if !ok {
		return 0, 0, "", false
	}
	start, rest, ok1 := strings.Cut(strings.TrimLeft(rest, " "), ":")
	dur, cmd, ok2 := strings.Cut(rest, ";")
	if !ok1 || !ok2 {
		return 0, 0, "", false
	}
	when, ok1 = parseInt(start)
	elapsed, ok2 = parseInt(dur)
	return when, elapsed, cmd, ok1 && ok2
}

// unmetafy undoes zsh's metafication: the byte 0x83 means that the next one
// has been xored with 32.
func unmetafy(s string) string {
	const meta = 0x83
	i := strings.IndexByte(s, meta)
	if i < 0 {
		return s
	}
	b := make([]byte, 0, len(s))
	for ; i >= 0 && i+1 < len(s); i = strings.IndexByte(s, meta) {
		b = append(b, s[:i]...)
		b = append(b, s[i+1]^32)
		s = s[i+2:]
	}
	return string(append(b, s...))
}

// parseBash parses a bash history file. With HISTTIMEFORMAT set, bash
// writes a "#<seconds>" line before each command; a file that starts with
// one is taken to have been written that way, and then a line not preceded
// by a time belongs to the command before it (bash's lithist).
func parseBash(data string) []item {
	var recs []item
	multi := len(data) > 1 && data[0] == '#' && isDigit(data[1])
	var when int64
	timed := false
	for data != "" {
		var line string
		line, data = nextLine(data)
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == "":
		case line[0] == '#' && len(line) > 1 && isDigit(line[1]):
			n := 1
			for n < len(line) && isDigit(line[n]) {
				n++
			}
			when, _ = parseInt(line[1:min(n, 19)])
			timed = true
		case multi && !timed && len(recs) > 0:
			recs[len(recs)-1].cmd += "\n" + line
		default:
			it := item{cmd: line}
			if timed {
				it.when, timed = when, false
			}
			recs = append(recs, it)
		}
	}
	return recs
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }
