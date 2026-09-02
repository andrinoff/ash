package shell

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// This file implements fish's `string` builtin, following fish 4.8:
// option parsing (GNU style, options may follow operands until "--"),
// output, error messages and exit statuses.
//
// Known differences from fish:
//   - Regular expressions run on Go's RE2 engine. PCRE2 syntax that has a
//     cheap RE2 equivalent is translated (\e \h \v \R \N \cX \o{} \G,
//     (?'name'...), (?>...), (?#...), possessive quantifiers, {,n});
//     backreferences and lookaround are reported as compile errors, and
//     -i folds case for all of Unicode rather than ASCII only.
//   - Strings are read from standard input only when it was redirected
//     (pipe, file, here-string); the shell's own stdin is never consumed,
//     as in fish. Unlike fish, operands are simply preferred over a
//     redirected stdin instead of being an error, because a function
//     called in a pipeline cannot be told apart from a direct redirection.
//   - Command substitution splits output on newlines, so the elements
//     `string collect` and `string split0` produce are not kept intact.

func init() {
	builtins["string"] = bString
	builtinHelp["string"] = "manipulate strings"
}

var sbSynopses = [][2]string{
	{"collect", "string collect [-a | --allow-empty] [-N | --no-trim-newlines] [STRING ...]"},
	{"escape", "string escape [-n | --no-quoted] [--style=script|var|url|regex] [STRING ...]"},
	{"join", "string join [-q | --quiet] [-n | --no-empty] SEP [STRING ...]"},
	{"join0", "string join0 [-q | --quiet] [-n | --no-empty] [STRING ...]"},
	{"length", "string length [-q | --quiet] [-V | --visible] [STRING ...]"},
	{"lower", "string lower [-q | --quiet] [STRING ...]"},
	{"match", "string match [-a | --all] [-e | --entire] [-i | --ignore-case] [-g | --groups-only] [-r | --regex] [-n | --index] [-q | --quiet] [-v | --invert] [(-m | --max-matches) MAX] PATTERN [STRING ...]"},
	{"pad", "string pad [-r | --right] [-C | --center] [(-c | --char) CHAR] [(-w | --width) INTEGER] [STRING ...]"},
	{"repeat", "string repeat [(-n | --count) COUNT] [(-m | --max) MAX] [-N | --no-newline] [-q | --quiet] [STRING ...]"},
	{"replace", "string replace [-a | --all] [-f | --filter] [-i | --ignore-case] [-r | --regex] [(-m | --max-matches) MAX] [-q | --quiet] PATTERN REPLACEMENT [STRING ...]"},
	{"shorten", "string shorten [(-c | --char) CHARS] [(-m | --max) INTEGER] [-N | --no-newline] [-l | --left] [-q | --quiet] [STRING ...]"},
	{"split", "string split [(-f | --fields) FIELDS] [-a | --allow-empty] [(-m | --max) MAX] [-n | --no-empty] [-q | --quiet] [-r | --right] SEP [STRING ...]"},
	{"split0", "string split0 [(-f | --fields) FIELDS] [-a | --allow-empty] [(-m | --max) MAX] [-n | --no-empty] [-q | --quiet] [-r | --right] [STRING ...]"},
	{"sub", "string sub [(-s | --start) START] [(-e | --end) END] [(-l | --length) LENGTH] [-q | --quiet] [STRING ...]"},
	{"trim", "string trim [-l | --left] [-r | --right] [(-c | --chars) CHARS] [-q | --quiet] [STRING ...]"},
	{"unescape", "string unescape [--style=script|var|url] [STRING ...]"},
	{"upper", "string upper [-q | --quiet] [STRING ...]"},
}

func bString(sh *Shell, io *IO, args []string) int {
	if len(args) < 2 {
		io.Err.WriteString("string: missing subcommand\n")
		return 2
	}
	sub := args[1]
	if sub == "-h" || sub == "--help" {
		var b strings.Builder
		b.WriteString("Usage:\n")
		for _, s := range sbSynopses {
			b.WriteString("  " + s[1] + "\n")
		}
		io.Out.WriteString(b.String())
		return 0
	}
	run := sbSubcommand(sub)
	if run == nil {
		fmt.Fprintf(io.Err, "string %s: invalid subcommand\n", sub)
		return 2
	}
	rest := args[2:]
	if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help") {
		for _, s := range sbSynopses {
			if s[0] == sub {
				io.Out.WriteString("Usage: " + s[1] + "\n")
			}
		}
		return 0
	}
	c := &sbCtx{sh: sh, io: io, sub: sub, out: bufio.NewWriter(io.Out)}
	status := run(c, rest)
	c.out.Flush()
	return status
}

func sbSubcommand(sub string) func(*sbCtx, []string) int {
	switch sub {
	case "collect":
		return sbCollect
	case "escape":
		return sbEscape
	case "join", "join0":
		return sbJoin
	case "length":
		return sbLength
	case "lower", "upper":
		return sbCase
	case "match":
		return sbMatch
	case "pad":
		return sbPad
	case "repeat":
		return sbRepeat
	case "replace":
		return sbReplace
	case "shorten":
		return sbShorten
	case "split", "split0":
		return sbSplit
	case "sub":
		return sbSub
	case "trim":
		return sbTrim
	case "unescape":
		return sbUnescape
	}
	return nil
}

// sbCtx is the state shared by the subcommands.
type sbCtx struct {
	sh  *Shell
	io  *IO
	sub string
	out *bufio.Writer
}

// fail prints "string SUB: message" to stderr and returns status 2.
func (c *sbCtx) fail(format string, a ...any) int {
	c.out.Flush()
	fmt.Fprintf(c.io.Err, "string %s: %s\n", c.sub, fmt.Sprintf(format, a...))
	return 2
}

// --- option parsing ---------------------------------------------------

type sbOptDef struct {
	short rune // 0 if the option only has a long form
	long  string
	arg   bool
}

func (d *sbOptDef) key() string {
	if d.short != 0 {
		return string(d.short)
	}
	return d.long
}

type sbOpt struct {
	key string // the short letter, or the long name for long-only options
	val string
}

// getopt parses args like fish's wgetopt: options may be mixed with
// operands until "--", short options can be clustered, long options can
// be abbreviated and take their argument as "--opt=x" or "--opt x".
// Errors are reported with the argument as written.
func (c *sbCtx) getopt(args []string, defs []sbOptDef) ([]sbOpt, []string, bool) {
	var opts []sbOpt
	var operands []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			operands = append(operands, a)
			continue
		}
		if a[1] == '-' {
			name, val, hasVal := strings.Cut(a[2:], "=")
			d := sbLongOpt(defs, name)
			switch {
			case d == nil:
				c.fail("%s: unknown option", a)
				return nil, nil, false
			case !d.arg && hasVal:
				c.fail("%s: option does not take an argument", a)
				return nil, nil, false
			case d.arg && !hasVal:
				if i+1 >= len(args) {
					c.fail("%s: option requires an argument", a)
					return nil, nil, false
				}
				i++
				val = args[i]
			}
			opts = append(opts, sbOpt{d.key(), val})
			continue
		}
		for j := 1; j < len(a); {
			r, size := utf8.DecodeRuneInString(a[j:])
			j += size
			d := sbShortOpt(defs, r)
			if d == nil {
				c.fail("%s: unknown option", a)
				return nil, nil, false
			}
			if !d.arg {
				opts = append(opts, sbOpt{d.key(), ""})
				continue
			}
			val := a[j:]
			if val == "" {
				if i+1 >= len(args) {
					c.fail("%s: option requires an argument", a)
					return nil, nil, false
				}
				i++
				val = args[i]
			}
			opts = append(opts, sbOpt{d.key(), val})
			break
		}
	}
	return opts, operands, true
}

func sbLongOpt(defs []sbOptDef, name string) *sbOptDef {
	var found *sbOptDef
	n := 0
	for k := range defs {
		d := &defs[k]
		if d.long == name {
			return d
		}
		if name != "" && strings.HasPrefix(d.long, name) {
			found = d
			n++
		}
	}
	if n == 1 {
		return found
	}
	return nil
}

func sbShortOpt(defs []sbOptDef, r rune) *sbOptDef {
	for k := range defs {
		if defs[k].short == r {
			return &defs[k]
		}
	}
	return nil
}

// sbInt parses an integer like fish_wcstol: surrounding whitespace and a
// sign are allowed, the rest must be decimal digits that fit in bits.
func sbInt(s string, bits int) (int64, bool) {
	t := strings.TrimFunc(s, unicode.IsSpace)
	if t == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(t, 10, bits)
	return v, err == nil
}

// count parses a non-negative option value, reporting errors as fish does.
func (c *sbCtx) count(val, what string) (int64, bool) {
	v, ok := sbInt(val, 64)
	if !ok {
		c.fail("%s: invalid integer", val)
		return 0, false
	}
	if v < 0 {
		c.fail("Invalid %s value '%s'", what, val)
		return 0, false
	}
	return v, true
}

// maxMatches parses the argument of match/replace --max-matches.
func (c *sbCtx) maxMatches(val string) (int64, bool) {
	t := strings.TrimPrefix(strings.TrimFunc(val, unicode.IsSpace), "+")
	v, err := strconv.ParseUint(t, 10, 64)
	if err != nil || v == 0 {
		c.fail("Invalid max matches value '%s'", val)
		return 0, false
	}
	if v > math.MaxInt64 {
		v = math.MaxInt64
	}
	return int64(v), true
}

// --- input ------------------------------------------------------------

// sbIter yields the strings a subcommand works on: its operands, or the
// lines of a redirected stdin when there are none. The second result says
// whether the item was terminated by a newline (operands always are).
type sbIter struct {
	args  []string
	r     *bufio.Reader
	whole bool // read stdin as a single item (split0, collect)
	done  bool
}

func (c *sbCtx) items(operands []string, whole bool) *sbIter {
	it := &sbIter{args: operands, whole: whole}
	if len(operands) == 0 {
		it.r = c.stdin()
	}
	return it
}

// stdin returns a reader for standard input if it was redirected for this
// command. Like fish, a terminal or the shell's own inherited stdin is
// never read, so `string join , $empty` cannot swallow a script's input.
func (c *sbCtx) stdin() *bufio.Reader {
	in := c.io.In
	if in == nil || in.closed || in.TTY {
		return nil
	}
	if in.File != nil {
		if in.File == c.sh.Stdin {
			return nil
		}
	} else if in.R == nil {
		return nil
	}
	return bufio.NewReaderSize(in, 64*1024)
}

func (it *sbIter) next() (string, bool, bool) {
	if it.r == nil {
		if len(it.args) == 0 {
			return "", false, false
		}
		s := it.args[0]
		it.args = it.args[1:]
		return s, true, true
	}
	if it.done {
		return "", false, false
	}
	if it.whole {
		it.done = true
		b, _ := io.ReadAll(it.r)
		if len(b) == 0 {
			return "", false, false
		}
		return string(b), false, true
	}
	line, err := it.r.ReadString('\n')
	if err != nil {
		it.done = true
		if line == "" {
			return "", false, false
		}
		return line, false, true
	}
	return line[:len(line)-1], true, true
}

// sbCharOffsets returns the byte offset of every character of s followed by
// len(s). Invalid UTF-8 bytes count as one character each, as in fish.
func sbCharOffsets(s string) []int {
	offs := make([]int, 0, len(s)+1)
	for i := 0; i < len(s); {
		offs = append(offs, i)
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return append(offs, len(s))
}

// --- collect ----------------------------------------------------------

func sbCollect(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{{'a', "allow-empty", false}, {'N', "no-trim-newlines", false}})
	if !ok {
		return 2
	}
	allowEmpty, noTrim := false, false
	for _, o := range opts {
		switch o.key {
		case "a":
			allowEmpty = true
		case "N":
			noTrim = true
		}
	}
	appended := 0
	it := c.items(ops, true)
	for s, nl, ok := it.next(); ok; s, nl, ok = it.next() {
		if !noTrim {
			s = strings.TrimRight(s, "\n")
		}
		c.out.WriteString(s)
		if nl {
			c.out.WriteByte('\n')
		}
		appended += len(s)
	}
	if allowEmpty && appended == 0 {
		c.out.WriteByte('\n')
	}
	if appended > 0 {
		return 0
	}
	return 1
}

// --- escape / unescape ------------------------------------------------

func sbEscape(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{{'n', "no-quoted", false}, {0, "style", true}})
	if !ok {
		return 2
	}
	noQuoted, style := false, "script"
	for _, o := range opts {
		switch o.key {
		case "n":
			noQuoted = true
		case "style":
			switch o.val {
			case "script", "var", "url", "regex":
				style = o.val
			default:
				return c.fail("Invalid escape style '%s'", o.val)
			}
		}
	}
	escaped := false
	it := c.items(ops, false)
	for s, nl, ok := it.next(); ok; s, nl, ok = it.next() {
		switch style {
		case "script":
			s = sbEscapeScript(s, noQuoted)
		case "var":
			s = sbEscapeVar(s)
		case "url":
			s = sbEscapeURL(s)
		case "regex":
			s = sbEscapeRegex(s)
		}
		c.out.WriteString(s)
		if nl {
			c.out.WriteByte('\n')
		}
		escaped = true
	}
	if escaped {
		return 0
	}
	return 1
}

func sbUnescape(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{{'n', "no-quoted", false}, {0, "style", true}})
	if !ok {
		return 2
	}
	style := "script"
	for _, o := range opts {
		if o.key == "style" {
			switch o.val {
			case "script", "var", "url":
				style = o.val
			default:
				return c.fail("Invalid style value '%s'", o.val)
			}
		}
	}
	n := 0
	it := c.items(ops, false)
	for s, nl, ok := it.next(); ok; s, nl, ok = it.next() {
		var res string
		var good bool
		switch style {
		case "script":
			res, good = sbUnescapeScript(s)
		case "var":
			res, good = sbUnescapeVar(s)
		case "url":
			res, good = sbUnescapeURL(s)
		}
		if !good {
			continue
		}
		c.out.WriteString(res)
		if nl {
			c.out.WriteByte('\n')
		}
		n++
	}
	if n > 0 {
		return 0
	}
	return 1
}

// sbEscapeScript quotes s so fish (and ash) read it back as one word.
func sbEscapeScript(s string, noQuoted bool) string {
	if s == "" && !noQuoted {
		return "''"
	}
	var b strings.Builder
	needEscape, complexEscape := false, false
	singleQ, doubleQ, dollars := 0, 0, 0
	prevAlnum := false // a '#' right after a letter or digit cannot start a comment
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&b, "\\X%02x", s[i])
			needEscape, complexEscape = true, true
			prevAlnum = false
			i++
			continue
		}
		hashSpecial := !prevAlnum
		prevAlnum = unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Other_Alphabetic, r)
		i += size
		switch {
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == 0x1b:
			b.WriteString(`\e`)
		case r == 0x7f:
			b.WriteString(`\x7f`)
		case r == 0:
			b.WriteString(`\x00`)
		case r < 0x1a:
			// fish writes 0x1a and 0x1c-0x1f unchanged
			b.WriteString(`\c`)
			b.WriteRune('a' + r - 1)
		case r == '\\' || r == '\'':
			if r == '\'' {
				singleQ++
			}
			needEscape = true
			b.WriteByte('\\')
			b.WriteRune(r)
			continue
		case strings.ContainsRune("&$ <>()[]{}*|;\"%~", r) || r == '#' && hashSpecial:
			switch r {
			case '"':
				doubleQ++
			case '$':
				dollars++
			}
			needEscape = true
			b.WriteByte('\\')
			b.WriteRune(r)
			continue
		default:
			b.WriteRune(r)
			continue
		}
		needEscape, complexEscape = true, true
	}
	if noQuoted || !needEscape || complexEscape {
		return b.String()
	}
	// Quoting is easier to read when nothing needs a backslash escape.
	q := byte('\'')
	if singleQ > doubleQ+dollars {
		q = '"'
	}
	b.Reset()
	b.WriteByte(q)
	for _, r := range s {
		if q == '\'' && (r == '\'' || r == '\\') || q == '"' && (r == '"' || r == '$' || r == '\\') {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte(q)
	return b.String()
}

func sbIsUpperHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'A' && c <= 'F' }

func sbIsAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// sbEscapeVar hex-encodes everything but ASCII alphanumerics so the result
// is a valid variable name.
func sbEscapeVar(s string) string {
	var b strings.Builder
	prevHex := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case sbIsAlnum(c) && (!prevHex || !sbIsUpperHex(c)):
			if prevHex {
				b.WriteByte('_')
				prevHex = false
			}
			b.WriteByte(c)
		case c == '_':
			b.WriteString("__")
			prevHex = false
		default:
			fmt.Fprintf(&b, "_%02X", c)
			prevHex = true
		}
	}
	if prevHex {
		b.WriteByte('_')
	}
	return b.String()
}

func sbEscapeURL(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if sbIsAlnum(c) || strings.IndexByte("/.~-_", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func sbEscapeRegex(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(`.^$*+()?[{}\|-]`, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// sbUnescapeScript undoes sbEscapeScript: quotes are removed and backslash
// escapes outside quotes are interpreted. Invalid escapes fail.
func sbUnescapeScript(s string) (string, bool) {
	var b strings.Builder
	const (
		unquoted = iota
		single
		double
	)
	mode := unquoted
	for i := 0; i < len(s); {
		c := s[i]
		switch mode {
		case single:
			switch {
			case c == '\\' && i+1 < len(s) && (s[i+1] == '\'' || s[i+1] == '\\'):
				b.WriteByte(s[i+1])
				i += 2
			case c == '\'':
				mode = unquoted
				i++
			default:
				b.WriteByte(c)
				i++
			}
		case double:
			switch {
			case c == '\\' && i+1 < len(s) && strings.IndexByte("\"$\\", s[i+1]) >= 0:
				b.WriteByte(s[i+1])
				i += 2
			case c == '\\' && i+1 < len(s) && s[i+1] == '\n':
				i += 2
			case c == '"':
				mode = unquoted
				i++
			default:
				b.WriteByte(c)
				i++
			}
		default:
			switch c {
			case '\'':
				mode = single
				i++
			case '"':
				mode = double
				i++
			case '\\':
				n, ok := sbUnescapeBackslash(&b, s, i)
				if !ok {
					return "", false
				}
				i = n
			default:
				b.WriteByte(c)
				i++
			}
		}
	}
	return b.String(), true
}

// sbUnescapeBackslash decodes the escape at s[i] (a backslash) into b and
// returns the index after it.
func sbUnescapeBackslash(b *strings.Builder, s string, i int) (int, bool) {
	if i+1 >= len(s) {
		return 0, false
	}
	e := s[i+1]
	j := i + 2
	hex := func(max int) (int64, int) {
		var v int64
		n := 0
		for n < max && j+n < len(s) && isHex(s[j+n]) {
			v = v*16 + int64(hexVal(s[j+n]))
			n++
		}
		return v, n
	}
	switch e {
	case 'a':
		b.WriteByte(7)
	case 'b':
		b.WriteByte(8)
	case 'e':
		b.WriteByte(0x1b)
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
	case '\n':
	case 'x', 'X':
		v, n := hex(2)
		if n == 0 {
			return 0, false
		}
		b.WriteByte(byte(v))
		j += n
	case 'u', 'U':
		max := 4
		if e == 'U' {
			max = 8
		}
		v, n := hex(max)
		if n == 0 || v > unicode.MaxRune {
			return 0, false
		}
		b.WriteRune(rune(v))
		j += n
	case 'c':
		if j >= len(s) {
			return 0, false
		}
		x := s[j]
		switch {
		case x >= 'a' && x <= 'a'+32:
			b.WriteRune(rune(x-'a') + 1)
		case x >= 'A' && x <= 'A'+32:
			b.WriteRune(rune(x-'A') + 1)
		default:
			return 0, false
		}
		j++
	case '0', '1', '2', '3', '4', '5', '6', '7':
		v := 0
		j = i + 1
		for n := 0; n < 3 && j < len(s) && s[j] >= '0' && s[j] <= '7'; n++ {
			v = v*8 + int(s[j]-'0')
			j++
		}
		if v > 0x7f {
			return 0, false
		}
		b.WriteByte(byte(v))
	default:
		_, size := utf8.DecodeRuneInString(s[i+1:])
		b.WriteString(s[i+1 : i+1+size])
		j = i + 1 + size
	}
	return j, true
}

// sbUnescapeVar decodes the output of sbEscapeVar.
func sbUnescapeVar(s string) (string, bool) {
	var b strings.Builder
	prevHex := false
	for i := 0; i < len(s); {
		c := s[i]
		if c > 0x7f {
			return "", false
		}
		if c != '_' {
			b.WriteByte(c)
			prevHex = false
			i++
			continue
		}
		switch {
		case i+1 == len(s):
			// a final underscore only ends a hex escape
			if !prevHex {
				return "", false
			}
			i++
		case s[i+1] == '_':
			b.WriteByte('_')
			prevHex = false
			i += 2
		case sbIsUpperHex(s[i+1]):
			if i+2 >= len(s) || !isHex(s[i+2]) {
				return "", false
			}
			b.WriteByte(byte(hexVal(s[i+1])<<4 | hexVal(s[i+2])))
			prevHex = true
			i += 3
		default:
			// separator after a hex escape
			prevHex = false
			i++
		}
	}
	return b.String(), true
}

func sbUnescapeURL(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c > 0x7f {
			return "", false
		}
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		switch {
		case i+1 < len(s) && s[i+1] == '%':
			b.WriteByte('%')
			i++
		case i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b.WriteByte(byte(hexVal(s[i+1])<<4 | hexVal(s[i+2])))
			i += 2
		default:
			return "", false
		}
	}
	return b.String(), true
}

// --- join / join0 -----------------------------------------------------

func sbJoin(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{{'q', "quiet", false}, {'n', "no-empty", false}})
	if !ok {
		return 2
	}
	quiet, noEmpty := false, false
	for _, o := range opts {
		switch o.key {
		case "q":
			quiet = true
		case "n":
			noEmpty = true
		}
	}
	join0 := c.sub == "join0"
	sep := "\x00"
	if !join0 {
		if len(ops) == 0 {
			return c.fail("missing argument")
		}
		sep, ops = ops[0], ops[1:]
	}
	n := 0
	trailingNL := true
	it := c.items(ops, false)
	for s, nl, ok := it.next(); ok; s, nl, ok = it.next() {
		if !quiet {
			if noEmpty && s == "" {
				continue
			}
			if n > 0 {
				c.out.WriteString(sep)
			}
			c.out.WriteString(s)
		} else if n > 1 {
			return 0
		}
		n++
		trailingNL = nl
	}
	if n > 0 && !quiet {
		if join0 {
			c.out.WriteByte(0)
		} else if trailingNL {
			c.out.WriteByte('\n')
		}
	}
	if n > 1 {
		return 0
	}
	return 1
}

// --- length -----------------------------------------------------------

func sbLength(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{{'q', "quiet", false}, {'V', "visible", false}})
	if !ok {
		return 2
	}
	quiet, visible := false, false
	for _, o := range opts {
		switch o.key {
		case "q":
			quiet = true
		case "V":
			visible = true
		}
	}
	nonEmpty := 0
	report := func(n int) bool {
		if n > 0 {
			nonEmpty++
		}
		if !quiet {
			c.out.WriteString(strconv.Itoa(n))
			c.out.WriteByte('\n')
			return false
		}
		return nonEmpty > 0
	}
	it := c.items(ops, false)
	for s, _, ok := it.next(); ok; s, _, ok = it.next() {
		if !visible {
			if report(utf8.RuneCountInString(s)) {
				return 0
			}
			continue
		}
		// Width is measured per line; a carriage return goes back to the
		// start of the line, so the widest stretch between them counts.
		for _, line := range strings.Split(s, "\n") {
			max := 0
			for _, seg := range strings.Split(line, "\r") {
				if w := sbWidth(seg); w > max {
					max = w
				}
			}
			if report(max) {
				return 0
			}
		}
	}
	if nonEmpty > 0 {
		return 0
	}
	return 1
}

// --- lower / upper ----------------------------------------------------

func sbCase(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{{'q', "quiet", false}})
	if !ok {
		return 2
	}
	quiet := len(opts) > 0
	upper := c.sub == "upper"
	n := 0
	it := c.items(ops, false)
	for s, nl, ok := it.next(); ok; s, nl, ok = it.next() {
		t := sbMapCase(s, upper)
		if t != s {
			n++
		}
		if quiet {
			if n > 0 {
				return 0
			}
			continue
		}
		c.out.WriteString(t)
		if nl {
			c.out.WriteByte('\n')
		}
	}
	if n > 0 {
		return 0
	}
	return 1
}

// sbMapCase converts case character by character using the full Unicode
// mappings (ß becomes SS), as fish does. Invalid bytes are kept.
func sbMapCase(s string, upper bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteByte(s[i])
			i++
			continue
		}
		i += size
		switch {
		case upper:
			if sp, ok := sbUpperSpecial[r]; ok {
				b.WriteString(sp)
			} else {
				b.WriteRune(unicode.ToUpper(r))
			}
		case r == 0x130:
			b.WriteString("i\u0307") // İ lowercases to i + combining dot
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// sbLowerFold lowercases each character one to one, for case-insensitive
// glob matching and literal replacement.
func sbLowerFold(s string) string {
	return strings.Map(unicode.ToLower, s)
}

// --- match ------------------------------------------------------------

func sbMatch(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{
		{'a', "all", false}, {'e', "entire", false}, {'g', "groups-only", false},
		{'i', "ignore-case", false}, {'v', "invert", false}, {'q', "quiet", false},
		{'r', "regex", false}, {'n', "index", false}, {'m', "max-matches", true},
	})
	if !ok {
		return 2
	}
	var all, entire, groups, icase, invert, quiet, regex, index bool
	var max int64
	for _, o := range opts {
		switch o.key {
		case "a":
			all = true
		case "e":
			entire = true
		case "g":
			groups = true
		case "i":
			icase = true
		case "v":
			invert = true
		case "q":
			quiet = true
		case "r":
			regex = true
		case "n":
			index = true
		case "m":
			if max, ok = c.maxMatches(o.val); !ok {
				return 2
			}
		}
	}
	if len(ops) == 0 {
		return c.fail("missing argument")
	}
	pattern, ops := ops[0], ops[1:]
	switch {
	case entire && index:
		return c.fail("invalid option combination, --entire and --index are mutually exclusive")
	case invert && groups:
		return c.fail("invalid option combination, --invert and --groups-only are mutually exclusive")
	case entire && groups:
		return c.fail("invalid option combination, --entire and --groups-only are mutually exclusive")
	}

	total := int64(0)
	done := func() bool { return quiet && total > 0 || max > 0 && total >= max }
	it := c.items(ops, false)

	if !regex {
		g := sbCompileGlob(pattern, icase, entire)
		for s, _, ok := it.next(); ok && !done(); s, _, ok = it.next() {
			subject := s
			if icase {
				subject = sbLowerFold(s)
			}
			if g.match(subject) == invert {
				continue
			}
			total++
			if quiet {
				continue
			}
			if index {
				fmt.Fprintf(c.out, "1 %d\n", utf8.RuneCountInString(s))
			} else {
				c.out.WriteString(s)
				c.out.WriteByte('\n')
			}
		}
		if total > 0 {
			return 0
		}
		return 1
	}

	rx, err := sbCompileRegex(pattern, icase)
	if err != nil {
		return c.regexError(pattern, err)
	}
	names := rx.re.SubexpNames()
	for _, name := range names {
		if name == "" {
			continue
		}
		if v, _ := c.sh.lookupVar(name); isReadOnlySpecial(name) || v != nil && v.ReadOnly {
			c.out.Flush()
			fmt.Fprintf(c.io.Err, "Modification of read-only variable \"%s\" is not allowed\n", name)
			return 2
		}
	}
	captures := make([][]string, len(names))
	populate := func(s string, m []int) {
		for gi, name := range names {
			switch {
			case name == "":
			case m != nil && m[2*gi] >= 0:
				captures[gi] = append(captures[gi], s[m[2*gi]:m[2*gi+1]])
			case all:
				captures[gi] = append(captures[gi], "")
			}
		}
	}
	report := func(s string, m []int, rc *sbRuneCounter) {
		if quiet {
			return
		}
		if entire {
			c.out.WriteString(s)
			c.out.WriteByte('\n')
		}
		first := 0
		if entire || groups {
			first = 1
		}
		for gi := first; gi < len(m)/2; gi++ {
			start, end := m[2*gi], m[2*gi+1]
			if start < 0 {
				continue
			}
			if index {
				pos := rc.at(start)
				fmt.Fprintf(c.out, "%d %d\n", pos+1, utf8.RuneCountInString(s[start:end]))
			} else {
				c.out.WriteString(s[start:end])
				c.out.WriteByte('\n')
			}
		}
	}
	for s, _, ok := it.next(); ok && !done(); s, _, ok = it.next() {
		var ms [][]int
		if all && !invert {
			ms = rx.re.FindAllStringSubmatchIndex(s, -1)
		} else if m := rx.re.FindStringSubmatchIndex(s); m != nil {
			ms = [][]int{m}
		}
		if len(ms) == 0 {
			if !invert {
				continue
			}
			total++
			if total == 1 {
				populate(s, nil)
			}
			if !quiet {
				if index {
					fmt.Fprintf(c.out, "1 %d\n", utf8.RuneCountInString(s))
				} else {
					c.out.WriteString(s)
					c.out.WriteByte('\n')
				}
			}
			continue
		}
		if invert {
			continue
		}
		total++
		rc := &sbRuneCounter{s: s}
		for _, m := range ms {
			report(s, m, rc)
			if total == 1 {
				populate(s, m)
			}
		}
	}
	// Named groups become variables, set even when nothing matched.
	for gi, name := range names {
		if name != "" {
			vals := captures[gi]
			if vals == nil {
				vals = []string{}
			}
			c.sh.SetVar(name, vals, setOpts{scope: setAuto})
		}
	}
	if total > 0 {
		return 0
	}
	return 1
}

// sbRuneCounter converts increasing byte offsets of s to character offsets.
type sbRuneCounter struct {
	s      string
	off, n int
}

func (rc *sbRuneCounter) at(b int) int {
	if b < rc.off {
		rc.off, rc.n = 0, 0
	}
	rc.n += utf8.RuneCountInString(rc.s[rc.off:b])
	rc.off = b
	return rc.n
}

// sbGlob is a string match pattern: literal segments separated by "*".
// fish 4.8 enables qmark-noglob, so "?" and brackets are literal; "\*"
// and "\\" are the only escapes.
type sbGlob struct {
	segs []string // len(segs)-1 stars sit between the segments
}

func sbCompileGlob(p string, icase, entire bool) sbGlob {
	var segs []string
	var cur strings.Builder
	for i := 0; i < len(p); i++ {
		switch {
		case p[i] == '*':
			segs = append(segs, cur.String())
			cur.Reset()
		case p[i] == '\\' && i+1 < len(p) && (p[i+1] == '*' || p[i+1] == '\\'):
			cur.WriteByte(p[i+1])
			i++
		default:
			cur.WriteByte(p[i])
		}
	}
	segs = append(segs, cur.String())
	if icase {
		for i := range segs {
			segs[i] = sbLowerFold(segs[i])
		}
	}
	if entire {
		if segs[0] != "" || len(segs) == 1 {
			segs = append([]string{""}, segs...)
		}
		if segs[len(segs)-1] != "" || len(segs) == 1 {
			segs = append(segs, "")
		}
	}
	return sbGlob{segs: segs}
}

func (g sbGlob) match(s string) bool {
	segs := g.segs
	if len(segs) == 1 {
		return s == segs[0]
	}
	first, last := segs[0], segs[len(segs)-1]
	if len(s) < len(first)+len(last) || !strings.HasPrefix(s, first) || !strings.HasSuffix(s, last) {
		return false
	}
	s = s[len(first) : len(s)-len(last)]
	for _, seg := range segs[1 : len(segs)-1] {
		i := strings.Index(s, seg)
		if i < 0 {
			return false
		}
		s = s[i+len(seg):]
	}
	return true
}

// --- pad --------------------------------------------------------------

func sbPad(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{
		{'c', "chars", true}, {'c', "char", true}, {'r', "right", false},
		{'C', "center", false}, {'w', "width", true},
	})
	if !ok {
		return 2
	}
	padChar, padWidth := " ", 1
	right, center := false, false
	var width int64
	for _, o := range opts {
		switch o.key {
		case "c":
			r, size := utf8.DecodeRuneInString(o.val)
			if o.val == "" || size != len(o.val) {
				return c.fail("Padding should be a character '%s'", o.val)
			}
			w := 1
			if r != utf8.RuneError || size != 1 {
				w = sbRuneWidth(r)
			}
			if w <= 0 {
				return c.fail("Invalid padding character of width zero '%s'", o.val)
			}
			padChar, padWidth = o.val, w
		case "r":
			right = true
		case "C":
			center = true
		case "w":
			if width, ok = c.count(o.val, "width"); !ok {
				return 2
			}
		}
	}
	type item struct {
		s string
		w int
	}
	var inputs []item
	maxWidth := 0
	trailingNL := true
	it := c.items(ops, false)
	for s, nl, ok := it.next(); ok; s, nl, ok = it.next() {
		w := sbWidth(s)
		if w > maxWidth {
			maxWidth = w
		}
		inputs = append(inputs, item{s, w})
		trailingNL = nl
	}
	target := int64(maxWidth)
	if width > target {
		target = width
	}
	// Padding is made of whole pad characters; leftover columns are
	// filled with spaces next to the text.
	fill := func(n int64, spacesFirst bool) {
		chars, spaces := n/int64(padWidth), n%int64(padWidth)
		if spacesFirst {
			c.out.WriteString(strings.Repeat(" ", int(spaces)))
		}
		for i := int64(0); i < chars; i++ {
			c.out.WriteString(padChar)
		}
		if !spacesFirst {
			c.out.WriteString(strings.Repeat(" ", int(spaces)))
		}
	}
	for _, in := range inputs {
		pad := target - int64(in.w)
		switch {
		case center:
			left := (pad + 1) / 2
			if right {
				left = pad / 2
			}
			fill(left, false)
			c.out.WriteString(in.s)
			fill(pad-left, true)
		case right:
			c.out.WriteString(in.s)
			fill(pad, true)
		default:
			fill(pad, false)
			c.out.WriteString(in.s)
		}
		if trailingNL {
			c.out.WriteByte('\n')
		}
	}
	return 0
}

// --- repeat -----------------------------------------------------------

func sbRepeat(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{
		{'n', "count", true}, {'m', "max", true}, {'q', "quiet", false}, {'N', "no-newline", false},
	})
	if !ok {
		return 2
	}
	var count, max int64
	countSet, maxSet, quiet, noNewline := false, false, false, false
	for _, o := range opts {
		switch o.key {
		case "n":
			if count, ok = c.count(o.val, "count"); !ok {
				return 2
			}
			countSet = true
		case "m":
			if max, ok = c.count(o.val, "max"); !ok {
				return 2
			}
			maxSet = true
		case "q":
			quiet = true
		case "N":
			noNewline = true
		}
	}
	if !countSet && !maxSet {
		if len(ops) == 0 {
			return c.fail("missing argument")
		}
		v, ok := sbInt(ops[0], 64)
		if !ok || v < 0 {
			return c.fail("Invalid count value '%s'", ops[0])
		}
		count, ops = v, ops[1:]
	}
	if count == 0 && max == 0 {
		return 1
	}
	allEmpty, first, trailingNL := true, true, true
	it := c.items(ops, false)
	for s, nl, ok := it.next(); ok; s, nl, ok = it.next() {
		trailingNL = nl
		if s == "" {
			continue
		}
		allEmpty = false
		if quiet {
			return 0
		}
		if !first {
			c.out.WriteByte('\n')
		}
		first = false
		offs := sbCharOffsets(s)
		n := int64(len(offs) - 1)
		// Output stops at max characters, or after count copies if that
		// is shorter.
		limit := max
		if max == 0 || count > 0 && count <= (max-1)/n {
			limit = math.MaxInt64
			if count <= math.MaxInt64/n {
				limit = count * n
			}
		}
		for i := limit / n; i > 0; i-- {
			if _, err := c.out.WriteString(s); err != nil {
				return 1
			}
		}
		c.out.WriteString(s[:offs[limit%n]])
	}
	if !quiet && !noNewline && !allEmpty && trailingNL {
		c.out.WriteByte('\n')
	}
	if allEmpty {
		return 1
	}
	return 0
}

// --- replace ----------------------------------------------------------

func sbReplace(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{
		{'a', "all", false}, {'f', "filter", false}, {'i', "ignore-case", false},
		{'q', "quiet", false}, {'r', "regex", false}, {'m', "max-matches", true},
	})
	if !ok {
		return 2
	}
	var all, filter, icase, quiet, regex bool
	var max int64
	for _, o := range opts {
		switch o.key {
		case "a":
			all = true
		case "f":
			filter = true
		case "i":
			icase = true
		case "q":
			quiet = true
		case "r":
			regex = true
		case "m":
			if max, ok = c.maxMatches(o.val); !ok {
				return 2
			}
		}
	}
	switch len(ops) {
	case 0:
		return c.fail("missing argument")
	case 1:
		return c.fail("expected 2 arguments; got 1")
	}
	pattern, repl, ops := ops[0], ops[1], ops[2:]
	var rx *sbRegex
	if regex {
		var err error
		if rx, err = sbCompileRegex(pattern, icase); err != nil {
			return c.regexError(pattern, err)
		}
	}
	replaced := int64(0)
	it := c.items(ops, false)
	for s, nl, ok := it.next(); ok; s, nl, ok = it.next() {
		var did bool
		var res string
		if regex {
			var err error
			if did, res, err = rx.replace(s, repl, all); err != nil {
				return c.fail("Regular expression substitute error: %s", err)
			}
		} else {
			did, res = sbLiteralReplace(s, pattern, repl, all, icase)
		}
		if did {
			replaced++
		}
		if !quiet && (!filter || did) {
			c.out.WriteString(res)
			if nl {
				c.out.WriteByte('\n')
			}
		}
		if quiet && replaced > 0 {
			return 0
		}
		if max > 0 && replaced >= max {
			break
		}
	}
	if replaced > 0 {
		return 0
	}
	return 1
}

func sbLiteralReplace(s, pattern, repl string, all, icase bool) (bool, string) {
	if pattern == "" {
		return false, s
	}
	if !icase {
		if !strings.Contains(s, pattern) {
			return false, s
		}
		if all {
			return true, strings.ReplaceAll(s, pattern, repl)
		}
		return true, strings.Replace(s, pattern, repl, 1)
	}
	// Compare lowercased characters. Like fish, the text before a match
	// is copied lowercased; the text after the last one is not.
	sub := []rune(sbLowerFold(s))
	pat := []rune(sbLowerFold(pattern))
	offs := sbCharOffsets(s)
	var b strings.Builder
	prev, did := 0, false
	for i := 0; i+len(pat) <= len(sub); {
		if !sbRunesAt(sub, i, pat) {
			i++
			continue
		}
		b.WriteString(sbMapCase(s[offs[prev]:offs[i]], false))
		b.WriteString(repl)
		i += len(pat)
		prev, did = i, true
		if !all {
			break
		}
	}
	if !did {
		return false, s
	}
	b.WriteString(s[offs[prev]:])
	return true, b.String()
}

func sbRunesAt(s []rune, i int, p []rune) bool {
	for k, r := range p {
		if s[i+k] != r {
			return false
		}
	}
	return true
}

// --- shorten ----------------------------------------------------------

func sbShorten(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{
		{'c', "chars", true}, {'c', "char", true}, {'m', "max", true},
		{'N', "no-newline", false}, {'l', "left", false}, {'q', "quiet", false},
	})
	if !ok {
		return 2
	}
	ell := "\u2026" // …
	var max int64
	maxSet, noNewline, left, quiet := false, false, false, false
	for _, o := range opts {
		switch o.key {
		case "c":
			ell = o.val
		case "m":
			if max, ok = c.count(o.val, "max"); !ok {
				return 2
			}
			maxSet = true
		case "N":
			noNewline = true
		case "l":
			left = true
		case "q":
			quiet = true
		}
	}
	it := c.items(ops, false)
	if maxSet && max == 0 {
		// A max of 0 means no shortening at all.
		for s, _, ok := it.next(); ok; s, _, ok = it.next() {
			c.out.WriteString(s)
			c.out.WriteByte('\n')
		}
		return 0
	}
	// Width only makes sense per line: either each line is shortened on
	// its own, or with -N only the first (last with -l) line is kept.
	var lines []string
	minWidth := math.MaxInt
	add := func(l string) {
		if w := sbWidth(l); w > 0 && w < minWidth {
			minWidth = w
		}
		lines = append(lines, l)
	}
	for s, _, ok := it.next(); ok; s, _, ok = it.next() {
		split := strings.Split(s, "\n")
		if !noNewline {
			for _, l := range split {
				add(l)
			}
			continue
		}
		l := split[0]
		if left {
			l = split[len(split)-1]
		}
		add(l + ell)
	}
	ourMax := minWidth
	if maxSet {
		ourMax = int(min(max, math.MaxInt32))
	}
	ellWidth := sbWidth(ell)
	if ellWidth > ourMax {
		// Not even the ellipsis fits: just truncate.
		ell, ellWidth = "", 0
	}
	n := 0
	for _, l := range lines {
		var res string
		var short bool
		if left {
			res, short = sbShortenLeft(l, ourMax, ell, ellWidth)
		} else {
			res, short = sbShortenRight(l, ourMax, ell, ellWidth)
		}
		if short {
			n++
			if quiet {
				return 0
			}
		}
		if !quiet {
			c.out.WriteString(res)
			c.out.WriteByte('\n')
		}
	}
	if n > 0 {
		return 0
	}
	return 1
}

// sbShortenRight shortens line to limit columns keeping its start, the
// way fish 4.8 does. Escape sequences are kept whole and take no room.
func sbShortenRight(line string, limit int, ell string, ellWidth int) (string, bool) {
	skip := func(pos int) int {
		for pos < len(line) {
			n := sbEscapeLen(line[pos:])
			if n == 0 {
				break
			}
			pos += n
		}
		return pos
	}
	// Past the end a character has width 0 (like fish reading a NUL), so a
	// line that ends in an escape sequence comes out "shortened": fish
	// appends the ellipsis to it.
	at := func(pos int) (int, int) {
		if pos >= len(line) {
			return 0, 1
		}
		r, size := utf8.DecodeRuneInString(line[pos:])
		return sbVisibleWidth(r), size
	}
	width, pos := 0, 0
	for width <= limit && pos < len(line) {
		pos = skip(pos)
		w, size := at(pos)
		if w <= 0 || width+w+ellWidth <= limit {
			width = max(width+w, 0)
			pos += size
			continue
		}
		// This character only fits without the ellipsis: keep the whole
		// line if all of it fits.
		total, end := width+w, pos+size
		for end < len(line) {
			end = skip(end)
			w, size := at(end)
			total = max(total+w, 0)
			end += size
		}
		if total <= limit {
			pos = end
		}
		break
	}
	if pos == len(line) {
		return line, false
	}
	return line[:min(pos, len(line))] + ell, true
}

// sbShortenLeft keeps the longest suffix of line that fits in limit
// columns after the ellipsis.
func sbShortenLeft(line string, limit int, ell string, ellWidth int) (string, bool) {
	if sbWidth(line) <= limit {
		return line, false
	}
	if !strings.ContainsAny(line, "\x1b\b") {
		// Widths simply add up: walk back from the end.
		width, pos := 0, len(line)
		for pos > 0 {
			r, size := utf8.DecodeLastRuneInString(line[:pos])
			w := sbVisibleWidth(r)
			if width+w+ellWidth > limit {
				break
			}
			width += w
			pos -= size
		}
		return ell + line[pos:], true
	}
	for pos := 0; pos < len(line); {
		if sbWidth(line[pos:])+ellWidth <= limit {
			return ell + line[pos:], true
		}
		_, size := utf8.DecodeRuneInString(line[pos:])
		pos += size
	}
	return ell, true
}

// --- split / split0 ---------------------------------------------------

// sbFieldRange is an inclusive range of 0-based field indexes; first may
// be greater than last for descending ranges.
type sbFieldRange struct{ first, last int64 }

// parseFields parses the -f argument: comma-separated 1-based field
// numbers and ranges like 2-4 or 4-2.
func (c *sbCtx) parseFields(arg string) ([]sbFieldRange, bool) {
	var fields []sbFieldRange
	for _, part := range strings.Split(arg, ",") {
		if lo, hi, isRange := strings.Cut(part, "-"); isRange {
			a, ok1 := sbInt(lo, 32)
			b, ok2 := sbInt(hi, 32)
			if !ok1 || !ok2 {
				c.fail("%s: invalid integer", arg)
				return nil, false
			}
			if a <= 0 || b <= 0 {
				c.fail("Invalid range value for field '%s'", arg)
				return nil, false
			}
			fields = append(fields, sbFieldRange{a - 1, b - 1})
			continue
		}
		v, ok := sbInt(part, 32)
		if !ok {
			c.fail("%s: invalid integer", arg)
			return nil, false
		}
		if v <= 0 {
			c.fail("Invalid fields value '%s'", arg)
			return nil, false
		}
		fields = append(fields, sbFieldRange{v - 1, v - 1})
	}
	return fields, true
}

func sbSplit(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{
		{'q', "quiet", false}, {'r', "right", false}, {'m', "max", true},
		{'n', "no-empty", false}, {'f', "fields", true}, {'a', "allow-empty", false},
	})
	if !ok {
		return 2
	}
	var quiet, right, noEmpty, allowEmpty bool
	max := int64(math.MaxInt64)
	var fields []sbFieldRange
	for _, o := range opts {
		switch o.key {
		case "q":
			quiet = true
		case "r":
			right = true
		case "m":
			if max, ok = c.count(o.val, "max"); !ok {
				return 2
			}
		case "n":
			noEmpty = true
		case "f":
			if fields, ok = c.parseFields(o.val); !ok {
				return 2
			}
		case "a":
			allowEmpty = true
		}
	}
	split0 := c.sub == "split0"
	sep := "\x00"
	if !split0 {
		if len(ops) == 0 {
			return c.fail("missing argument")
		}
		sep, ops = ops[0], ops[1:]
	}
	if fields == nil && allowEmpty {
		return c.fail("invalid option combination, --allow-empty is only valid with --fields")
	}
	var all [][]string
	nsplits, nargs := 0, 0
	it := c.items(ops, split0)
	for s, _, ok := it.next(); ok; s, _, ok = it.next() {
		parts := sbSplitAbout(s, sep, max, noEmpty, right)
		if quiet && len(parts) > 1 {
			return 0
		}
		nsplits += len(parts)
		nargs++
		all = append(all, parts)
	}
	status := 1
	if nsplits > nargs {
		status = 0
	}
	if quiet {
		return status
	}
	for _, parts := range all {
		if split0 && len(parts) > 0 && parts[len(parts)-1] == "" {
			// split0 ignores a trailing NUL: a\0b\0 is two elements
			parts = parts[:len(parts)-1]
		}
		if fields == nil {
			for _, p := range parts {
				c.out.WriteString(p)
				c.out.WriteByte('\n')
			}
			continue
		}
		n := int64(len(parts))
		if !allowEmpty {
			// Print nothing more if a requested field does not exist.
			for _, f := range fields {
				if f.first >= n || f.last >= n {
					return 1
				}
			}
		}
		for _, f := range fields {
			if f.first <= f.last {
				for i := f.first; i <= f.last && i < n; i++ {
					c.out.WriteString(parts[i])
					c.out.WriteByte('\n')
				}
				continue
			}
			for i := min(f.first, n-1); i >= f.last; i-- {
				c.out.WriteString(parts[i])
				c.out.WriteByte('\n')
			}
		}
	}
	return status
}

// sbSplitAbout splits s on sep at most max times, from the right if right
// is set. An empty sep splits between characters.
func sbSplitAbout(s, sep string, max int64, noEmpty, right bool) []string {
	var out []string
	keep := func(p string) {
		if !noEmpty || p != "" {
			out = append(out, p)
		}
	}
	h := s
	for remaining := max; remaining > 0 && h != ""; remaining-- {
		if !right {
			at, skip := 0, len(sep)
			if sep == "" {
				_, at = utf8.DecodeRuneInString(h)
				if at == len(h) {
					break
				}
			} else if at = strings.Index(h, sep); at < 0 {
				break
			}
			keep(h[:at])
			h = h[at+skip:]
			continue
		}
		var at int
		if sep == "" {
			_, size := utf8.DecodeLastRuneInString(h)
			at = len(h) - size
			if at == 0 {
				break
			}
		} else if at = strings.LastIndex(h, sep); at < 0 {
			break
		}
		keep(h[at+len(sep):])
		h = h[:at]
	}
	keep(h)
	if right {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out
}

// --- sub --------------------------------------------------------------

func sbSub(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{
		{'l', "length", true}, {'s', "start", true}, {'e', "end", true}, {'q', "quiet", false},
	})
	if !ok {
		return 2
	}
	var start, end, length int64
	startSet, endSet, lengthSet, quiet := false, false, false, false
	for _, o := range opts {
		if o.key == "q" {
			quiet = true
			continue
		}
		v, ok := sbInt(o.val, 64)
		if !ok {
			return c.fail("%s: invalid integer", o.val)
		}
		switch o.key {
		case "l":
			if v < 0 {
				return c.fail("Invalid length value '%s'", o.val)
			}
			length, lengthSet = v, true
		case "s":
			if v == 0 {
				return c.fail("Invalid start value '%s'", o.val)
			}
			start, startSet = v, true
		case "e":
			if v == 0 {
				return c.fail("Invalid end value '%s'", o.val)
			}
			end, endSet = v, true
		}
	}
	if lengthSet && endSet {
		return c.fail("invalid option combination, --end and --length are mutually exclusive")
	}
	// fromEnd turns a 1-based or negative index into a character position.
	fromEnd := func(v, n int64) int64 {
		if v > 0 {
			return v
		}
		if v += n; v < 0 {
			return 0
		}
		return v
	}
	nsub := 0
	it := c.items(ops, false)
	for s, nl, ok := it.next(); ok; s, nl, ok = it.next() {
		offs := sbCharOffsets(s)
		n := int64(len(offs) - 1)
		first := int64(0)
		if startSet {
			first = fromEnd(start, n)
			if start > 0 {
				first--
			}
		}
		first = min(first, n)
		count := n
		switch {
		case lengthSet:
			count = length
		case endSet:
			count = max(fromEnd(end, n)-first, 0)
		}
		last := n
		if count < n-first {
			last = first + count
		}
		nsub++
		if quiet {
			return 0
		}
		c.out.WriteString(s[offs[first]:offs[last]])
		if nl {
			c.out.WriteByte('\n')
		}
	}
	if nsub > 0 {
		return 0
	}
	return 1
}

// --- trim -------------------------------------------------------------

func sbTrim(c *sbCtx, args []string) int {
	opts, ops, ok := c.getopt(args, []sbOptDef{
		{'c', "chars", true}, {'l', "left", false}, {'r', "right", false}, {'q', "quiet", false},
	})
	if !ok {
		return 2
	}
	chars := " \f\n\r\t\v"
	left, right, quiet := false, false, false
	for _, o := range opts {
		switch o.key {
		case "c":
			chars = o.val
		case "l":
			left = true
		case "r":
			right = true
		case "q":
			quiet = true
		}
	}
	if !left && !right {
		left, right = true, true
	}
	set := map[string]bool{}
	co := sbCharOffsets(chars)
	for i := 0; i+1 < len(co); i++ {
		set[chars[co[i]:co[i+1]]] = true
	}
	ntrim := 0
	it := c.items(ops, false)
	for s, nl, ok := it.next(); ok; s, nl, ok = it.next() {
		offs := sbCharOffsets(s)
		n := len(offs) - 1
		lo, hi := 0, n
		if left {
			for lo < n && set[s[offs[lo]:offs[lo+1]]] {
				lo++
			}
		}
		if right && lo < n {
			for hi > lo && set[s[offs[hi-1]:offs[hi]]] {
				hi--
			}
		}
		ntrim += lo + n - hi
		if quiet {
			if ntrim > 0 {
				return 0
			}
			continue
		}
		c.out.WriteString(s[offs[lo]:offs[hi]])
		if nl {
			c.out.WriteByte('\n')
		}
	}
	if ntrim > 0 {
		return 0
	}
	return 1
}

// --- display width ----------------------------------------------------

// sbRuneWidth is fish_wcwidth: -1 for control characters, 0 for combining
// and other zero-width characters, 2 for wide East Asian characters and
// emoji, 1 otherwise.
func sbRuneWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 0x20 || r >= 0x7f && r < 0xa0:
		return -1
	case r < 0x300:
		if r == 0xad { // soft hyphen
			return 0
		}
		return 1
	case r == 0xfe0f: // VS16 widens the previous character, count it here
		return 1
	case r == 0xfe0e:
		return 0
	case r >= 0x1f3fb && r <= 0x1f3ff: // skin tone modifiers
		return 0
	case r >= 0x1160 && r <= 0x11ff, r >= 0xd7b0 && r <= 0xd7ff: // Hangul jungseong/jongseong
		return 0
	case unicode.Is(sbWideTable, r):
		return 2
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc, unicode.Cf, unicode.Zl, unicode.Zp):
		return 0
	case !unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Zs, unicode.Co):
		return 0 // unassigned
	}
	return 1
}

// sbVisibleWidth is fish_wcwidth_visible: like sbRuneWidth but 0 for
// unprintable characters, and -1 for backspace.
func sbVisibleWidth(r rune) int {
	if r == '\b' {
		return -1
	}
	return max(sbRuneWidth(r), 0)
}

// sbWidth is the number of columns s occupies on a single line, not
// counting the escape sequences fish knows about.
func sbWidth(s string) int {
	width := 0
	for _, r := range s {
		// a backspace cannot go further left than the start of the line
		if w := sbVisibleWidth(r); w > 0 || width > 0 {
			width += w
		}
	}
	for pos := 0; ; {
		i := strings.IndexByte(s[pos:], 0x1b)
		if i < 0 {
			break
		}
		pos += i
		n := sbEscapeLen(s[pos:])
		if n == 0 {
			pos++
			continue
		}
		for _, r := range s[pos : pos+n] {
			width -= sbVisibleWidth(r)
		}
		// the sequence may end where a second one starts
		pos += n - 1
	}
	return max(width, 0)
}

// sbEscapeLen returns the length in bytes of the terminal escape sequence
// at the start of s, or 0. It recognizes what fish's escape_code_length
// does: screen/tmux title sequences, OSC, CSI and two-byte sequences.
func sbEscapeLen(s string) int {
	if s == "" || s[0] != 0x1b {
		return 0
	}
	at := func(i int) byte {
		if i < len(s) {
			return s[i]
		}
		return 0
	}
	if at(1) == 'k' || strings.HasPrefix(s[1:], "Ptmux;") {
		tmux := at(1) != 'k'
		for off := 2; ; {
			i := strings.Index(s[off:], "\x1b\\")
			if i < 0 {
				return 2
			}
			end := off + i
			// in tmux, \e\e is an escaped escape and does not terminate
			if !tmux || s[end-1] != 0x1b {
				return end + 2
			}
			off = end + 1
		}
	}
	if at(1) == ']' {
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 || s[i] == '\\' && s[i-1] == 0x1b {
				return i + 1
			}
		}
	}
	if at(1) == '[' {
		if c := at(2); c >= '@' && c <= '_' {
			return 3
		}
		i := 2
		for i < len(s) && s[i] < 0x80 {
			c := s[i]
			i++
			if c >= '@' && c <= '~' {
				break
			}
		}
		return i
	}
	if c := at(1); c >= '@' && c <= '_' {
		return 2
	}
	return 0
}

// sbWideTable lists the characters fish 4.8 shows two columns wide (East
// Asian Wide and Fullwidth, emoji presentation, the CJK planes).
var sbWideTable = &unicode.RangeTable{
	R16: []unicode.Range16{
		{0x1100, 0x115f, 1}, {0x231a, 0x231b, 1}, {0x2329, 0x232a, 1}, {0x23e9, 0x23ec, 1},
		{0x23f0, 0x23f0, 1}, {0x23f3, 0x23f3, 1}, {0x25fd, 0x25fe, 1}, {0x2614, 0x2615, 1},
		{0x2630, 0x2637, 1}, {0x2648, 0x2653, 1}, {0x267f, 0x267f, 1}, {0x268a, 0x268f, 1},
		{0x2693, 0x2693, 1}, {0x26a1, 0x26a1, 1}, {0x26aa, 0x26ab, 1}, {0x26bd, 0x26be, 1},
		{0x26c4, 0x26c5, 1}, {0x26ce, 0x26ce, 1}, {0x26d4, 0x26d4, 1}, {0x26ea, 0x26ea, 1},
		{0x26f2, 0x26f3, 1}, {0x26f5, 0x26f5, 1}, {0x26fa, 0x26fa, 1}, {0x26fd, 0x26fd, 1},
		{0x2705, 0x2705, 1}, {0x270a, 0x270b, 1}, {0x2728, 0x2728, 1}, {0x274c, 0x274c, 1},
		{0x274e, 0x274e, 1}, {0x2753, 0x2755, 1}, {0x2757, 0x2757, 1}, {0x2795, 0x2797, 1},
		{0x27b0, 0x27b0, 1}, {0x27bf, 0x27bf, 1}, {0x2b1b, 0x2b1c, 1}, {0x2b50, 0x2b50, 1},
		{0x2b55, 0x2b55, 1}, {0x2e80, 0x2e99, 1}, {0x2e9b, 0x2ef3, 1}, {0x2f00, 0x2fd5, 1},
		{0x2ff0, 0x3029, 1}, {0x3030, 0x303e, 1}, {0x3041, 0x3096, 1}, {0x309b, 0x30ff, 1},
		{0x3105, 0x312f, 1}, {0x3131, 0x318e, 1}, {0x3190, 0x31e5, 1}, {0x31ef, 0x321e, 1},
		{0x3220, 0x3247, 1}, {0x3250, 0xa48c, 1}, {0xa490, 0xa4c6, 1}, {0xa960, 0xa97c, 1},
		{0xac00, 0xd7a3, 1}, {0xf900, 0xfaff, 1}, {0xfe10, 0xfe19, 1}, {0xfe30, 0xfe52, 1},
		{0xfe54, 0xfe66, 1}, {0xfe68, 0xfe6b, 1}, {0xff01, 0xff60, 1}, {0xffe0, 0xffe6, 1},
	},
	R32: []unicode.Range32{
		{0x16fe0, 0x16fe3, 1}, {0x16ff2, 0x16ff6, 1}, {0x17000, 0x18cd5, 1},
		{0x18cff, 0x18d1e, 1}, {0x18d80, 0x18df2, 1}, {0x1aff0, 0x1aff3, 1},
		{0x1aff5, 0x1affb, 1}, {0x1affd, 0x1affe, 1}, {0x1b000, 0x1b122, 1},
		{0x1b132, 0x1b132, 1}, {0x1b150, 0x1b152, 1}, {0x1b155, 0x1b155, 1},
		{0x1b164, 0x1b167, 1}, {0x1b170, 0x1b2fb, 1}, {0x1d300, 0x1d356, 1},
		{0x1d360, 0x1d376, 1}, {0x1f004, 0x1f004, 1}, {0x1f0cf, 0x1f0cf, 1},
		{0x1f18e, 0x1f18e, 1}, {0x1f191, 0x1f19a, 1}, {0x1f200, 0x1f202, 1},
		{0x1f210, 0x1f23b, 1}, {0x1f240, 0x1f248, 1}, {0x1f250, 0x1f251, 1},
		{0x1f260, 0x1f265, 1}, {0x1f300, 0x1f320, 1}, {0x1f32d, 0x1f335, 1},
		{0x1f337, 0x1f37c, 1}, {0x1f37e, 0x1f393, 1}, {0x1f3a0, 0x1f3ca, 1},
		{0x1f3cf, 0x1f3d3, 1}, {0x1f3e0, 0x1f3f0, 1}, {0x1f3f4, 0x1f3f4, 1},
		{0x1f3f8, 0x1f3fa, 1}, {0x1f400, 0x1f43e, 1}, {0x1f440, 0x1f440, 1},
		{0x1f442, 0x1f4fc, 1}, {0x1f4ff, 0x1f53d, 1}, {0x1f54b, 0x1f54e, 1},
		{0x1f550, 0x1f567, 1}, {0x1f57a, 0x1f57a, 1}, {0x1f595, 0x1f596, 1},
		{0x1f5a4, 0x1f5a4, 1}, {0x1f5fb, 0x1f64f, 1}, {0x1f680, 0x1f6c5, 1},
		{0x1f6cc, 0x1f6cc, 1}, {0x1f6d0, 0x1f6d2, 1}, {0x1f6d5, 0x1f6d8, 1},
		{0x1f6dc, 0x1f6df, 1}, {0x1f6eb, 0x1f6ec, 1}, {0x1f6f4, 0x1f6fc, 1},
		{0x1f7e0, 0x1f7eb, 1}, {0x1f7f0, 0x1f7f0, 1}, {0x1f90c, 0x1f93a, 1},
		{0x1f93c, 0x1f945, 1}, {0x1f947, 0x1f9ff, 1}, {0x1fa70, 0x1fa7c, 1},
		{0x1fa80, 0x1fa8a, 1}, {0x1fa8e, 0x1fac6, 1}, {0x1fac8, 0x1fac8, 1},
		{0x1facd, 0x1fadc, 1}, {0x1fadf, 0x1faea, 1}, {0x1faef, 0x1faf8, 1},
		{0x20000, 0x2fffd, 1}, {0x30000, 0x3fffd, 1},
	},
}

// sbUpperSpecial holds the uppercase mappings that expand to several
// characters (Unicode SpecialCasing.txt).
var sbUpperSpecial = map[rune]string{
	0x00df: "\u0053\u0053", 0x0149: "\u02bc\u004e", 0x01f0: "\u004a\u030c",
	0x0390: "\u0399\u0308\u0301", 0x03b0: "\u03a5\u0308\u0301", 0x0587: "\u0535\u0552",
	0x1e96: "\u0048\u0331", 0x1e97: "\u0054\u0308", 0x1e98: "\u0057\u030a",
	0x1e99: "\u0059\u030a", 0x1e9a: "\u0041\u02be", 0x1f50: "\u03a5\u0313",
	0x1f52: "\u03a5\u0313\u0300", 0x1f54: "\u03a5\u0313\u0301", 0x1f56: "\u03a5\u0313\u0342",
	0x1f80: "\u1f08\u0399", 0x1f81: "\u1f09\u0399", 0x1f82: "\u1f0a\u0399",
	0x1f83: "\u1f0b\u0399", 0x1f84: "\u1f0c\u0399", 0x1f85: "\u1f0d\u0399",
	0x1f86: "\u1f0e\u0399", 0x1f87: "\u1f0f\u0399", 0x1f88: "\u1f08\u0399",
	0x1f89: "\u1f09\u0399", 0x1f8a: "\u1f0a\u0399", 0x1f8b: "\u1f0b\u0399",
	0x1f8c: "\u1f0c\u0399", 0x1f8d: "\u1f0d\u0399", 0x1f8e: "\u1f0e\u0399",
	0x1f8f: "\u1f0f\u0399", 0x1f90: "\u1f28\u0399", 0x1f91: "\u1f29\u0399",
	0x1f92: "\u1f2a\u0399", 0x1f93: "\u1f2b\u0399", 0x1f94: "\u1f2c\u0399",
	0x1f95: "\u1f2d\u0399", 0x1f96: "\u1f2e\u0399", 0x1f97: "\u1f2f\u0399",
	0x1f98: "\u1f28\u0399", 0x1f99: "\u1f29\u0399", 0x1f9a: "\u1f2a\u0399",
	0x1f9b: "\u1f2b\u0399", 0x1f9c: "\u1f2c\u0399", 0x1f9d: "\u1f2d\u0399",
	0x1f9e: "\u1f2e\u0399", 0x1f9f: "\u1f2f\u0399", 0x1fa0: "\u1f68\u0399",
	0x1fa1: "\u1f69\u0399", 0x1fa2: "\u1f6a\u0399", 0x1fa3: "\u1f6b\u0399",
	0x1fa4: "\u1f6c\u0399", 0x1fa5: "\u1f6d\u0399", 0x1fa6: "\u1f6e\u0399",
	0x1fa7: "\u1f6f\u0399", 0x1fa8: "\u1f68\u0399", 0x1fa9: "\u1f69\u0399",
	0x1faa: "\u1f6a\u0399", 0x1fab: "\u1f6b\u0399", 0x1fac: "\u1f6c\u0399",
	0x1fad: "\u1f6d\u0399", 0x1fae: "\u1f6e\u0399", 0x1faf: "\u1f6f\u0399",
	0x1fb2: "\u1fba\u0399", 0x1fb3: "\u0391\u0399", 0x1fb4: "\u0386\u0399",
	0x1fb6: "\u0391\u0342", 0x1fb7: "\u0391\u0342\u0399", 0x1fbc: "\u0391\u0399",
	0x1fc2: "\u1fca\u0399", 0x1fc3: "\u0397\u0399", 0x1fc4: "\u0389\u0399",
	0x1fc6: "\u0397\u0342", 0x1fc7: "\u0397\u0342\u0399", 0x1fcc: "\u0397\u0399",
	0x1fd2: "\u0399\u0308\u0300", 0x1fd3: "\u0399\u0308\u0301", 0x1fd6: "\u0399\u0342",
	0x1fd7: "\u0399\u0308\u0342", 0x1fe2: "\u03a5\u0308\u0300", 0x1fe3: "\u03a5\u0308\u0301",
	0x1fe4: "\u03a1\u0313", 0x1fe6: "\u03a5\u0342", 0x1fe7: "\u03a5\u0308\u0342",
	0x1ff2: "\u1ffa\u0399", 0x1ff3: "\u03a9\u0399", 0x1ff4: "\u038f\u0399",
	0x1ff6: "\u03a9\u0342", 0x1ff7: "\u03a9\u0342\u0399", 0x1ffc: "\u03a9\u0399",
	0xfb00: "\u0046\u0046", 0xfb01: "\u0046\u0049", 0xfb02: "\u0046\u004c",
	0xfb03: "\u0046\u0046\u0049", 0xfb04: "\u0046\u0046\u004c", 0xfb05: "\u0053\u0054",
	0xfb06: "\u0053\u0054", 0xfb13: "\u0544\u0546", 0xfb14: "\u0544\u0535",
	0xfb15: "\u0544\u053b", 0xfb16: "\u054e\u0546", 0xfb17: "\u0544\u053d",
}

// --- regular expressions ----------------------------------------------

// sbRegex is a compiled `string match/replace -r` pattern.
type sbRegex struct {
	re       *regexp.Regexp
	nullable bool // can match the empty string
	leftCtx  bool // has assertions that look before the match (^ \A \b \B)
}

func sbCompileRegex(pat string, icase bool) (*sbRegex, error) {
	t := sbTranslateRegex(pat)
	if icase {
		t = "(?i)" + t
	}
	re, err := regexp.Compile(t)
	if err != nil {
		return nil, err
	}
	// RE2 accepts group names PCRE2 rejects.
	seen := map[string]bool{}
	for _, name := range re.SubexpNames() {
		switch {
		case name == "":
		case name[0] >= '0' && name[0] <= '9':
			return nil, sbRegexErr("subpattern name must start with a non-digit")
		case len(name) > 32:
			return nil, sbRegexErr("subpattern name is too long (maximum 32 code units)")
		case seen[name]:
			return nil, sbRegexErr("two named subpatterns have the same name (PCRE2_DUPNAMES not set)")
		}
		seen[name] = true
	}
	rx := &sbRegex{re: re, nullable: true, leftCtx: true}
	if tree, err := syntax.Parse(t, syntax.Perl); err == nil {
		rx.nullable = sbNullable(tree)
		rx.leftCtx = sbLeftContext(tree)
	}
	return rx, nil
}

type sbRegexErr string

func (e sbRegexErr) Error() string { return string(e) }

// regexError reports a pattern that failed to compile.
func (c *sbCtx) regexError(pat string, err error) int {
	msg := err.Error()
	if se, ok := err.(*syntax.Error); ok {
		msg = sbSyntaxErrMsg(se)
	}
	c.fail("Regular expression compile error: %s", msg)
	fmt.Fprintf(c.io.Err, "string %s: %s\n", c.sub, pat)
	return 2
}

// sbSyntaxErrMsg words an RE2 syntax error like PCRE2 does, and explains
// the PCRE2 features RE2 does not have.
func sbSyntaxErrMsg(se *syntax.Error) string {
	expr := se.Expr
	switch {
	case strings.HasPrefix(expr, "(?=") || strings.HasPrefix(expr, "(?!") ||
		strings.HasPrefix(expr, "(?<=") || strings.HasPrefix(expr, "(?<!"):
		return "lookaround assertions are not supported (RE2 syntax)"
	case se.Code == syntax.ErrInvalidEscape && len(expr) > 1 && strings.IndexByte("123456789gk", expr[1]) >= 0:
		return "backreferences are not supported (RE2 syntax)"
	}
	switch se.Code {
	case syntax.ErrMissingParen:
		return "missing closing parenthesis"
	case syntax.ErrUnexpectedParen:
		return "unmatched closing parenthesis"
	case syntax.ErrMissingBracket:
		return "missing terminating ] for character class"
	case syntax.ErrTrailingBackslash:
		return `\ at end of pattern`
	case syntax.ErrInvalidCharRange:
		return "range out of order in character class"
	case syntax.ErrMissingRepeatArgument, syntax.ErrInvalidRepeatOp:
		return "quantifier does not follow a repeatable item"
	case syntax.ErrInvalidRepeatSize:
		return "numbers out of order in {} quantifier"
	case syntax.ErrInvalidNamedCapture:
		return "subpattern name expected"
	}
	return fmt.Sprintf("%s: %s (RE2 syntax)", se.Code, se.Expr)
}

// sbTranslateRegex rewrites PCRE2 syntax that RE2 lacks into equivalents.
func sbTranslateRegex(p string) string {
	var b strings.Builder
	inClass := false
	afterQuant := false // a '+' now would make the quantifier possessive
	for i := 0; i < len(p); {
		c := p[i]
		if inClass {
			switch {
			case c == '\\' && i+1 < len(p):
				i = sbTranslateEscape(&b, p, i, true)
			case c == '[' && i+1 < len(p) && p[i+1] == ':':
				if j := strings.Index(p[i+2:], ":]"); j >= 0 {
					b.WriteString(p[i : i+j+4])
					i += j + 4
				} else {
					b.WriteString(`\[`)
					i++
				}
			case c == '[':
				b.WriteString(`\[`)
				i++
			case c == ']':
				inClass = false
				b.WriteByte(']')
				i++
			default:
				_, size := utf8.DecodeRuneInString(p[i:])
				b.WriteString(p[i : i+size])
				i += size
			}
			continue
		}
		quant := false
		switch c {
		case '\\':
			if strings.HasPrefix(p[i:], `\Q`) {
				// quoted text is copied unchanged up to \E
				j := strings.Index(p[i+2:], `\E`)
				if j < 0 {
					b.WriteString(p[i:])
					i = len(p)
				} else {
					b.WriteString(p[i : i+j+4])
					i += j + 4
				}
				break
			}
			i = sbTranslateEscape(&b, p, i, false)
		case '[':
			inClass = true
			b.WriteByte('[')
			i++
			if i < len(p) && p[i] == '^' {
				b.WriteByte('^')
				i++
			}
			if i < len(p) && p[i] == ']' {
				// a ] right after [ or [^ is literal
				b.WriteString(`\]`)
				i++
			}
		case '(':
			switch rest := p[i:]; {
			case strings.HasPrefix(rest, "(?#"):
				if j := strings.IndexByte(rest, ')'); j >= 0 {
					i += j + 1
				} else {
					i = len(p)
				}
			case strings.HasPrefix(rest, "(?'"):
				if j := strings.IndexByte(rest[3:], '\''); j >= 0 {
					b.WriteString("(?P<" + rest[3:3+j] + ">")
					i += j + 4
				} else {
					b.WriteString("(?'")
					i += 3
				}
			case strings.HasPrefix(rest, "(?>"):
				b.WriteString("(?:")
				i += 3
			case strings.HasPrefix(rest, "(?"):
				b.WriteString("(?")
				i += 2
			default:
				b.WriteByte('(')
				i++
			}
		case '*', '+', '?':
			switch {
			case c == '+' && afterQuant:
				// possessive quantifier: RE2 has no backtracking to prevent
				i++
			case c == '?' && afterQuant:
				b.WriteByte('?') // lazy
				i++
			default:
				b.WriteByte(c)
				i++
				quant = true
			}
		case '{':
			if j, lo, hi, ok := sbQuantifier(p, i); ok {
				if lo == "" {
					lo = "0"
				}
				b.WriteString("{" + lo + hi + "}")
				i = j
				quant = true
			} else {
				b.WriteString(`\{`)
				i++
			}
		default:
			_, size := utf8.DecodeRuneInString(p[i:])
			b.WriteString(p[i : i+size])
			i += size
		}
		afterQuant = quant
	}
	return b.String()
}

// sbQuantifier recognizes {n}, {n,}, {n,m} and {,m} at p[i]. It returns
// the index after the closing brace, the lower bound and the rest
// (",m", "," or "").
func sbQuantifier(p string, i int) (int, string, string, bool) {
	j := i + 1
	k := j
	for k < len(p) && p[k] >= '0' && p[k] <= '9' {
		k++
	}
	lo := p[j:k]
	if k < len(p) && p[k] == '}' && lo != "" {
		return k + 1, lo, "", true
	}
	if k >= len(p) || p[k] != ',' {
		return 0, "", "", false
	}
	m := k + 1
	for m < len(p) && p[m] >= '0' && p[m] <= '9' {
		m++
	}
	if m >= len(p) || p[m] != '}' || lo == "" && m == k+1 {
		return 0, "", "", false
	}
	return m + 1, lo, p[k:m], true
}

const (
	sbHSpace = `\t \x{a0}\x{1680}\x{180e}\x{2000}-\x{200a}\x{202f}\x{205f}\x{3000}`
	sbVSpace = `\n\x0b\f\r\x{85}\x{2028}\x{2029}`
)

// sbTranslateEscape translates the escape at p[i] and returns the index
// after it.
func sbTranslateEscape(b *strings.Builder, p string, i int, inClass bool) int {
	if i+1 >= len(p) {
		b.WriteByte('\\')
		return i + 1
	}
	set := func(chars string, negate bool) {
		switch {
		case inClass && !negate:
			b.WriteString(chars)
		case negate:
			b.WriteString("[^" + chars + "]")
		default:
			b.WriteString("[" + chars + "]")
		}
	}
	switch e := p[i+1]; {
	case e == 'e':
		b.WriteString(`\x1b`)
	case e == 'h' || e == 'H' && !inClass:
		set(sbHSpace, e == 'H')
	case e == 'v' || e == 'V' && !inClass:
		set(sbVSpace, e == 'V')
	case e == 's' || e == 'S' && !inClass:
		// PCRE2's \s includes vertical tab
		set(`\t\n\x0b\f\r `, e == 'S')
	case e == 'R' && !inClass:
		b.WriteString(`(?:\r\n|[` + sbVSpace + `])`)
	case e == 'N' && !inClass && !strings.HasPrefix(p[i+2:], "{"):
		b.WriteString(`[^\n]`)
	case e == 'b' && inClass:
		b.WriteString(`\x08`)
	case e == 'Z':
		b.WriteString(`\z`)
	case e == 'G':
		b.WriteString(`\A`)
	case e == 'c' && i+2 < len(p) && p[i+2] < 0x80:
		x := p[i+2]
		if x >= 'a' && x <= 'z' {
			x -= 32
		}
		fmt.Fprintf(b, `\x{%x}`, x^0x40)
		return i + 3
	case e == 'o' && strings.HasPrefix(p[i+2:], "{"):
		if j := strings.IndexByte(p[i+3:], '}'); j >= 0 {
			if v, err := strconv.ParseUint(p[i+3:i+3+j], 8, 32); err == nil {
				fmt.Fprintf(b, `\x{%x}`, v)
				return i + 4 + j
			}
		}
		b.WriteString(`\o`)
	case strings.IndexByte("pPxgkN", e) >= 0 && strings.HasPrefix(p[i+2:], "{"):
		// \p{Greek}, \x{41}: copy the braces too, they are no quantifier
		end := len(p)
		if j := strings.IndexByte(p[i+2:], '}'); j >= 0 {
			end = i + 3 + j
		}
		b.WriteString(p[i:end])
		return end
	default:
		_, size := utf8.DecodeRuneInString(p[i+1:])
		b.WriteString(p[i : i+1+size])
		return i + 1 + size
	}
	return i + 2
}

// sbNullable reports whether re can match the empty string.
func sbNullable(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpNoMatch, syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return false
	case syntax.OpLiteral:
		return len(re.Rune) == 0
	case syntax.OpCapture, syntax.OpPlus:
		return sbNullable(re.Sub[0])
	case syntax.OpRepeat:
		return re.Min == 0 || sbNullable(re.Sub[0])
	case syntax.OpConcat:
		for _, s := range re.Sub {
			if !sbNullable(s) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		for _, s := range re.Sub {
			if sbNullable(s) {
				return true
			}
		}
		return false
	}
	return true
}

func sbLeftContext(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpBeginLine, syntax.OpBeginText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	}
	for _, s := range re.Sub {
		if sbLeftContext(s) {
			return true
		}
	}
	return false
}

// globalMatches finds the matches a PCRE2 global substitution replaces.
// Unlike RE2's FindAll it allows an empty match right after a non-empty
// one ("baaac" =~ s/a*/x/g gives "xbxxcx").
func (rx *sbRegex) globalMatches(s string) [][]int {
	if !rx.nullable || rx.leftCtx {
		return rx.re.FindAllStringSubmatchIndex(s, -1)
	}
	var out [][]int
	for pos := 0; pos <= len(s); {
		m := rx.re.FindStringSubmatchIndex(s[pos:])
		if m == nil {
			break
		}
		for k := range m {
			if m[k] >= 0 {
				m[k] += pos
			}
		}
		out = append(out, m)
		if m[1] > m[0] {
			pos = m[1]
			continue
		}
		if m[1] >= len(s) {
			break
		}
		_, size := utf8.DecodeRuneInString(s[m[1]:])
		pos = m[1] + size
	}
	return out
}

// replace substitutes repl (PCRE2 extended replacement syntax) for the
// first or all matches in s.
func (rx *sbRegex) replace(s, repl string, all bool) (bool, string, error) {
	var ms [][]int
	if all {
		ms = rx.globalMatches(s)
	} else if m := rx.re.FindStringSubmatchIndex(s); m != nil {
		ms = [][]int{m}
	}
	if len(ms) == 0 {
		return false, s, nil
	}
	var b strings.Builder
	prev := 0
	for _, m := range ms {
		b.WriteString(s[prev:m[0]])
		x := &sbSubst{re: rx.re, subject: s, m: m, out: &b}
		if err := x.run(repl); err != nil {
			return false, "", err
		}
		prev = m[1]
	}
	b.WriteString(s[prev:])
	return true, b.String(), nil
}

// Replacement errors, worded like PCRE2's.
const (
	sbErrBadReplacement = sbRegexErr("invalid replacement string")
	sbErrNoSubstring    = sbRegexErr("unknown substring")
	sbErrBadEscape      = sbRegexErr("bad escape sequence in replacement string")
	sbErrMissingBrace   = sbRegexErr("expected closing curly bracket in replacement string")
	sbErrBadSubst       = sbRegexErr("bad substitution in replacement string")
)

// sbSubst expands one replacement for one match. It implements PCRE2's
// extended substitution: $n ${n} $name ${name} $$ ${n:-default}
// ${n:+set:unset}, backslash escapes, \Q..\E and case forcing with
// \u \l \U \L \E (ASCII letters only, as in fish).
type sbSubst struct {
	re      *regexp.Regexp
	subject string
	m       []int
	out     *strings.Builder
	single  int // \u or \l pending for the next character
	persist int // \U or \L in effect
	depth   int
}

func (x *sbSubst) caseFor() int {
	f := x.single
	if f != 0 {
		x.single = 0
		return f
	}
	return x.persist
}

func (x *sbSubst) put(r rune) {
	switch f := x.caseFor(); {
	case f > 0 && r >= 'a' && r <= 'z':
		r -= 'a' - 'A'
	case f < 0 && r >= 'A' && r <= 'Z':
		r += 'a' - 'A'
	}
	x.out.WriteRune(r)
}

// text inserts subject text, which may hold invalid UTF-8.
func (x *sbSubst) text(s string) {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			x.caseFor()
			x.out.WriteByte(s[i])
		} else {
			x.put(r)
		}
		i += size
	}
}

func (x *sbSubst) group(n int) error {
	if n > x.re.NumSubexp() {
		return sbErrNoSubstring
	}
	if x.m[2*n] >= 0 {
		x.text(x.subject[x.m[2*n]:x.m[2*n+1]])
	}
	return nil
}

func (x *sbSubst) run(t string) error {
	if x.depth++; x.depth > 20 {
		return sbErrBadReplacement
	}
	defer func() { x.depth-- }()
	literal := false // inside \Q...\E
	for i := 0; i < len(t); {
		if literal {
			if strings.HasPrefix(t[i:], `\E`) {
				literal = false
				i += 2
				continue
			}
			r, size := utf8.DecodeRuneInString(t[i:])
			x.put(r)
			i += size
			continue
		}
		switch t[i] {
		case '$':
			n, err := x.dollar(t, i)
			if err != nil {
				return err
			}
			i = n
		case '\\':
			if i+1 < len(t) {
				switch t[i+1] {
				case 'U':
					x.persist = 1
					i += 2
					continue
				case 'L':
					x.persist = -1
					i += 2
					continue
				case 'u':
					x.single = 1
					i += 2
					continue
				case 'l':
					x.single = -1
					i += 2
					continue
				}
			}
			n, e, err := sbReplEscape(t, i, x.re.NumSubexp())
			if err != nil {
				return err
			}
			switch e.kind {
			case sbEscChar:
				x.put(e.r)
			case sbEscQuote:
				literal = true
			case sbEscEnd:
				x.single, x.persist = 0, 0
			case sbEscGroup:
				if err := x.group(e.group); err != nil {
					return err
				}
			}
			i = n
		default:
			r, size := utf8.DecodeRuneInString(t[i:])
			if r == utf8.RuneError && size == 1 {
				x.caseFor()
				x.out.WriteByte(t[i])
			} else {
				x.put(r)
			}
			i += size
		}
	}
	return nil
}

func sbIsWordByte(c byte) bool { return c == '_' || sbIsAlnum(c) }

// dollar expands the group reference starting at t[i] ('$').
func (x *sbSubst) dollar(t string, i int) (int, error) {
	i++
	if i >= len(t) {
		return 0, sbErrBadReplacement
	}
	if t[i] == '$' {
		x.put('$')
		return i + 1, nil
	}
	braces, angle, star := false, false, false
	if t[i] == '{' || t[i] == '<' {
		braces, angle = t[i] == '{', t[i] == '<'
		if i++; i >= len(t) {
			return 0, sbErrBadReplacement
		}
	}
	if t[i] == '*' {
		star = true
		if i++; i >= len(t) {
			return 0, sbErrBadReplacement
		}
	}
	group := -1
	name := ""
	if !star && !angle && t[i] >= '0' && t[i] <= '9' {
		group = int(t[i] - '0')
		for i++; i < len(t) && t[i] >= '0' && t[i] <= '9'; i++ {
			if group = group*10 + int(t[i]-'0'); group > x.re.NumSubexp() {
				return 0, sbErrNoSubstring
			}
		}
	} else {
		j := i
		for j < len(t) && sbIsWordByte(t[j]) {
			j++
		}
		if j == i || j-i > 32 {
			return 0, sbErrBadReplacement
		}
		name, i = t[i:j], j
	}
	var special byte
	var text1, text2 string
	switch {
	case braces:
		if !star && i < len(t)-2 && t[i] == ':' {
			special = t[i+1]
			if special != '+' && special != '-' {
				return 0, sbErrBadSubst
			}
			i += 2
			end, err := sbFindTextEnd(t, i, special == '-', x.re.NumSubexp())
			if err != nil {
				return 0, err
			}
			text1, i = t[i:end], end
			if special == '+' && t[i] == ':' {
				i++
				if end, err = sbFindTextEnd(t, i, true, x.re.NumSubexp()); err != nil {
					return 0, err
				}
				text2, i = t[i:end], end
			}
		} else if i >= len(t) || t[i] != '}' {
			return 0, sbErrMissingBrace
		}
		i++
	case angle:
		if i >= len(t) || t[i] != '>' {
			return 0, sbErrBadReplacement
		}
		i++
	}
	if star {
		if name == "MARK" {
			return i, nil // RE2 has no (*MARK)
		}
		return 0, sbErrBadReplacement
	}
	if group < 0 {
		if group = x.re.SubexpIndex(name); group < 0 {
			return 0, sbErrNoSubstring
		}
	}
	if group > x.re.NumSubexp() {
		return 0, sbErrNoSubstring
	}
	set := x.m[2*group] >= 0
	var err error
	switch {
	case special == 0 || special == '-' && set:
		err = x.group(group)
	case special == '-':
		err = x.run(text1)
	case set:
		err = x.run(text1)
	default:
		err = x.run(text2)
	}
	return i, err
}

// sbFindTextEnd finds the end of the text in ${n:-text} or ${n:+a:b}:
// the closing brace, or with last unset also a colon, at nesting level 0.
func sbFindTextEnd(t string, i int, last bool, ngroups int) (int, error) {
	nest := 0
	literal := false
	for ; i < len(t); i++ {
		c := t[i]
		if literal {
			if c == '\\' && i+1 < len(t) && t[i+1] == 'E' {
				literal = false
				i++
			}
			continue
		}
		switch c {
		case '}':
			if nest == 0 {
				return i, nil
			}
			nest--
		case ':':
			if !last && nest == 0 {
				return i, nil
			}
		case '$':
			if i+1 < len(t) && t[i+1] == '{' {
				nest++
				i++
			}
		case '\\':
			if i+1 < len(t) && strings.IndexByte("LlUu", t[i+1]) >= 0 {
				i++
				continue
			}
			n, e, err := sbReplEscape(t, i, ngroups)
			if err != nil {
				return 0, err
			}
			if e.kind == sbEscQuote {
				literal = true
			}
			i = n - 1
		}
	}
	return 0, sbErrMissingBrace
}

const (
	sbEscChar = iota
	sbEscQuote
	sbEscEnd
	sbEscGroup
)

type sbReplEsc struct {
	kind  int
	r     rune
	group int
}

// sbReplEscape parses the backslash escape at t[i] in a replacement string
// and returns the index after it.
func sbReplEscape(t string, i, ngroups int) (int, sbReplEsc, error) {
	if i+1 >= len(t) {
		return 0, sbReplEsc{}, sbErrBadEscape
	}
	char := func(r rune, n int) (int, sbReplEsc, error) {
		if r > unicode.MaxRune || r >= 0xd800 && r <= 0xdfff {
			return 0, sbReplEsc{}, sbErrBadEscape
		}
		return n, sbReplEsc{kind: sbEscChar, r: r}, nil
	}
	e := t[i+1]
	j := i + 2
	switch e {
	case 'a':
		return char(7, j)
	case 'b':
		return char(8, j)
	case 'e':
		return char(0x1b, j)
	case 'f':
		return char(12, j)
	case 'n':
		return char('\n', j)
	case 'r':
		return char('\r', j)
	case 't':
		return char('\t', j)
	case 'v':
		return char(11, j)
	case 'Q':
		return j, sbReplEsc{kind: sbEscQuote}, nil
	case 'E':
		return j, sbReplEsc{kind: sbEscEnd}, nil
	case '0':
		v := rune(0)
		for n := 0; n < 2 && j < len(t) && t[j] >= '0' && t[j] <= '7'; n++ {
			v = v*8 + rune(t[j]-'0')
			j++
		}
		return char(v, j)
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		// \1 to \9 are group references, as are larger numbers when the
		// pattern has that many groups; otherwise they are octal.
		k := i + 1
		v := 0
		for k < len(t) && t[k] >= '0' && t[k] <= '9' && v < 100000 {
			v = v*10 + int(t[k]-'0')
			k++
		}
		if v < 10 || e >= '8' || v <= ngroups {
			return k, sbReplEsc{kind: sbEscGroup, group: v}, nil
		}
		r := rune(0)
		j = i + 1
		for n := 0; n < 3 && j < len(t) && t[j] >= '0' && t[j] <= '7'; n++ {
			r = r*8 + rune(t[j]-'0')
			j++
		}
		return char(r, j)
	case 'o':
		if j >= len(t) || t[j] != '{' {
			break
		}
		end := strings.IndexByte(t[j:], '}')
		if end <= 1 {
			break
		}
		v, err := strconv.ParseUint(t[j+1:j+end], 8, 32)
		if err != nil {
			break
		}
		return char(rune(v), j+end+1)
	case 'x':
		if j < len(t) && t[j] == '{' {
			end := strings.IndexByte(t[j:], '}')
			if end <= 1 {
				break
			}
			v, err := strconv.ParseUint(t[j+1:j+end], 16, 32)
			if err != nil {
				break
			}
			return char(rune(v), j+end+1)
		}
		v, n := rune(0), 0
		for n < 2 && j+n < len(t) && isHex(t[j+n]) {
			v = v*16 + rune(hexVal(t[j+n]))
			n++
		}
		if n == 0 {
			break
		}
		return char(v, j+n)
	case 'c':
		if j >= len(t) || t[j] < 0x20 || t[j] > 0x7e {
			break
		}
		x := t[j]
		if x >= 'a' && x <= 'z' {
			x -= 32
		}
		return char(rune(x^0x40), j+1)
	default:
		if sbIsAlnum(e) {
			break // unknown letter escapes are errors
		}
		r, size := utf8.DecodeRuneInString(t[i+1:])
		return char(r, i+1+size)
	}
	return 0, sbReplEsc{}, sbErrBadEscape
}
