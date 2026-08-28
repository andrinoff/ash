package history

import (
	"strconv"
	"strings"
)

// header is the first line of a history file.
const header = "# ash history v1\n"

// appendRecord appends the file line for it to b.
func appendRecord(b []byte, it *item) []byte {
	b = strconv.AppendInt(b, it.when, 10)
	b = append(b, '\t')
	b = strconv.AppendInt(b, int64(it.status), 10)
	b = append(b, '\t')
	b = strconv.AppendInt(b, it.dur, 10)
	b = append(b, '\t')
	b = appendEscaped(b, it.dir)
	b = append(b, '\t')
	b = appendEscaped(b, it.cmd)
	return append(b, '\n')
}

// appendEscaped appends s to b with backslashes, tabs and newlines escaped.
func appendEscaped(b []byte, s string) []byte {
	for {
		i := strings.IndexAny(s, "\\\t\n")
		if i < 0 {
			return append(b, s...)
		}
		b = append(b, s[:i]...)
		switch s[i] {
		case '\\':
			b = append(b, '\\', '\\')
		case '\t':
			b = append(b, '\\', 't')
		case '\n':
			b = append(b, '\\', 'n')
		}
		s = s[i+1:]
	}
}

// unescape undoes appendEscaped. A backslash before any other character is
// kept as it is.
func unescape(s string) string {
	i := strings.IndexByte(s, '\\')
	if i < 0 {
		return s
	}
	b := make([]byte, 0, len(s))
	for i >= 0 {
		b = append(b, s[:i]...)
		s = s[i:]
		c := byte('\\')
		if len(s) > 1 {
			switch s[1] {
			case '\\':
				c = '\\'
				s = s[1:]
			case 't':
				c = '\t'
				s = s[1:]
			case 'n':
				c = '\n'
				s = s[1:]
			}
		}
		b = append(b, c)
		s = s[1:]
		i = strings.IndexByte(s, '\\')
	}
	return string(append(b, s...))
}

// parseRecords parses the entries of a history file, oldest first, skipping
// lines that don't parse. It also returns how many bytes of data it
// consumed: everything up to the last newline, as a final line without one
// may still be being written.
func parseRecords(data string) ([]item, int) {
	end := strings.LastIndexByte(data, '\n') + 1
	data = data[:end]
	recs := make([]item, 0, strings.Count(data, "\n"))
	for len(data) > 0 {
		i := strings.IndexByte(data, '\n')
		if it, ok := parseLine(data[:i]); ok {
			recs = append(recs, it)
		}
		data = data[i+1:]
	}
	return recs, end
}

// parseLine parses one line of a history file (without its newline):
// unix-seconds, status, duration-ms, dir and cmd, separated by tabs.
func parseLine(line string) (item, bool) {
	if line == "" || line[0] == '#' {
		return item{}, false
	}
	var f [4]string
	rest := line
	for k := range f {
		i := strings.IndexByte(rest, '\t')
		if i < 0 {
			return item{}, false
		}
		f[k], rest = rest[:i], rest[i+1:]
	}
	if rest == "" || strings.IndexByte(rest, '\t') >= 0 {
		return item{}, false
	}
	when, ok1 := parseInt(f[0])
	status, ok2 := parseInt(f[1])
	dur, ok3 := parseInt(f[2])
	if !ok1 || !ok2 || !ok3 || dur < 0 || status != int64(int32(status)) {
		return item{}, false
	}
	return item{
		cmd:    unescape(rest),
		dir:    unescape(f[3]),
		when:   when,
		dur:    dur,
		status: int32(status),
	}, true
}

// parseInt parses an optionally negative decimal number of up to 18
// digits.
func parseInt(s string) (int64, bool) {
	neg := s != "" && s[0] == '-'
	if neg {
		s = s[1:]
	}
	if s == "" || len(s) > 18 {
		return 0, false
	}
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i] - '0'
		if c > 9 {
			return 0, false
		}
		n = n*10 + int64(c)
	}
	if neg {
		n = -n
	}
	return n, true
}

// trimCommand removes the whitespace at the end of a command line unless it
// is escaped with a backslash, as fish does before adding a command to its
// history.
func trimCommand(s string) string {
	for len(s) > 0 {
		switch s[len(s)-1] {
		case ' ', '\t', '\n', '\r':
		default:
			return s
		}
		n := 0
		for i := len(s) - 2; i >= 0 && s[i] == '\\'; i-- {
			n++
		}
		if n%2 == 1 {
			return s
		}
		s = s[:len(s)-1]
	}
	return s
}
