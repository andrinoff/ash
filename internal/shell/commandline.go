package shell

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/andrinoff/ash/internal/syntax"
)

func bCommandline(sh *Shell, io *IO, args []string) int {
	ed := sh.Editor
	a := args[1:]
	var (
		mode                   = "replace"
		scope                  = "all"
		cut, tokenize, current bool
		cursor, funcs          bool
		isValid, paging        bool
		search                 bool
		values                 []string
	)
	for i := 0; i < len(a); i++ {
		o := a[i]
		if o == "--" {
			values = append(values, a[i+1:]...)
			break
		}
		if !strings.HasPrefix(o, "-") || len(o) < 2 {
			values = append(values, o)
			continue
		}
		if strings.HasPrefix(o, "--") {
			switch o {
			case "--append":
				mode = "append"
			case "--insert":
				mode = "insert"
			case "--replace":
				mode = "replace"
			case "--current-job":
				scope = "job"
			case "--current-process":
				scope = "process"
			case "--current-token":
				scope = "token"
			case "--current-buffer":
				scope = "all"
			case "--cut-at-cursor":
				cut = true
			case "--tokens-expanded", "--tokens-raw", "--tokenize":
				tokenize = true
			case "--cursor":
				cursor = true
			case "--function":
				funcs = true
			case "--is-valid":
				isValid = true
			case "--paging-mode", "--paging-full-mode", "--search-mode":
				paging = true
				search = o == "--search-mode"
			case "--input", "--selection", "--line", "--current-selection":
			default:
			}
			continue
		}
		for _, c := range o[1:] {
			switch c {
			case 'a':
				mode = "append"
			case 'i':
				mode = "insert"
			case 'r':
				mode = "replace"
			case 'j':
				scope = "job"
			case 'p':
				scope = "process"
			case 't':
				scope = "token"
			case 'b':
				scope = "all"
			case 'c':
				cut = true
			case 'o', 'x':
				tokenize = true
			case 'C':
				cursor = true
			case 'f':
				funcs = true
			case 'P', 'S':
				paging = true
			case 'L', 's', 'I':
			}
		}
	}
	_ = current
	if ed == nil {
		if isValid || paging || search {
			return 1
		}
		// Not interactive: behave as an empty command line.
		if len(values) == 0 && !funcs {
			if cursor {
				fmt.Fprintln(io.Out, 0)
			}
			return 1
		}
		return 1
	}
	if funcs {
		ed.QueueFunctions(values...)
		return 0
	}
	if paging {
		if ed.PagerActive() {
			return 0
		}
		return 1
	}
	buf, pos := ed.Buffer()
	if isValid {
		if strings.TrimSpace(buf) == "" {
			return 1
		}
		if _, err := syntax.Parse(buf); err != nil {
			if syntax.IsIncomplete(err) {
				return 2
			}
			return 1
		}
		return 0
	}
	if cursor {
		if len(values) == 0 {
			fmt.Fprintln(io.Out, len([]rune(buf[:pos])))
			return 0
		}
		n, err := strconv.Atoi(values[0])
		if err != nil {
			return 2
		}
		rs := []rune(buf)
		if n < 0 {
			n = 0
		}
		if n > len(rs) {
			n = len(rs)
		}
		ed.SetBuffer(buf, len(string(rs[:n])))
		return 0
	}
	start, end := scopeRange(buf, pos, scope)
	if len(values) == 0 {
		text := buf[start:end]
		if cut {
			if pos < start {
				text = ""
			} else if pos < end {
				text = buf[start:pos]
			}
		}
		if tokenize {
			for _, t := range CommandTokens(text) {
				fmt.Fprintln(io.Out, t)
			}
			return 0
		}
		fmt.Fprintln(io.Out, text)
		return 0
	}
	v := strings.Join(values, "\n")
	switch mode {
	case "replace":
		nb := buf[:start] + v + buf[end:]
		npos := start + len(v)
		if scope == "all" {
			npos = len(v)
		}
		ed.SetBuffer(nb, npos)
	case "append":
		nb := buf[:end] + v + buf[end:]
		ed.SetBuffer(nb, pos)
	case "insert":
		nb := buf[:pos] + v + buf[pos:]
		ed.SetBuffer(nb, pos+len(v))
	}
	return 0
}

// scopeRange returns the byte range of the job/process/token at pos.
func scopeRange(buf string, pos int, scope string) (int, int) {
	switch scope {
	case "token":
		s := pos
		for s > 0 && !isTokenSep(buf[s-1]) {
			s--
		}
		e := pos
		for e < len(buf) && !isTokenSep(buf[e]) {
			e++
		}
		return s, e
	case "process", "job":
		seps := "|;&\n"
		if scope == "job" {
			seps = ";&\n"
		}
		s := pos
		for s > 0 && !strings.ContainsRune(seps, rune(buf[s-1])) {
			s--
		}
		e := pos
		for e < len(buf) && !strings.ContainsRune(seps, rune(buf[e])) {
			e++
		}
		for s < e && (buf[s] == ' ' || buf[s] == '\t') {
			s++
		}
		return s, e
	}
	return 0, len(buf)
}

func isTokenSep(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '|' || c == ';' || c == '&' || c == '<' || c == '>' || c == '(' || c == ')'
}

// CommandTokens splits a command line into unquoted words, the way
// `commandline -o` does. It is tolerant of unfinished input.
func CommandTokens(s string) []string {
	var out []string
	var cur strings.Builder
	have := false
	q := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q == '\'':
			if c == '\'' {
				q = 0
			} else {
				cur.WriteByte(c)
			}
		case q == '"':
			if c == '"' {
				q = 0
			} else if c == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			q = c
			have = true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			have = true
		case c == ' ' || c == '\t' || c == '\n':
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		case c == '|' || c == ';' || c == '&':
			// an operator ends a word but isn't one; like fish, the
			// words of every process in the text are listed
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

func bBind(sh *Shell, io *IO, args []string) int {
	ed := sh.Editor
	a := args[1:]
	mode := "default"
	setsMode := ""
	erase, list := false, false
	for len(a) > 0 && strings.HasPrefix(a[0], "-") && len(a[0]) > 1 && !looksLikeKey(a[0]) {
		o := a[0]
		a = a[1:]
		switch o {
		case "-M", "--mode":
			if len(a) > 0 {
				mode = a[0]
				a = a[1:]
			}
		case "-m", "--sets-mode":
			if len(a) > 0 {
				setsMode = a[0]
				a = a[1:]
			}
		case "-e", "--erase":
			erase = true
		case "-a", "--all", "--preset", "--user", "-s", "--silent", "-k", "--key":
			if o == "-a" || o == "--all" {
				list = true
			}
		case "-L", "--list-modes":
			fmt.Fprintln(io.Out, "default\ninsert\nvisual\nreplace")
			return 0
		case "-f", "--function-names":
			for _, f := range EditorFunctions {
				fmt.Fprintln(io.Out, f)
			}
			return 0
		case "--key-names":
			return 0
		case "--":
			goto done
		default:
			if strings.HasPrefix(o, "--mode=") {
				mode = strings.TrimPrefix(o, "--mode=")
				continue
			}
			if strings.HasPrefix(o, "--sets-mode=") {
				setsMode = strings.TrimPrefix(o, "--sets-mode=")
				continue
			}
		}
	}
done:
	if ed == nil {
		return 0
	}
	if len(a) == 0 || list {
		for _, b := range ed.Bindings() {
			fmt.Fprintln(io.Out, b)
		}
		return 0
	}
	if erase {
		for _, k := range a {
			ed.Unbind(mode, k)
		}
		return 0
	}
	if len(a) == 1 {
		for _, b := range ed.Bindings() {
			if strings.Contains(b, " "+a[0]+" ") || strings.HasSuffix(b, " "+a[0]) {
				fmt.Fprintln(io.Out, b)
			}
		}
		return 0
	}
	if err := ed.Bind(mode, a[0], strings.Join(a[1:], " "), setsMode); err != nil {
		fmt.Fprintf(io.Err, "bind: %v\n", err)
		return 1
	}
	return 0
}

func looksLikeKey(s string) bool {
	return strings.HasPrefix(s, "-\\") || s == "-"
}

// EditorFunctions lists named editor functions (for bind -f and
// completion); the editor fills it in.
var EditorFunctions []string
