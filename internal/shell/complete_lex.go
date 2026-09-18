package shell

import (
	"strings"
	"unicode/utf8"
)

// This file holds the tolerant scanner used by tab completion. It reads
// the command line up to the cursor and works out which command the
// cursor is in, the words before it, and the state of the token being
// completed (quotes, escapes, a variable name...). Unlike the parser it
// never fails: unfinished quotes and substitutions are the normal case.

// compWord is a word of the command line being completed.
type compWord struct {
	start, end int    // byte range of the word's source in the buffer
	value      string // text with quotes and escapes removed; expansions kept as typed
	expand     bool   // has an unquoted expansion ($var, ~, (cmd), `cmd`)
	glob       bool   // has an unquoted wildcard (* ? [)

	// Offsets (in the buffer) of the first expansion or wildcard and the
	// end of the last one, -1 if there is none.
	expFirst, expEnd int
	globLast         int // offset of the last unquoted wildcard, -1 if none

	// Where the word can be split for file completion: just past the last
	// '/' (slash*) and the last '=' or ':' (sep*). pos is a buffer offset,
	// val the length of value at that point and quote the quote open
	// there. pos is -1 when there is no such character.
	slashPos, slashVal int
	slashQuote         byte
	sepPos, sepVal     int
	sepQuote           byte
}

func newCompWord(start int) compWord {
	return compWord{start: start, end: start, expFirst: -1, expEnd: -1, globLast: -1, slashPos: -1, sepPos: -1}
}

// compLine is a command line scanned for completion.
type compLine struct {
	buf    string
	cursor int

	words []compWord // words of the current command before the token
	tok   compWord   // the token being completed (ends at the cursor)

	quote  byte // quote open at the cursor: 0, '\'', '"' or '$' for $'...'
	escape bool // the token ends with an unfinished backslash

	varPos   int  // >= 0: the cursor is in a variable name that starts here
	varBrace bool // ... written as ${name

	redirect bool // the token is the target of a redirection
	skip     bool // nothing to complete: comment, fd number, heredoc word
}

// raw returns the source text of the token.
func (l *compLine) raw() string { return l.buf[l.tok.start:l.tok.end] }

// commandIndex returns the index in l.words of the command name, or -1
// when the token is itself in command position. Leading assignments,
// decorations (command, builtin, exec) and keywords that start a command
// (if, while, and, not, ...) are skipped.
func (l *compLine) commandIndex() int {
	for i, w := range l.words {
		raw := l.buf[w.start:w.end]
		if isAssignWord(raw) {
			continue
		}
		if !w.expand && !w.glob && raw == w.value && isCommandPrefix(w.value) {
			switch w.value {
			case "command", "builtin", "exec":
				// "command -v x": the decoration is the command.
				next := ""
				if i+1 < len(l.words) {
					next = l.words[i+1].value
				} else {
					next = l.tok.value
				}
				if strings.HasPrefix(next, "-") {
					return i
				}
			}
			continue
		}
		return i
	}
	return -1
}

// isCommandPrefix reports whether a word is followed by a command name.
func isCommandPrefix(s string) bool {
	switch s {
	case "if", "then", "else", "elif", "while", "until", "do", "!", "time",
		"and", "or", "not", "begin", "{", "command", "builtin", "exec":
		return true
	}
	return false
}

// isAssignWord reports whether raw looks like NAME=value, NAME+=value or
// NAME[index]=value.
func isAssignWord(raw string) bool {
	if raw == "" || !isNameStartByte(raw[0]) {
		return false
	}
	i := 1
	for i < len(raw) && isNameByte(raw[i]) {
		i++
	}
	if i < len(raw) && raw[i] == '[' {
		j := strings.IndexByte(raw[i:], ']')
		if j < 0 {
			return false
		}
		i += j + 1
	}
	if i < len(raw) && raw[i] == '+' {
		i++
	}
	return i < len(raw) && raw[i] == '='
}

// scanCompLine scans buf up to cursor.
func scanCompLine(buf string, cursor int) *compLine {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(buf) {
		cursor = len(buf)
	}
	lx := &compLexer{s: buf[:cursor], out: &compLine{buf: buf, cursor: cursor, varPos: -1}}
	lx.command(0)
	return lx.out
}

type compLexer struct {
	s   string // the buffer up to the cursor
	i   int
	out *compLine
}

// compFrame is the state of one command list (the top level or the
// inside of a substitution).
type compFrame struct {
	words    []compWord
	redirect bool // the next word is a redirection target
	dup      bool // ... of >& or <&: a file descriptor, usually
	skipNext bool // the next word is not completable (heredoc delimiter)
}

type wordState struct {
	quote    byte
	escape   bool
	varPos   int
	varBrace bool
	nested   bool // the cursor was reached in a substitution (out is filled)
	skip     bool // the cursor is somewhere we can't complete
}

func (lx *compLexer) peek(off int) byte {
	if lx.i+off < len(lx.s) {
		return lx.s[lx.i+off]
	}
	return 0
}

// command scans commands until the byte close (')' or '`', 0 at the top
// level). It reports whether the cursor was reached, in which case lx.out
// describes the innermost command around it.
func (lx *compLexer) command(close byte) bool {
	var f compFrame
	for {
		lx.blanks()
		if lx.i >= len(lx.s) {
			lx.finish(&f, newCompWord(lx.i), wordState{varPos: -1})
			return true
		}
		c := lx.s[lx.i]
		if close != 0 && c == close {
			lx.i++
			return false
		}
		switch c {
		case '\n', ';', '|', '&', ')':
			if c == '&' && lx.peek(1) == '>' {
				// &> and &>> redirect both outputs
				lx.i += 2
				if lx.peek(0) == '>' {
					lx.i++
				}
				f.redirect = true
				continue
			}
			lx.i++
			f = compFrame{}
			continue
		case '<', '>':
			if lx.peek(1) != '(' {
				lx.redirOp(&f)
				continue
			}
		case '#':
			if j := strings.IndexByte(lx.s[lx.i:], '\n'); j >= 0 {
				lx.i += j
				continue
			}
			lx.out.tok = newCompWord(lx.i)
			lx.out.tok.end = len(lx.s)
			lx.out.skip = true
			return true
		}
		w, st, eof := lx.word()
		if eof {
			if !st.nested {
				lx.finish(&f, w, st)
			}
			return true
		}
		if lx.i < len(lx.s) && (lx.s[lx.i] == '<' || lx.s[lx.i] == '>') && lx.peek(1) != '(' && isAllDigits(lx.s[w.start:w.end]) {
			// 2>file: the digits are part of the redirection
			lx.redirOp(&f)
			continue
		}
		if f.redirect || f.skipNext {
			f.redirect, f.dup, f.skipNext = false, false, false
			continue
		}
		f.words = append(f.words, w)
	}
}

// blanks skips spaces, tabs and line continuations.
func (lx *compLexer) blanks() {
	for lx.i < len(lx.s) {
		switch lx.s[lx.i] {
		case ' ', '\t', '\r':
			lx.i++
		case '\\':
			if lx.peek(1) != '\n' {
				return
			}
			lx.i += 2
		default:
			return
		}
	}
}

// redirOp consumes a redirection operator.
func (lx *compLexer) redirOp(f *compFrame) {
	c := lx.s[lx.i]
	lx.i++
	n := lx.peek(0)
	switch {
	case c == '<' && n == '<':
		lx.i++
		if lx.peek(0) == '<' {
			// here-string: the word is data
			lx.i++
			f.skipNext = true
			return
		}
		if lx.peek(0) == '-' {
			lx.i++
		}
		f.skipNext = true // heredoc delimiter
		return
	case n == '&':
		lx.i++
		f.redirect, f.dup = true, true
		return
	case c == '>' && (n == '>' || n == '|' || n == '?'):
		lx.i++
	case c == '<' && n == '>':
		lx.i++
	}
	f.redirect = true
}

// finish records the state at the cursor.
func (lx *compLexer) finish(f *compFrame, w compWord, st wordState) {
	o := lx.out
	o.words = f.words
	o.tok = w
	o.quote, o.escape = st.quote, st.escape
	o.varPos, o.varBrace = st.varPos, st.varBrace
	o.redirect = f.redirect
	o.skip = st.skip || f.skipNext
	if f.dup {
		// >&2: a file descriptor, unless a file name is being typed
		if v := w.value; v == "" || v == "-" || isAllDigits(v) {
			o.skip = true
		}
	}
}

// markExp records an expansion between start and end.
func (w *compWord) markExp(start, end int) {
	if w.expFirst < 0 {
		w.expFirst = start
	}
	if end > w.expEnd {
		w.expEnd = end
	}
}

// noteSeps records path and assignment separators in a run of literal
// text that ends at buffer offset end (exclusive) and whose value ends at
// vlen.
func (w *compWord) noteSeps(text string, end, vlen int, quote byte) {
	if k := strings.LastIndexByte(text, '/'); k >= 0 {
		back := len(text) - k - 1
		w.slashPos, w.slashVal, w.slashQuote = end-back, vlen-back, quote
	}
	if k := strings.LastIndexAny(text, "=:"); k >= 0 {
		back := len(text) - k - 1
		w.sepPos, w.sepVal, w.sepQuote = end-back, vlen-back, quote
	}
}

// word scans one word. It returns eof when the cursor is inside it or
// right after it.
func (lx *compLexer) word() (compWord, wordState, bool) {
	w := newCompWord(lx.i)
	st := wordState{varPos: -1}
	var val strings.Builder
	done := func(eof bool) (compWord, wordState, bool) {
		w.end = lx.i
		w.value = val.String()
		return w, st, eof
	}
	for lx.i < len(lx.s) {
		c := lx.s[lx.i]
		switch c {
		case ' ', '\t', '\r', '\n', ';', '&', '|', ')':
			return done(false)
		case '<', '>':
			if lx.i != w.start || lx.peek(1) != '(' {
				return done(false)
			}
			// process substitution <(...) or >(...)
			start := lx.i
			w.expand = true
			lx.i += 2
			if lx.command(')') {
				st.nested = true
				return done(true)
			}
			w.markExp(start, lx.i)
			val.WriteString(lx.s[start:lx.i])
		case '(':
			start := lx.i
			w.expand = true
			lx.i++
			if lx.command(')') {
				st.nested = true
				return done(true)
			}
			w.markExp(start, lx.i)
			val.WriteString(lx.s[start:lx.i])
		case '`':
			start := lx.i
			w.expand = true
			lx.i++
			if lx.command('`') {
				st.nested = true
				return done(true)
			}
			w.markExp(start, lx.i)
			val.WriteString(lx.s[start:lx.i])
		case '\\':
			if lx.i+1 >= len(lx.s) {
				st.escape = true
				lx.i++
				return done(true)
			}
			if lx.s[lx.i+1] == '\n' {
				lx.i += 2
				continue
			}
			v, n := unescapeOutsideComp(lx.s[lx.i:])
			val.WriteString(v)
			lx.i += n
			w.noteSeps(v, lx.i, val.Len(), 0)
		case '\'':
			rest := lx.s[lx.i+1:]
			j := strings.IndexByte(rest, '\'')
			if j < 0 {
				val.WriteString(rest)
				lx.i = len(lx.s)
				w.noteSeps(rest, lx.i, val.Len(), '\'')
				st.quote = '\''
				return done(true)
			}
			val.WriteString(rest[:j])
			lx.i += j + 1
			w.noteSeps(rest[:j], lx.i, val.Len(), '\'')
			lx.i++
		case '"':
			if lx.dquote(&val, &w, &st) {
				return done(true)
			}
		case '$':
			if lx.dollar(&val, &w, &st, false) {
				return done(true)
			}
		case '~':
			if lx.i == w.start {
				w.expand = true
				w.markExp(lx.i, lx.i+1)
			}
			val.WriteByte(c)
			lx.i++
		case '*', '?', '[':
			w.glob = true
			w.globLast = lx.i
			w.markExp(lx.i, lx.i+1)
			val.WriteByte(c)
			lx.i++
		case '/', '=', ':':
			val.WriteByte(c)
			lx.i++
			w.noteSeps(lx.s[lx.i-1:lx.i], lx.i, val.Len(), 0)
		default:
			val.WriteByte(c)
			lx.i++
		}
	}
	return done(true)
}

// dquote scans a double-quoted string. It reports whether the cursor was
// reached inside it.
func (lx *compLexer) dquote(val *strings.Builder, w *compWord, st *wordState) bool {
	lx.i++
	for lx.i < len(lx.s) {
		c := lx.s[lx.i]
		switch c {
		case '"':
			lx.i++
			return false
		case '\\':
			if lx.i+1 >= len(lx.s) {
				st.escape = true
				st.quote = '"'
				lx.i++
				return true
			}
			switch n := lx.s[lx.i+1]; n {
			case '$', '`', '"', '\\':
				val.WriteByte(n)
				lx.i += 2
			case '\n':
				lx.i += 2
			default:
				val.WriteByte('\\')
				lx.i++
			}
		case '$':
			if lx.dollar(val, w, st, true) {
				if !st.nested {
					st.quote = '"'
				}
				return true
			}
		case '`':
			start := lx.i
			w.expand = true
			lx.i++
			if lx.command('`') {
				st.nested = true
				return true
			}
			w.markExp(start, lx.i)
			val.WriteString(lx.s[start:lx.i])
		default:
			val.WriteByte(c)
			lx.i++
			if c == '/' || c == '=' || c == ':' {
				w.noteSeps(lx.s[lx.i-1:lx.i], lx.i, val.Len(), '"')
			}
		}
	}
	st.quote = '"'
	return true
}

// dollar scans an expansion starting with '$'. Expansions are kept in
// the value as typed, including a variable name the cursor is in. It
// reports whether the cursor was reached.
func (lx *compLexer) dollar(val *strings.Builder, w *compWord, st *wordState, inDQ bool) bool {
	start := lx.i
	j := lx.i + 1
	if j >= len(lx.s) {
		// a lone '$' before the cursor: complete variable names
		w.expand = true
		w.markExp(start, j)
		val.WriteByte('$')
		st.varPos = j
		lx.i = j
		return true
	}
	c := lx.s[j]
	switch {
	case c == '(':
		w.expand = true
		if j+1 < len(lx.s) && lx.s[j+1] == '(' {
			// $(( arithmetic )): skip it
			lx.i = j + 2
			depth := 0
			for lx.i < len(lx.s) {
				switch lx.s[lx.i] {
				case '(':
					depth++
				case ')':
					if depth == 0 && lx.peek(1) == ')' {
						lx.i += 2
						val.WriteString(lx.s[start:lx.i])
						w.markExp(start, lx.i)
						return false
					}
					if depth > 0 {
						depth--
					}
				}
				lx.i++
			}
			st.skip = true
			return true
		}
		lx.i = j + 1
		if lx.command(')') {
			st.nested = true
			return true
		}
		w.markExp(start, lx.i)
		val.WriteString(lx.s[start:lx.i])
		return false
	case c == '{':
		w.expand = true
		k := j + 1
		if k < len(lx.s) && (lx.s[k] == '#' || lx.s[k] == '!') {
			k++
		}
		n := k
		for n < len(lx.s) && isNameByte(lx.s[n]) {
			n++
		}
		if n >= len(lx.s) {
			val.WriteString(lx.s[start:n])
			st.varPos, st.varBrace = k, true
			lx.i = n
			return true
		}
		depth := 0
		for i := n; i < len(lx.s); i++ {
			switch lx.s[i] {
			case '{':
				depth++
			case '}':
				if depth > 0 {
					depth--
					continue
				}
				lx.i = i + 1
				val.WriteString(lx.s[start:lx.i])
				w.markExp(start, lx.i)
				return false
			}
		}
		lx.i = len(lx.s)
		st.skip = true
		return true
	case c == '\'' && !inDQ:
		// $'...' with C escapes
		k := j + 1
		for k < len(lx.s) && lx.s[k] != '\'' {
			if lx.s[k] == '\\' {
				k++
			}
			k++
		}
		if k >= len(lx.s) {
			if strings.HasSuffix(lx.s[j+1:], "\\") && !strings.HasSuffix(lx.s[j+1:], "\\\\") {
				st.escape = true
			}
			body := lx.s[j+1:]
			val.WriteString(decodeANSIC(body))
			lx.i = len(lx.s)
			if !strings.Contains(body, "\\") {
				w.noteSeps(body, lx.i, val.Len(), '$')
			}
			st.quote = '$'
			return true
		}
		body := lx.s[j+1 : k]
		val.WriteString(decodeANSIC(body))
		lx.i = k + 1
		if !strings.Contains(body, "\\") {
			w.noteSeps(body, k, val.Len(), '$')
		}
		return false
	case c == '"' && !inDQ:
		// $"..." is "..."
		lx.i = j
		return lx.dquote(val, w, st)
	case isNameStartByte(c):
		w.expand = true
		n := j
		for n < len(lx.s) && isNameByte(lx.s[n]) {
			n++
		}
		if n >= len(lx.s) {
			w.markExp(start, n)
			val.WriteString(lx.s[start:n])
			st.varPos = j
			lx.i = n
			return true
		}
		val.WriteString(lx.s[start:n])
		lx.i = n
		w.markExp(start, n)
		return false
	case isDigitByte(c) || strings.IndexByte("?#@*!$-", c) >= 0:
		w.expand = true
		val.WriteString(lx.s[start : j+1])
		lx.i = j + 1
		w.markExp(start, lx.i)
		return false
	}
	val.WriteByte('$')
	lx.i++
	return false
}

// unescapeOutsideComp is the parser's handling of a backslash outside
// quotes: C escapes are understood, anything else stands for itself.
func unescapeOutsideComp(s string) (string, int) {
	if len(s) < 2 {
		return "", len(s)
	}
	switch s[1] {
	case 'n':
		return "\n", 2
	case 't':
		return "\t", 2
	case 'r':
		return "\r", 2
	case 'e':
		return "\x1b", 2
	case 'x', 'X':
		v, n := hexPrefix(s[2:], 2)
		if n == 0 {
			return string(s[1]), 2
		}
		return string([]byte{byte(v)}), 2 + n
	case 'u':
		v, n := hexPrefix(s[2:], 4)
		if n == 0 {
			return "u", 2
		}
		return string(rune(v)), 2 + n
	case 'U':
		v, n := hexPrefix(s[2:], 8)
		if n == 0 {
			return "U", 2
		}
		return string(rune(v)), 2 + n
	case 'c':
		if len(s) > 2 && s[2] >= '@' && s[2] <= '~' {
			return string([]byte{s[2] & 0x1f}), 3
		}
		if len(s) > 2 && s[2] == '?' {
			return "\x7f", 3
		}
		return "c", 2
	}
	_, size := utf8.DecodeRuneInString(s[1:])
	return s[1 : 1+size], 1 + size
}

func hexPrefix(s string, max int) (int, int) {
	v, n := 0, 0
	for n < len(s) && n < max && isHex(s[n]) {
		v = v*16 + hexVal(s[n])
		n++
	}
	return v, n
}

// decodeANSIC decodes the body of $'...'.
func decodeANSIC(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			if c != '\\' {
				b.WriteByte(c)
			}
			continue
		}
		i++
		switch n := s[i]; n {
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
		case 'x':
			v, k := hexPrefix(s[i+1:], 2)
			if k == 0 {
				b.WriteString("\\x")
				continue
			}
			b.WriteByte(byte(v))
			i += k
		case 'u', 'U':
			max := 4
			if n == 'U' {
				max = 8
			}
			v, k := hexPrefix(s[i+1:], max)
			if k == 0 {
				b.WriteByte('\\')
				b.WriteByte(n)
				continue
			}
			b.WriteRune(rune(v))
			i += k
		default:
			b.WriteByte(n)
		}
	}
	return b.String()
}
