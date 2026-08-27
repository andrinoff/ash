package shell

import (
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// printf follows fish's (itself derived from GNU coreutils), with bash's
// -v VAR, --, %q and width/precision for %b. Like bash, an argument that
// isn't a valid number is reported and printed as 0 (or as the part that
// did convert), and printing goes on with exit status 1.

func init() {
	builtins["printf"] = bPrintf
	builtinHelp["printf"] = "format and print data"
}

const printfUsage = "printf: usage: printf [-v var] format [arguments]\n"

func bPrintf(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	varName, toVar := "", false
	if len(a) > 0 && a[0] == "-v" {
		if len(a) < 2 {
			io.Err.WriteString("printf: -v: option requires an argument\n" + printfUsage)
			return 2
		}
		varName, toVar = a[1], true
		a = a[2:]
		if name, _, err := splitVarIndex(varName); err != nil || !validVarName(name) {
			fmt.Fprintf(io.Err, "printf: %s: invalid variable name\n", varName)
			return 2
		}
	}
	if len(a) > 0 && a[0] == "--" {
		a = a[1:]
	}
	if len(a) == 0 {
		if toVar {
			io.Err.WriteString(printfUsage)
		}
		return 2 // a bare printf is silent, as in fish
	}
	p := &printfState{out: io.Out, errs: io.Err, toVar: toVar}
	format, rest := a[0], a[1:]
	for {
		// The format is reused until the arguments run out.
		used := p.formatOnce(format, rest)
		rest = rest[used:]
		p.flush()
		if used == 0 || len(rest) == 0 || p.stop {
			break
		}
	}
	if toVar {
		if err := printfAssign(sh, varName, string(p.saved)); err != nil {
			fmt.Fprintf(io.Err, "printf: %v\n", err)
			return 1
		}
	}
	return p.status
}

// printfAssign sets name, which may be an indexed array element (bash's
// printf -v 'arr[1]').
func printfAssign(sh *Shell, name, val string) error {
	base, idx, err := splitVarIndex(name)
	if err != nil {
		return err
	}
	if idx == "" {
		return sh.SetVar(base, []string{val}, setOpts{scope: setShAuto})
	}
	n, err := sh.arith(idx)
	if err != nil {
		return err
	}
	vals := append([]string(nil), sh.GetList(base)...)
	if n < 0 {
		n += int64(len(vals))
	}
	if n < 0 || n > 1<<24 {
		return errorf("%s: bad array subscript", name)
	}
	for int64(len(vals)) <= n {
		vals = append(vals, "")
	}
	vals[n] = val
	return sh.SetVar(base, vals, setOpts{scope: setShAuto})
}

type printfState struct {
	out, errs *Stream
	buf       []byte // output not yet written
	saved     []byte // everything printed, for -v
	toVar     bool
	stop      bool // after \c or a fatal error nothing more is printed
	status    int
}

func (p *printfState) flush() {
	if len(p.buf) == 0 {
		return
	}
	if p.toVar {
		p.saved = append(p.saved, p.buf...)
	} else if _, err := p.out.Write(p.buf); err != nil {
		p.stop, p.status = true, 1
	}
	p.buf = p.buf[:0]
}

// warn reports an error; printing continues but the status becomes 1.
func (p *printfState) warn(format string, args ...any) {
	if p.stop {
		return
	}
	p.flush() // keep the output in order with the message
	fmt.Fprintf(p.errs, "printf: "+format+"\n", args...)
	p.status = 1
}

// fail reports an error after which nothing more is printed.
func (p *printfState) fail(format string, args ...any) {
	p.warn(format, args...)
	p.stop = true
}

// formatOnce prints the format once and returns how many arguments it
// used.
func (p *printfState) formatOnce(f string, args []string) int {
	used := 0
	next := func() (string, bool) {
		if used < len(args) {
			used++
			return args[used-1], true
		}
		return "", false
	}
	for i := 0; i < len(f) && !p.stop; {
		switch f[i] {
		case '\\':
			var n int
			var stop bool
			var msg string
			p.buf, n, stop, msg = printfEscape(p.buf, f[i:], false)
			i += n
			if msg != "" {
				p.fail("%s", msg)
			} else if stop {
				p.stop = true
			}
		case '%':
			i += p.directive(f[i:], next)
		default:
			p.buf = append(p.buf, f[i])
			i++
		}
	}
	return used
}

type printfSpec struct {
	minus, plus, space, hash, zero bool
	width                          int
	prec                           int // -1 if none
}

// directive handles the % directive at the start of s and returns its
// length.
func (p *printfState) directive(s string, next func() (string, bool)) int {
	if len(s) > 1 && s[1] == '%' {
		p.buf = append(p.buf, '%')
		return 2
	}
	d := printfSpec{prec: -1}
	quote := false
	i := 1
flags:
	for ; i < len(s); i++ {
		switch s[i] {
		case '-':
			d.minus = true
		case '+':
			d.plus = true
		case ' ':
			d.space = true
		case '#':
			d.hash = true
		case '0':
			d.zero = true
		case '\'': // thousands grouping; there is no separator in the C locale
			quote = true
		default:
			break flags
		}
	}
	if i < len(s) && s[i] == '*' {
		i++
		if a, ok := next(); ok {
			w := p.intArg(a)
			if w < math.MinInt32 || w > math.MaxInt32 {
				p.fail("invalid field width: %s", a)
				return i
			}
			if w < 0 {
				d.minus, w = true, -w
			}
			d.width = int(w)
		}
	} else {
		j := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if j < i {
			w, err := strconv.ParseInt(s[j:i], 10, 32)
			if err != nil {
				p.fail("Number out of range")
				return i
			}
			d.width = int(w)
		}
	}
	dot := i < len(s) && s[i] == '.'
	if dot {
		i++
		if i < len(s) && s[i] == '*' {
			i++
			d.prec = 0
			if a, ok := next(); ok {
				pr := p.intArg(a)
				if pr > math.MaxInt32 {
					p.fail("invalid precision: %s", a)
					return i
				}
				d.prec = int(max(pr, -1)) // negative: as if omitted
			}
		} else {
			j := i
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				i++
			}
			d.prec = 0
			if j < i {
				pr, err := strconv.ParseInt(s[j:i], 10, 32)
				if err != nil {
					p.fail("Number out of range")
					return i
				}
				d.prec = int(pr)
			}
		}
	}
	lenMod := false // C length modifiers are accepted and ignored
	for i < len(s) && strings.IndexByte("hlLjtz", s[i]) >= 0 {
		i, lenMod = i+1, true
	}
	var conv byte
	if i < len(s) {
		conv = s[i]
	}
	if !printfConvOK(conv, quote, d.hash, d.zero, dot, lenMod) {
		end := min(i+1, len(s))
		p.fail("%s: invalid conversion specification", s[:end])
		return end
	}
	arg, _ := next() // a missing argument is an empty string (or 0)
	if d.minus {
		d.zero = false
	}
	switch conv {
	case 'd', 'i':
		v := p.intArg(arg)
		u := uint64(v)
		if v < 0 {
			u = -u
		}
		p.buf = append(p.buf, d.formatInt(conv, v < 0, u)...)
	case 'o', 'u', 'x', 'X':
		p.buf = append(p.buf, d.formatInt(conv, false, p.uintArg(arg))...)
	case 'a', 'A', 'e', 'E', 'f', 'F', 'g', 'G':
		p.buf = append(p.buf, d.formatFloat(conv, p.floatArg(arg))...)
	case 'c':
		_, n := utf8.DecodeRuneInString(arg) // the first character; none for ""
		p.buf = append(p.buf, d.formatStr(arg[:n])...)
	case 's':
		p.buf = append(p.buf, d.formatStr(arg)...)
	case 'q':
		p.buf = append(p.buf, d.formatStr(bashQuote(arg))...)
	case 'b':
		var b []byte
		stop := false
		for j := 0; j < len(arg) && !stop; {
			if arg[j] != '\\' {
				b = append(b, arg[j])
				j++
				continue
			}
			var n int
			var msg string
			b, n, stop, msg = printfEscape(b, arg[j:], true)
			j += n
			if msg != "" {
				p.buf = append(p.buf, b...)
				p.fail("%s", msg)
				return i + 1
			}
		}
		p.buf = append(p.buf, d.formatStr(string(b))...)
		p.stop = p.stop || stop
	}
	return i + 1
}

// printfConvOK reports whether a conversion character is valid with the
// given flags, using the same rules as fish and coreutils.
func printfConvOK(c byte, quote, hash, zero, prec, lenMod bool) bool {
	in := func(set string) bool { return strings.IndexByte(set, c) >= 0 }
	switch {
	case c == 0 || !in("aAcdeEfFgGiosuxXbq"),
		quote && in("aAceEosxXbq"),
		hash && in("cdisubq"),
		zero && in("csbq"),
		prec && c == 'c',
		lenMod && in("bq"):
		return false
	}
	return true
}

// printfEscape expands the backslash escape at the start of s onto dst.
// It returns the buffer, the bytes of s used, whether \c asked to stop all
// output, and an error message for a malformed escape. In %b arguments
// (inArg) octal escapes are \0nnn as well as \nnn.
func printfEscape(dst []byte, s string, inArg bool) ([]byte, int, bool, string) {
	if len(s) < 2 {
		return append(dst, '\\'), 1, false, ""
	}
	switch c := s[1]; c {
	case 'a':
		return append(dst, 7), 2, false, ""
	case 'b':
		return append(dst, 8), 2, false, ""
	case 'c':
		return dst, 2, true, ""
	case 'e', 'E':
		return append(dst, 27), 2, false, ""
	case 'f':
		return append(dst, 12), 2, false, ""
	case 'n':
		return append(dst, '\n'), 2, false, ""
	case 'r':
		return append(dst, '\r'), 2, false, ""
	case 't':
		return append(dst, '\t'), 2, false, ""
	case 'v':
		return append(dst, 11), 2, false, ""
	case '\\', '"':
		return append(dst, c), 2, false, ""
	case '\'', '?':
		if !inArg { // bash, in the format only
			return append(dst, c), 2, false, ""
		}
	case 'x':
		v, n := 0, 0
		for n < 2 && 2+n < len(s) && isHex(s[2+n]) {
			v = v*16 + hexVal(s[2+n])
			n++
		}
		if n == 0 {
			return dst, 2, false, "missing hexadecimal number in escape"
		}
		return append(dst, byte(v)), 2 + n, false, ""
	case 'u', 'U':
		size := 4
		if c == 'U' {
			size = 8
		}
		v, n := 0, 0
		for n < size && 2+n < len(s) && isHex(s[2+n]) {
			v = v*16 + hexVal(s[2+n])
			n++
		}
		if n == 0 {
			return dst, 2, false, "Missing hexadecimal number in Unicode escape"
		}
		if v > unicode.MaxRune || v >= 0xd800 && v <= 0xdfff {
			return dst, 2 + n, false, fmt.Sprintf("Not a valid Unicode character: \\%c%0*x", c, size, v)
		}
		return utf8.AppendRune(dst, rune(v)), 2 + n, false, ""
	case '0', '1', '2', '3', '4', '5', '6', '7':
		i := 1
		if inArg && c == '0' {
			i = 2
		}
		v := 0
		for n := 0; n < 3 && i < len(s) && s[i] >= '0' && s[i] <= '7'; n++ {
			v = v*8 + int(s[i]-'0')
			i++
		}
		return append(dst, byte(v)), i, false, ""
	}
	return append(dst, '\\', s[1]), 2, false, ""
}

// ---- numeric arguments ----

// quotedChar handles a numeric argument like 'a or "a, which stands for
// the character's code.
func quotedChar(s string) (int64, bool) {
	if s == "" || s[0] != '\'' && s[0] != '"' {
		return 0, false
	}
	if len(s) == 1 {
		return 0, true
	}
	r, n := utf8.DecodeRuneInString(s[1:])
	if r == utf8.RuneError && n == 1 {
		return int64(s[1]), true
	}
	return int64(r), true
}

// checkRest reports an argument that wasn't (completely) a number; n is
// how much of s converted.
func (p *printfState) checkRest(s string, n int, e numErr) {
	if e != numOK {
		n = 0
	}
	switch {
	case n == len(s):
	case n == 0:
		p.warn("%s: expected a numeric value", s)
	default:
		p.warn("%s: value not completely converted (can't convert '%s')", s, s[n:])
		// It might be meant as hex ("0x" typo) or decimal.
		if s[0] == '0' && isHex(s[n]) && !p.stop {
			p.errs.WriteString("printf: Hint: a leading '0' without an 'x' indicates an octal number\n")
		}
	}
}

func (p *printfState) intArg(s string) int64 {
	if c, ok := quotedChar(s); ok {
		return c
	}
	mag, neg, n, e := scanInt(s)
	switch {
	case e == numOverflow || !neg && mag > math.MaxInt64 || neg && mag > 1<<63:
		p.warn("%s: Number out of range", s)
		if neg {
			return math.MinInt64
		}
		return math.MaxInt64
	}
	p.checkRest(s, n, e)
	if neg {
		return -int64(mag)
	}
	return int64(mag)
}

func (p *printfState) uintArg(s string) uint64 {
	if c, ok := quotedChar(s); ok {
		return uint64(c)
	}
	mag, neg, n, e := scanInt(s)
	if e == numOverflow {
		p.warn("%s: Number out of range", s)
		return math.MaxUint64
	}
	p.checkRest(s, n, e)
	if neg {
		return -mag // wraps around, as with strtoul
	}
	return mag
}

func (p *printfState) floatArg(s string) float64 {
	if c, ok := quotedChar(s); ok {
		return float64(c)
	}
	v, n, e := scanFloat(s)
	if e == numOverflow {
		p.warn("%s: Number out of range", s)
		return v
	}
	p.checkRest(s, n, e)
	if e != numOK {
		return 0
	}
	return v
}

// scanInt parses an integer at the start of s like strtol with base 0:
// leading whitespace, an optional sign, then 0x hex, 0 octal or decimal
// digits. It returns the magnitude, the sign and the bytes used.
func scanInt(s string) (mag uint64, neg bool, n int, e numErr) {
	i := 0
	for i < len(s) {
		r, m := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += m
	}
	if i == len(s) {
		return 0, false, 0, numEmpty
	}
	if s[i] == '+' || s[i] == '-' {
		neg = s[i] == '-'
		i++
	}
	base := uint64(10)
	zeroEnd := -1 // after a leading 0, which alone is a valid number
	if i < len(s) && s[i] == '0' {
		i++
		zeroEnd = i
		switch {
		case i < len(s) && s[i]|0x20 == 'x':
			i++
			base = 16
		case i < len(s) && s[i] >= '0' && s[i] <= '9':
			base = 8
		default:
			return 0, false, i, numOK
		}
	}
	start := i
	for ; i < len(s); i++ {
		var d uint64
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case c|0x20 >= 'a' && c|0x20 <= 'f':
			d = uint64(c|0x20-'a') + 10
		default:
			d = 99
		}
		if d >= base {
			break
		}
		hi, lo := bits.Mul64(mag, base)
		if hi != 0 || lo+d < lo {
			return 0, neg, 0, numOverflow
		}
		mag = lo + d
	}
	if i == start {
		if zeroEnd >= 0 {
			return 0, false, zeroEnd, numOK
		}
		return 0, false, 0, numInvalid
	}
	return mag, neg && mag != 0, i, numOK
}

// ---- formatting ----

// pad lays out prefix and body in the field width: spaces on the left (or
// the right with -), or zeros between the prefix and the body with 0.
func (d *printfSpec) pad(prefix, body string, zero bool) string {
	n := d.width - len(prefix) - len(body)
	if n <= 0 {
		return prefix + body
	}
	switch {
	case d.minus:
		return prefix + body + strings.Repeat(" ", n)
	case zero:
		return prefix + strings.Repeat("0", n) + body
	}
	return strings.Repeat(" ", n) + prefix + body
}

func (d *printfSpec) formatInt(conv byte, neg bool, u uint64) string {
	prefix, body := "", ""
	prec := d.prec
	zero := d.zero && prec < 0 // a precision disables the 0 flag
	switch conv {
	case 'd', 'i':
		switch {
		case neg:
			prefix = "-"
		case d.plus:
			prefix = "+"
		case d.space:
			prefix = " "
		}
		if u != 0 {
			body = strconv.FormatUint(u, 10)
		}
	case 'u':
		if u != 0 {
			body = strconv.FormatUint(u, 10)
		}
	case 'o':
		if u != 0 {
			body = strconv.FormatUint(u, 8)
		}
		if d.hash && max(prec, 0) <= len(body)+1 {
			prec = len(body) + 1 // a leading 0
		}
	case 'x', 'X':
		if u != 0 {
			body = strconv.FormatUint(u, 16)
			if conv == 'X' {
				body = strings.ToUpper(body)
			}
			if d.hash {
				prefix = "0" + string(conv)
			}
		}
	}
	if prec < 0 {
		prec = 1
	}
	if prec > len(body) {
		body = strings.Repeat("0", prec-len(body)) + body
	}
	return d.pad(prefix, body, zero)
}

func (d *printfSpec) formatFloat(conv byte, v float64) string {
	upper := conv >= 'A' && conv <= 'Z'
	sign := ""
	switch {
	case math.Signbit(v):
		sign = "-"
	case d.plus:
		sign = "+"
	case d.space:
		sign = " "
	}
	if math.IsInf(v, 0) || math.IsNaN(v) {
		s := "inf"
		if math.IsNaN(v) {
			s = "nan"
		}
		if upper {
			s = strings.ToUpper(s)
		}
		return d.pad(sign, s, false)
	}
	a := math.Abs(v)
	var body string
	switch conv | 0x20 {
	case 'a':
		return d.formatHexFloat(sign, a, upper)
	case 'f':
		prec := d.prec
		if prec < 0 {
			prec = 6
		}
		body = strconv.FormatFloat(a, 'f', prec, 64)
		if d.hash && prec == 0 {
			body += "."
		}
	case 'e':
		prec := d.prec
		if prec < 0 {
			prec = 6
		}
		body = strconv.FormatFloat(a, 'e', prec, 64)
		if d.hash && prec == 0 {
			body = body[:1] + "." + body[1:]
		}
	case 'g':
		body = formatG(a, d.prec, d.hash)
	}
	if upper {
		body = strings.ToUpper(body)
	}
	return d.pad(sign, body, d.zero)
}

// formatG implements %g for a >= 0: %e or %f style depending on the
// exponent, without trailing zeros unless # is given.
func formatG(a float64, prec int, hash bool) string {
	switch {
	case prec < 0:
		prec = 6
	case prec == 0:
		prec = 1
	}
	exp := 0
	if a != 0 {
		s := strconv.FormatFloat(a, 'e', prec-1, 64)
		exp, _ = strconv.Atoi(s[strings.IndexByte(s, 'e')+1:])
	}
	var s, tail string
	if exp < -4 || exp >= prec {
		s = strconv.FormatFloat(a, 'e', prec-1, 64)
		k := strings.IndexByte(s, 'e')
		s, tail = s[:k], s[k:]
	} else {
		s = strconv.FormatFloat(a, 'f', prec-1-exp, 64)
	}
	switch {
	case hash && !strings.Contains(s, "."):
		s += "."
	case !hash && strings.Contains(s, "."):
		s = strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
	}
	return s + tail
}

// formatHexFloat implements %a like musl (and fish): 0x1.8p+1, rounding
// to the precision half to even.
func (d *printfSpec) formatHexFloat(sign string, a float64, upper bool) string {
	y, e2 := math.Frexp(a)
	if y != 0 {
		y, e2 = y*2, e2-1 // in [1, 2)
	}
	if d.prec >= 0 && d.prec < 13 {
		round := math.Ldexp(1, 52-4*d.prec)
		y += round
		y -= round
	}
	digits := "0123456789abcdef"
	prefix, p := "0x", "p"
	if upper {
		digits, prefix, p = "0123456789ABCDEF", "0X", "P"
	}
	esign := "+"
	if e2 < 0 {
		esign, e2 = "-", -e2
	}
	exp := p + esign + strconv.Itoa(e2)
	var body []byte
	for {
		x := int(y)
		body = append(body, digits[x])
		y = 16 * (y - float64(x))
		if len(body) == 1 && (y != 0 || d.prec > 0 || d.hash) {
			body = append(body, '.')
		}
		if y == 0 {
			break
		}
	}
	for len(body) < d.prec+2 && d.prec > 0 {
		body = append(body, '0')
	}
	return d.pad(sign+prefix, string(body)+exp, d.zero)
}

// formatStr pads (and with a precision truncates) a string, measuring it
// in terminal columns as fish does.
func (d *printfSpec) formatStr(s string) string {
	var w int
	if d.prec >= 0 {
		s, w = truncateWidth(s, d.prec)
	} else {
		w = textWidth(s)
	}
	if n := d.width - w; n > 0 {
		if d.minus {
			return s + strings.Repeat(" ", n)
		}
		return strings.Repeat(" ", n) + s
	}
	return s
}

// bashQuote quotes s for reuse as shell input the way bash's printf %q
// does: backslashes before special characters, or $'...' when s contains
// control characters.
func bashQuote(s string) string {
	if s == "" {
		return "''"
	}
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 || unicode.IsControl(r) {
			return ansiCQuote(s)
		}
		i += n
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(" \t\n!\"$&'()*,;<>?[\\]^`{|}", c) >= 0 ||
			c == '#' && i == 0 ||
			c == '~' && (i == 0 || s[i-1] == ':' || s[i-1] == '=') {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

func ansiCQuote(s string) string {
	var b strings.Builder
	b.WriteString("$'")
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\a':
			b.WriteString(`\a`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\v':
			b.WriteString(`\v`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == 0x1b:
			b.WriteString(`\E`)
		case r == '\\', r == '\'':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == utf8.RuneError && n == 1 || unicode.IsControl(r):
			for _, c := range []byte(s[i : i+n]) {
				fmt.Fprintf(&b, "\\%03o", c)
			}
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	b.WriteByte('\'')
	return b.String()
}

// ---- display width ----

// textWidth returns how many terminal columns s takes, counted like fish's
// printf: East Asian wide characters and emoji take two, combining marks
// and other invisible characters none.
func textWidth(s string) int {
	w := 0
	for s != "" {
		n := graphemeLen(s)
		w += graphemeWidth(s[:n])
		s = s[n:]
	}
	return w
}

// truncateWidth returns the longest prefix of s made of whole graphemes
// that fits in max columns, and its width.
func truncateWidth(s string, max int) (string, int) {
	w, i := 0, 0
	for w < max && i < len(s) {
		n := graphemeLen(s[i:])
		gw := graphemeWidth(s[i : i+n])
		if w+gw > max {
			break
		}
		w, i = w+gw, i+n
	}
	return s[:i], w
}

const zwj = 0x200d

func isRegionalIndicator(r rune) bool { return r >= 0x1f1e6 && r <= 0x1f1ff }

func isEmojiModifier(r rune) bool { return r >= 0x1f3fb && r <= 0x1f3ff }

// graphemeLen returns the length of the first grapheme cluster of s,
// approximating Unicode's extended grapheme cluster rules (UAX #29).
func graphemeLen(s string) int {
	r, n := utf8.DecodeRuneInString(s)
	if r == '\r' && len(s) > 1 && s[1] == '\n' {
		return 2
	}
	if isGraphemeControl(r) {
		return n
	}
	pict := inRanges(r, extPictRanges)
	prev, i := r, n
	for i < len(s) {
		c, m := utf8.DecodeRuneInString(s[i:])
		switch {
		case c == zwj || c == 0x200c || isEmojiModifier(c) || c >= 0xe0020 && c <= 0xe007f ||
			unicode.In(c, unicode.Mn, unicode.Me, unicode.Mc) || c == 0x0e33 || c == 0x0eb3:
			// extending characters and spacing marks
		case hangulJoins(prev, c):
		case prev == zwj && pict && inRanges(c, extPictRanges): // emoji ZWJ sequence
		case isRegionalIndicator(r) && isRegionalIndicator(prev) && isRegionalIndicator(c) && i == n:
			// a flag: a pair of regional indicators
		default:
			return i
		}
		prev, i = c, i+m
	}
	return i
}

func isGraphemeControl(r rune) bool {
	switch {
	case r < 0x20, r >= 0x7f && r < 0xa0, r == 0x2028, r == 0x2029:
		return true
	case r == 0x200c, r == zwj, r >= 0xe0020 && r <= 0xe007f:
		return false
	}
	return r > 0xa0 && unicode.Is(unicode.Cf, r)
}

// hangulJoins reports whether Hangul jamo or syllables prev and c belong
// to one syllable.
func hangulJoins(prev, c rune) bool {
	kind := func(r rune) byte {
		switch {
		case r >= 0x1100 && r <= 0x115f, r >= 0xa960 && r <= 0xa97c:
			return 'L'
		case r >= 0x1160 && r <= 0x11a7, r >= 0xd7b0 && r <= 0xd7c6:
			return 'V'
		case r >= 0x11a8 && r <= 0x11ff, r >= 0xd7cb && r <= 0xd7fb:
			return 'T'
		case r >= 0xac00 && r <= 0xd7a3:
			if (r-0xac00)%28 == 0 {
				return 'v' // LV
			}
			return 't' // LVT
		}
		return 0
	}
	switch a, b := kind(prev), kind(c); a {
	case 'L':
		return b == 'L' || b == 'V' || b == 'v' || b == 't'
	case 'V', 'v':
		return b == 'V' || b == 'T'
	case 'T', 't':
		return b == 'T'
	}
	return false
}

// graphemeWidth returns the columns a grapheme cluster takes.
func graphemeWidth(g string) int {
	r, n := utf8.DecodeRuneInString(g)
	w := runeWidth(r)
	joined := false
	for _, c := range g[n:] {
		switch {
		case c == 0xfe0f: // emoji presentation
			if inRanges(r, extPictRanges) || r == '#' || r == '*' || r >= '0' && r <= '9' {
				w = 2
			}
		case c == 0xfe0e: // text presentation
			if w == 2 && inRanges(r, extPictRanges) {
				w = 1
			}
		case c == zwj:
			joined = true
			continue
		case joined && inRanges(c, extPictRanges), isEmojiModifier(c):
			// part of an emoji sequence
		case isRegionalIndicator(r) && isRegionalIndicator(c):
			w = 2
		default:
			w += runeWidth(c)
		}
		joined = false
	}
	return w
}

func runeWidth(r rune) int {
	switch {
	case r < 0x300:
		if r == 0xad { // soft hyphen
			return 0
		}
		return 1
	case unicode.In(r, unicode.Mn, unicode.Me), inRanges(r, zeroWidthRanges):
		return 0
	case inRanges(r, wideRanges):
		return 2
	}
	return 1
}

type runeRange struct{ lo, hi rune }

func inRanges(r rune, t []runeRange) bool {
	i := sort.Search(len(t), func(i int) bool { return t[i].hi >= r })
	return i < len(t) && t[i].lo <= r
}

// zeroWidthRanges: Hangul medial vowels and final consonants (which join
// the preceding syllable) and default-ignorable code points.
var zeroWidthRanges = []runeRange{
	{0x034f, 0x034f}, {0x061c, 0x061c}, {0x1160, 0x11ff}, {0x17b4, 0x17b5},
	{0x180b, 0x180f}, {0x200b, 0x200f}, {0x202a, 0x202e}, {0x2060, 0x206f},
	{0x3164, 0x3164}, {0xd7b0, 0xd7ff}, {0xfe00, 0xfe0f}, {0xfeff, 0xfeff},
	{0xffa0, 0xffa0}, {0xfff0, 0xfff8}, {0x1bca0, 0x1bca3}, {0x1d173, 0x1d17a},
	{0xe0000, 0xe0fff},
}

// wideRanges: East Asian Wide and Fullwidth characters (Unicode 16).
var wideRanges = []runeRange{
	{0x1100, 0x115f}, {0x231a, 0x231b}, {0x2329, 0x232a}, {0x23e9, 0x23ec},
	{0x23f0, 0x23f0}, {0x23f3, 0x23f3}, {0x25fd, 0x25fe}, {0x2614, 0x2615},
	{0x2630, 0x2637}, {0x2648, 0x2653}, {0x267f, 0x267f}, {0x268a, 0x268f},
	{0x2693, 0x2693}, {0x26a1, 0x26a1}, {0x26aa, 0x26ab}, {0x26bd, 0x26be},
	{0x26c4, 0x26c5}, {0x26ce, 0x26ce}, {0x26d4, 0x26d4}, {0x26ea, 0x26ea},
	{0x26f2, 0x26f3}, {0x26f5, 0x26f5}, {0x26fa, 0x26fa}, {0x26fd, 0x26fd},
	{0x2705, 0x2705}, {0x270a, 0x270b}, {0x2728, 0x2728}, {0x274c, 0x274c},
	{0x274e, 0x274e}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27b0, 0x27b0}, {0x27bf, 0x27bf}, {0x2b1b, 0x2b1c}, {0x2b50, 0x2b50},
	{0x2b55, 0x2b55}, {0x2e80, 0x2e99}, {0x2e9b, 0x2ef3}, {0x2f00, 0x2fd5},
	{0x2ff0, 0x303e}, {0x3041, 0x3096}, {0x3099, 0x30ff}, {0x3105, 0x312f},
	{0x3131, 0x318e}, {0x3190, 0x31e5}, {0x31ef, 0x321e}, {0x3220, 0x3247},
	{0x3250, 0xa48c}, {0xa490, 0xa4c6}, {0xa960, 0xa97c}, {0xac00, 0xd7a3},
	{0xf900, 0xfaff}, {0xfe10, 0xfe19}, {0xfe30, 0xfe52}, {0xfe54, 0xfe66},
	{0xfe68, 0xfe6b}, {0xff01, 0xff60}, {0xffe0, 0xffe6}, {0x16fe0, 0x16fe4},
	{0x16ff0, 0x16ff1}, {0x17000, 0x187f7}, {0x18800, 0x18cd5}, {0x18cff, 0x18d08},
	{0x1aff0, 0x1aff3}, {0x1aff5, 0x1affb}, {0x1affd, 0x1affe}, {0x1b000, 0x1b122},
	{0x1b132, 0x1b132}, {0x1b150, 0x1b152}, {0x1b155, 0x1b155}, {0x1b164, 0x1b167},
	{0x1b170, 0x1b2fb}, {0x1d300, 0x1d356}, {0x1d360, 0x1d376}, {0x1f004, 0x1f004},
	{0x1f0cf, 0x1f0cf}, {0x1f18e, 0x1f18e}, {0x1f191, 0x1f19a}, {0x1f200, 0x1f202},
	{0x1f210, 0x1f23b}, {0x1f240, 0x1f248}, {0x1f250, 0x1f251}, {0x1f260, 0x1f265},
	{0x1f300, 0x1f320}, {0x1f32d, 0x1f335}, {0x1f337, 0x1f37c}, {0x1f37e, 0x1f393},
	{0x1f3a0, 0x1f3ca}, {0x1f3cf, 0x1f3d3}, {0x1f3e0, 0x1f3f0}, {0x1f3f4, 0x1f3f4},
	{0x1f3f8, 0x1f43e}, {0x1f440, 0x1f440}, {0x1f442, 0x1f4fc}, {0x1f4ff, 0x1f53d},
	{0x1f54b, 0x1f54e}, {0x1f550, 0x1f567}, {0x1f57a, 0x1f57a}, {0x1f595, 0x1f596},
	{0x1f5a4, 0x1f5a4}, {0x1f5fb, 0x1f64f}, {0x1f680, 0x1f6c5}, {0x1f6cc, 0x1f6cc},
	{0x1f6d0, 0x1f6d2}, {0x1f6d5, 0x1f6d7}, {0x1f6dc, 0x1f6df}, {0x1f6eb, 0x1f6ec},
	{0x1f6f4, 0x1f6fc}, {0x1f7e0, 0x1f7eb}, {0x1f7f0, 0x1f7f0}, {0x1f90c, 0x1f93a},
	{0x1f93c, 0x1f945}, {0x1f947, 0x1f9ff}, {0x1fa70, 0x1fa7c}, {0x1fa80, 0x1fa89},
	{0x1fa8f, 0x1fac6}, {0x1face, 0x1fadc}, {0x1fadf, 0x1fae9}, {0x1faf0, 0x1faf8},
	{0x20000, 0x2fffd}, {0x30000, 0x3fffd},
}

// extPictRanges approximates Extended_Pictographic (emoji that can take
// presentation selectors and join ZWJ sequences).
var extPictRanges = []runeRange{
	{0x00a9, 0x00a9}, {0x00ae, 0x00ae}, {0x203c, 0x203c}, {0x2049, 0x2049},
	{0x2122, 0x2122}, {0x2139, 0x2139}, {0x2194, 0x2199}, {0x21a9, 0x21aa},
	{0x231a, 0x231b}, {0x2328, 0x2328}, {0x2388, 0x2388}, {0x23cf, 0x23cf},
	{0x23e9, 0x23f3}, {0x23f8, 0x23fa}, {0x24c2, 0x24c2}, {0x25aa, 0x25ab},
	{0x25b6, 0x25b6}, {0x25c0, 0x25c0}, {0x25fb, 0x25fe}, {0x2600, 0x27bf},
	{0x2934, 0x2935}, {0x2b05, 0x2b07}, {0x2b1b, 0x2b1c}, {0x2b50, 0x2b50},
	{0x2b55, 0x2b55}, {0x3030, 0x3030}, {0x303d, 0x303d}, {0x3297, 0x3297},
	{0x3299, 0x3299}, {0x1f000, 0x1f0ff}, {0x1f10d, 0x1f10f}, {0x1f12f, 0x1f12f},
	{0x1f16c, 0x1f171}, {0x1f17e, 0x1f17f}, {0x1f18e, 0x1f18e}, {0x1f191, 0x1f19a},
	{0x1f1ad, 0x1f1e5}, {0x1f201, 0x1f20f}, {0x1f21a, 0x1f21a}, {0x1f22f, 0x1f22f},
	{0x1f232, 0x1f23a}, {0x1f23c, 0x1f23f}, {0x1f249, 0x1f3fa}, {0x1f400, 0x1f53d},
	{0x1f546, 0x1f64f}, {0x1f680, 0x1f6ff}, {0x1f774, 0x1f77f}, {0x1f7d5, 0x1f7ff},
	{0x1f80c, 0x1f80f}, {0x1f848, 0x1f84f}, {0x1f85a, 0x1f85f}, {0x1f888, 0x1f88f},
	{0x1f8ae, 0x1f8ff}, {0x1f90c, 0x1f93a}, {0x1f93c, 0x1f945}, {0x1f947, 0x1faff},
	{0x1fc00, 0x1fffd},
}
