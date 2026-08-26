package shell

import (
	"bufio"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// The math builtin follows fish's: the arguments are joined with spaces
// into one expression, evaluated in floating point by a port of fish's
// modified tinyexpr, and printed with up to --scale decimals.

func init() {
	builtins["math"] = bMath
	builtinHelp["math"] = "perform mathematics calculations"
}

const mathHelp = `Usage: math [OPTIONS] EXPRESSION...
Evaluate a math expression and print the result.

Options:
  -s N, --scale N        decimal places to print (0-15 or max, default 6);
                         a scale of 0 truncates to an integer
  -b BASE, --base BASE   print an integer in base 16 (hex) or 8 (octal)
  -m MODE, --scale-mode MODE
                         truncate, round, floor or ceiling
  -h, --help             show this help

Operators: + - * x / % ^ and parentheses. Use "x" or a quoted "*" for
multiplication. Functions: abs acos asin atan atan2 bitand bitor bitxor ceil
cos cosh exp fac floor ln log log10 log2 max min ncr npr pow round sin sinh
sqrt tan tanh. Constants: e pi tau.
`

type mathMode uint8

const (
	mathModeDefault mathMode = iota // as-is, or truncate when the scale is 0
	mathModeTrunc
	mathModeRound
	mathModeFloor
	mathModeCeil
)

type mathOpts struct {
	scale int // digits after the decimal point
	base  int // 10, 16 or 8
	mode  mathMode
	help  bool
}

func bMath(sh *Shell, io *IO, args []string) int {
	o, rest, st := parseMathArgs(io, args)
	if st >= 0 {
		return st
	}
	if o.help {
		io.Out.WriteString(mathHelp)
		return 0
	}
	if len(rest) == 0 && io.In.ReadsInput() {
		// `echo 1 + 2 | math`: each input line is an argument.
		rest = readArgLines(io.In)
	}
	// Join like fish: empty leading arguments don't add a separator.
	var b strings.Builder
	for _, a := range rest {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(a)
	}
	expr := b.String()
	if expr == "" {
		fmt.Fprintln(io.Err, "math: expected >= 1 arguments; got 0")
		return 1
	}
	v, perr := teInterp([]rune(expr))
	if perr != nil {
		caret := "^"
		if perr.len >= 2 {
			caret = "^" + strings.Repeat("~", perr.len-2) + "^"
		}
		fmt.Fprintf(io.Err, "math: Error: %s\n'%s'\n%s%s\n",
			teErrText[perr.kind], expr, strings.Repeat(" ", perr.pos+1), caret)
		return 1
	}
	var msg string
	switch {
	case math.IsInf(v, 0):
		msg = "Result is infinite"
	case math.IsNaN(v):
		msg = "Result is not a number"
	case math.Abs(v) >= 1<<53:
		// Past 2^53 not every integer is representable.
		msg = "Result magnitude is too large"
	}
	if msg != "" {
		fmt.Fprintf(io.Err, "math: Error: %s\n'%s'\n", msg, expr)
		return 1
	}
	if _, err := io.Out.WriteString(formatMathResult(v, o) + "\n"); err != nil {
		return 1
	}
	return 0
}

// readArgLines reads newline-separated arguments from a stream.
func readArgLines(in *Stream) []string {
	var out []string
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			out = append(out, strings.TrimSuffix(line, "\n"))
		}
		if err != nil {
			return out
		}
	}
}

// parseMathArgs parses math's options. Like fish, it stops at the first
// argument that isn't a known option, since the expression itself may
// start with a minus sign (math -5 + 3). It returns the expression
// arguments, or an exit status >= 0 after an error.
func parseMathArgs(io *IO, args []string) (mathOpts, []string, int) {
	o := mathOpts{scale: 6, base: 10}
	haveScale := false
	set := func(opt byte, val string) string {
		switch opt {
		case 's':
			haveScale = true
			if val == "max" {
				o.scale = 15
				return ""
			}
			n, err := strconv.ParseInt(strings.TrimFunc(val, unicode.IsSpace), 10, 32)
			if err != nil || n < 0 || n > 15 {
				return val + ": invalid scale"
			}
			o.scale = int(n)
		case 'm':
			switch val {
			case "truncate", "trunc":
				o.mode = mathModeTrunc
			case "round":
				o.mode = mathModeRound
			case "floor":
				o.mode = mathModeFloor
			case "ceiling", "ceil":
				o.mode = mathModeCeil
			default:
				return val + ": invalid mode"
			}
		case 'b':
			switch val {
			case "hex":
				o.base = 16
			case "octal":
				o.base = 8
			default:
				n, err := strconv.ParseInt(strings.TrimFunc(val, unicode.IsSpace), 10, 32)
				if err != nil || n != 8 && n != 16 {
					return val + ": invalid base value"
				}
				o.base = int(n)
			}
		}
		return ""
	}
	fail := func(msg string) (mathOpts, []string, int) {
		fmt.Fprintf(io.Err, "math: %s\n", msg)
		return o, nil, 2
	}
	i := 1
args:
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if len(a) < 2 || a[0] != '-' {
			break
		}
		if a[1] == '-' {
			name, val, hasVal := strings.Cut(a[2:], "=")
			opt := matchMathLongOpt(name)
			if opt == 0 {
				break // unknown or ambiguous: the expression starts here
			}
			i++
			if opt == 'h' {
				if hasVal {
					return fail(a + ": option does not take an argument")
				}
				o.help = true
				continue
			}
			if !hasVal {
				if i >= len(args) {
					return fail(a + ": option requires an argument")
				}
				val = args[i]
				i++
			}
			if msg := set(opt, val); msg != "" {
				return fail(msg)
			}
			continue
		}
		for j := 1; j < len(a); j++ {
			switch c := a[j]; c {
			case 'h':
				o.help = true
			case 's', 'b', 'm':
				val := a[j+1:]
				if val == "" {
					if i+1 >= len(args) {
						return fail("-" + string(c) + ": option requires an argument")
					}
					i++
					val = args[i]
				}
				if msg := set(c, val); msg != "" {
					return fail(msg)
				}
				i++
				continue args
			default:
				// Not an option (e.g. -5 or -pi): the expression starts here.
				break args
			}
		}
		i++
	}
	if haveScale && o.scale != 0 && o.base != 10 {
		return fail("invalid option combination, non-zero scale value only valid for base 10")
	}
	return o, args[i:], -1
}

// matchMathLongOpt resolves a (possibly abbreviated) long option name to
// its short letter, or 0 if it is unknown or ambiguous.
func matchMathLongOpt(name string) byte {
	opts := [...]struct {
		name string
		c    byte
	}{{"scale", 's'}, {"base", 'b'}, {"help", 'h'}, {"scale-mode", 'm'}}
	var found byte
	for _, o := range opts {
		if o.name == name {
			return o.c
		}
		if strings.HasPrefix(o.name, name) {
			if found != 0 {
				return 0
			}
			found = o.c
		}
	}
	return found
}

// formatMathResult prints v the way fish's math does.
func formatMathResult(v float64, o mathOpts) string {
	switch o.base {
	case 16:
		v = math.Trunc(v)
		sign := ""
		if math.Signbit(v) {
			sign = "-"
		}
		return sign + "0x" + strconv.FormatUint(f64ToU64(math.Abs(v)), 16)
	case 8:
		v = math.Trunc(v)
		if v == 0 {
			return "0"
		}
		sign := ""
		if v < 0 {
			sign = "-"
		}
		return sign + "0" + strconv.FormatUint(f64ToU64(math.Abs(v)), 8)
	}
	p := math.Pow10(o.scale)
	v *= p
	switch o.mode {
	case mathModeTrunc:
		v = math.Trunc(v)
	case mathModeRound:
		v = math.Round(v)
	case mathModeFloor:
		v = math.Floor(v)
	case mathModeCeil:
		v = math.Ceil(v)
	default:
		if o.scale == 0 {
			v = math.Trunc(v)
		}
	}
	if o.scale == 0 {
		return strconv.FormatFloat(v, 'f', 0, 64) // keeps "-0", as fish does
	}
	v /= p
	s := strconv.FormatFloat(v, 'f', o.scale, 64)
	if strings.IndexByte(s, '.') >= 0 {
		s = strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
	}
	if s == "" {
		s = "0"
	}
	return s
}

// f64ToU64 converts like Rust's `as u64`: truncating, saturating at the
// ends of the range, and mapping NaN to 0.
func f64ToU64(f float64) uint64 {
	switch {
	case f != f || f <= 0:
		return 0
	case f >= 1<<64:
		return math.MaxUint64
	}
	return uint64(f)
}

// ---- expression evaluation (port of fish's tinyexpr) ----

type teFunc struct {
	arity int // 0 for constants, -1 for variadic
	val   float64
	f1    func(float64) float64
	f2    func(float64, float64) float64
	fn    func([]float64) float64
}

func (f *teFunc) call(a []float64) float64 {
	switch {
	case f.arity == 0:
		return f.val
	case f.f1 != nil:
		return f.f1(a[0])
	case f.f2 != nil:
		return f.f2(a[0], a[1])
	}
	return f.fn(a)
}

func teBitwise(op func(a, b uint64) uint64) *teFunc {
	return &teFunc{arity: 2, f2: func(a, b float64) float64 {
		return float64(op(f64ToU64(a), f64ToU64(b)))
	}}
}

var teFuncs = map[string]*teFunc{
	"abs":    {arity: 1, f1: math.Abs},
	"acos":   {arity: 1, f1: math.Acos},
	"asin":   {arity: 1, f1: math.Asin},
	"atan":   {arity: 1, f1: math.Atan},
	"atan2":  {arity: 2, f2: math.Atan2},
	"bitand": teBitwise(func(a, b uint64) uint64 { return a & b }),
	"bitor":  teBitwise(func(a, b uint64) uint64 { return a | b }),
	"bitxor": teBitwise(func(a, b uint64) uint64 { return a ^ b }),
	"ceil":   {arity: 1, f1: math.Ceil},
	"cos":    {arity: 1, f1: math.Cos},
	"cosh":   {arity: 1, f1: teCosh},
	"e":      {arity: 0, val: math.E},
	"exp":    {arity: 1, f1: teExp},
	"fac":    {arity: 1, f1: teFac},
	"floor":  {arity: 1, f1: math.Floor},
	"ln":     {arity: 1, f1: teLn},
	"log":    {arity: 1, f1: teLog10},
	"log10":  {arity: 1, f1: teLog10},
	"log2":   {arity: 1, f1: teLog2},
	"max":    {arity: -1, fn: teMax},
	"min":    {arity: -1, fn: teMin},
	"ncr":    {arity: 2, f2: teNcr},
	"npr":    {arity: 2, f2: func(n, r float64) float64 { return teNcr(n, r) * teFac(r) }},
	"pi":     {arity: 0, val: math.Pi},
	"pow":    {arity: 2, f2: tePow},
	"round":  {arity: 1, f1: math.Round},
	"sin":    {arity: 1, f1: math.Sin},
	"sinh":   {arity: 1, f1: teSinh},
	"sqrt":   {arity: 1, f1: math.Sqrt},
	"tan":    {arity: 1, f1: math.Tan},
	"tanh":   {arity: 1, f1: teTanh},
	"tau":    {arity: 0, val: 2 * math.Pi},
}

// Go's math package is not correctly rounded for pow, exp and log (Pow
// is often an ulp off), while the C libraries fish uses are, which shows
// with --scale max. These compute the exact result to 192 bits and round
// once.

const hpPrec = 192

var hpConst struct {
	once2, once10 sync.Once
	ln2, ln10     *big.Float
}

func hpFloat(f float64) *big.Float { return new(big.Float).SetPrec(hpPrec).SetFloat64(f) }

func hpLn2() *big.Float {
	hpConst.once2.Do(func() {
		third := new(big.Float).SetPrec(hpPrec).Quo(hpFloat(1), hpFloat(3))
		hpConst.ln2 = hpAtanh2(third) // ln 2 = 2 atanh(1/3)
	})
	return hpConst.ln2
}

func hpLn10() *big.Float {
	hpConst.once10.Do(func() { hpConst.ln10 = hpLn(10) })
	return hpConst.ln10
}

// hpAtanh2 returns 2*atanh(z) = ln((1+z)/(1-z)) for small |z|.
func hpAtanh2(z *big.Float) *big.Float {
	z2 := new(big.Float).SetPrec(hpPrec).Mul(z, z)
	pow := new(big.Float).SetPrec(hpPrec).Set(z)
	sum := new(big.Float).SetPrec(hpPrec).Set(z)
	t := new(big.Float).SetPrec(hpPrec)
	for k := int64(3); ; k += 2 {
		pow.Mul(pow, z2)
		t.Quo(pow, hpFloat(float64(k)))
		if t.Sign() == 0 || t.MantExp(nil) < sum.MantExp(nil)-hpPrec-4 {
			break
		}
		sum.Add(sum, t)
	}
	return sum.Mul(sum, hpFloat(2))
}

// hpLn returns ln(x) for finite x > 0.
func hpLn(x float64) *big.Float {
	m, k := math.Frexp(x)
	if m < math.Sqrt2/2 {
		m, k = m*2, k-1
	}
	num := new(big.Float).SetPrec(hpPrec).Sub(hpFloat(m), hpFloat(1))
	den := new(big.Float).SetPrec(hpPrec).Add(hpFloat(m), hpFloat(1))
	r := hpAtanh2(num.Quo(num, den))
	if k != 0 {
		r.Add(r, new(big.Float).SetPrec(hpPrec).Mul(hpLn2(), hpFloat(float64(k))))
	}
	return r
}

// hpExp returns e^t for |t| < 2^20.
func hpExp(t *big.Float) *big.Float {
	kf, _ := new(big.Float).SetPrec(hpPrec).Quo(t, hpLn2()).Float64()
	k := math.Round(kf)
	r := new(big.Float).SetPrec(hpPrec).Mul(hpLn2(), hpFloat(k))
	r.Sub(t, r) // |r| <= ln2/2
	const halvings = 10
	r.SetMantExp(r, -halvings)
	sum := hpFloat(1)
	term := hpFloat(1)
	for n := 1; ; n++ {
		term.Mul(term, r)
		term.Quo(term, hpFloat(float64(n)))
		if term.Sign() == 0 || term.MantExp(nil) < -hpPrec-4 {
			break
		}
		sum.Add(sum, term)
	}
	for i := 0; i < halvings; i++ {
		sum.Mul(sum, sum)
	}
	return sum.SetMantExp(sum, int(k))
}

func hpRound(x *big.Float) float64 {
	f, _ := x.Float64()
	return f
}

func tePow(x, y float64) float64 {
	r := math.Pow(x, y)
	if r == 0 || math.IsInf(r, 0) || math.IsNaN(r) || x == 0 || y == 0 || x == 1 || y == 1 ||
		math.IsInf(x, 0) || math.IsInf(y, 0) {
		return r
	}
	ax := math.Abs(x) // x < 0 only with an integer y; r has the right sign
	var v *big.Float
	if n := math.Abs(y); n == math.Trunc(n) && n <= 64 {
		// Exact power by repeated multiplication.
		prec := uint(53*n + 64)
		b := new(big.Float).SetPrec(prec).SetFloat64(ax)
		v = new(big.Float).SetPrec(prec).SetInt64(1)
		for i := 0; i < int(n); i++ {
			v.Mul(v, b)
		}
		if y < 0 {
			v = new(big.Float).SetPrec(hpPrec).Quo(hpFloat(1), v)
		}
	} else {
		t := hpLn(ax)
		v = hpExp(t.Mul(t, hpFloat(y)))
	}
	return math.Copysign(hpRound(v), r)
}

func teExp(x float64) float64 {
	r := math.Exp(x)
	if r == 0 || math.IsInf(r, 0) || math.IsNaN(r) || x == 0 {
		return r
	}
	return hpRound(hpExp(hpFloat(x)))
}

func teLn(x float64) float64 {
	if !(x > 0) || math.IsInf(x, 1) || x == 1 {
		return math.Log(x)
	}
	return hpRound(hpLn(x))
}

func teLog2(x float64) float64 {
	if !(x > 0) || math.IsInf(x, 1) {
		return math.Log2(x)
	}
	if m, k := math.Frexp(x); m == 0.5 {
		return float64(k - 1) // exact power of two
	}
	return hpRound(new(big.Float).SetPrec(hpPrec).Quo(hpLn(x), hpLn2()))
}

func teLog10(x float64) float64 {
	if !(x > 0) || math.IsInf(x, 1) || x == 1 {
		return math.Log10(x)
	}
	return hpRound(new(big.Float).SetPrec(hpPrec).Quo(hpLn(x), hpLn10()))
}

// teExpPair returns e^x and e^-x for the hyperbolic functions, or ok=false
// when x is out of the range where that is useful.
func teExpPair(x float64) (ep, em *big.Float, ok bool) {
	if math.IsNaN(x) || math.Abs(x) < 0x1p-60 || math.Abs(x) > 800 {
		return nil, nil, false
	}
	ep = hpExp(hpFloat(x))
	em = new(big.Float).SetPrec(hpPrec).Quo(hpFloat(1), ep)
	return ep, em, true
}

func teSinh(x float64) float64 {
	ep, em, ok := teExpPair(x)
	if !ok {
		return math.Sinh(x)
	}
	s := new(big.Float).SetPrec(hpPrec).Sub(ep, em)
	return hpRound(s.SetMantExp(s, -1))
}

func teCosh(x float64) float64 {
	ep, em, ok := teExpPair(x)
	if !ok {
		return math.Cosh(x)
	}
	s := new(big.Float).SetPrec(hpPrec).Add(ep, em)
	return hpRound(s.SetMantExp(s, -1))
}

func teTanh(x float64) float64 {
	if math.Abs(x) > 22 { // rounds to ±1
		return math.Tanh(x)
	}
	ep, em, ok := teExpPair(x)
	if !ok {
		return math.Tanh(x)
	}
	num := new(big.Float).SetPrec(hpPrec).Sub(ep, em)
	den := new(big.Float).SetPrec(hpPrec).Add(ep, em)
	return hpRound(num.Quo(num, den))
}

func teFac(n float64) float64 {
	if n < 0 {
		return math.NaN()
	}
	if n > 1<<64 {
		return math.Inf(1)
	}
	k := f64ToU64(n)
	acc := uint64(1)
	for i := uint64(1); i <= k; i++ {
		hi, lo := bits.Mul64(acc, i)
		if hi != 0 {
			return math.Inf(1)
		}
		acc = lo
	}
	return float64(acc)
}

func teNcr(n, r float64) float64 {
	if math.IsNaN(n) {
		return math.Inf(1)
	}
	if n < 0 || r < 0 || n < r {
		return math.NaN()
	}
	if n > 1<<64 || r > 1<<64 {
		return math.Inf(1)
	}
	un, ur := f64ToU64(n), f64ToU64(r)
	if ur > un/2 {
		ur = un - ur
	}
	result := uint64(1)
	for i := uint64(1); i <= ur; i++ {
		hi, lo := bits.Mul64(result, un-ur+i)
		if hi != 0 {
			return math.Inf(1)
		}
		result = lo / i
	}
	return float64(result)
}

func teMax(a []float64) float64 {
	acc := math.Inf(-1)
	for _, b := range a {
		switch {
		case math.IsNaN(acc):
		case math.IsNaN(b):
			acc = b
		case acc == b:
			if math.Signbit(acc) { // +0 is larger than -0
				acc = b
			}
		case b > acc:
			acc = b
		}
	}
	return acc
}

func teMin(a []float64) float64 {
	acc := math.Inf(1)
	for _, b := range a {
		switch {
		case math.IsNaN(acc):
		case math.IsNaN(b):
			acc = b
		case acc == b:
			if !math.Signbit(acc) { // -0 is smaller than +0
				acc = b
			}
		case b < acc:
			acc = b
		}
	}
	return acc
}

type teErrKind uint8

const (
	teErrUnknownFunction teErrKind = iota
	teErrMissingClosingParen
	teErrTooFewArgs
	teErrTooManyArgs
	teErrMissingOperator
	teErrUnexpectedToken
	teErrLogicalOperator
	teErrDivByZero
	teErrNumberTooLarge
	teErrUnknown
)

var teErrText = [...]string{
	teErrUnknownFunction:     "Unknown function",
	teErrMissingClosingParen: "Missing closing parenthesis",
	teErrTooFewArgs:          "Too few arguments",
	teErrTooManyArgs:         "Too many arguments",
	teErrMissingOperator:     "Missing operator",
	teErrUnexpectedToken:     "Unexpected token",
	teErrLogicalOperator:     "Logical operations are not supported, use `test` instead",
	teErrDivByZero:           "Division by zero",
	teErrNumberTooLarge:      "Number is too large",
	teErrUnknown:             "Expression is bogus",
}

// teError is a parse error; pos and len are in runes and place the caret.
type teError struct {
	kind     teErrKind
	pos, len int
}

type teTokKind uint8

const (
	teTokError teTokKind = iota
	teTokEnd
	teTokSep
	teTokOpen
	teTokClose
	teTokNumber
	teTokFunc
	teTokInfix
)

type teTok struct {
	kind teTokKind
	num  float64
	fn   *teFunc
	op   rune // + - * / ^ %
}

type teParser struct {
	src []rune
	pos int
	cur teTok
	err *teError
}

// teInterp evaluates an expression.
func teInterp(src []rune) (float64, *teError) {
	p := &teParser{src: src, cur: teTok{kind: teTokEnd}}
	p.next()
	v := p.expr()
	switch {
	case p.cur.kind == teTokEnd:
		return v, nil
	case p.err != nil:
		return v, p.err
	}
	// A superfluous token we have no idea what to do with.
	return v, &teError{kind: teErrTooManyArgs, pos: p.pos}
}

func (p *teParser) setError(kind teErrKind, pos, n int) {
	p.cur = teTok{kind: teTokError}
	p.err = &teError{kind: kind, pos: pos, len: n}
}

func (p *teParser) setErrorHere(kind teErrKind) { p.setError(kind, p.pos, 0) }

// noSpecificError reports whether a new error may replace the current one.
func (p *teParser) noSpecificError() bool {
	return p.cur.kind != teTokError || p.err != nil && p.err.kind == teErrUnknown
}

func isAlphabetic(r rune) bool {
	return unicode.IsLetter(r) || unicode.Is(unicode.Nl, r) || unicode.Is(unicode.Other_Alphabetic, r)
}

// token scans the token at p.pos and returns how many runes it spans.
// skip is set for whitespace and end at the end of the input.
func (p *teParser) token() (int, teTok, bool, bool) {
	if p.pos >= len(p.src) {
		return 0, teTok{}, false, true
	}
	next := p.src[p.pos:]
	c := next[0]
	if v, n, ok := scanRadixLiteral(next); ok {
		if n < 0 {
			p.setError(teErrNumberTooLarge, p.pos, 0)
			return 0, teTok{kind: teTokError}, false, false
		}
		return n, teTok{kind: teTokNumber, num: v}, false, false
	}
	if c >= '0' && c <= '9' || c == '.' {
		v, n, e := scanMathNumber(next)
		switch e {
		case numOK:
			return n, teTok{kind: teTokNumber, num: v}, false, false
		case numOverflow:
			p.setError(teErrNumberTooLarge, p.pos, n)
		default:
			p.setError(teErrUnknown, p.pos+n, 1)
		}
		return n, teTok{kind: teTokError}, false, false
	}
	// An identifier, but not "x" followed by whitespace: that's the
	// multiplication operator.
	if isAlphabetic(c) && !(c == 'x' && len(next) > 1 && unicode.IsSpace(next[1])) {
		n := 0
		for n < len(next) && (isAlphabetic(next[n]) || next[n] >= '0' && next[n] <= '9' || next[n] == '_') {
			n++
		}
		if f := teFuncs[string(next[:n])]; f != nil {
			return n, teTok{kind: teTokFunc, fn: f}, false, false
		}
		if p.noSpecificError() {
			p.setError(teErrUnknownFunction, p.pos, n)
		}
		return n, teTok{kind: teTokError}, false, false
	}
	var tok teTok
	switch c {
	case '+', '-', '/', '^', '%':
		tok = teTok{kind: teTokInfix, op: c}
	case 'x', '*':
		tok = teTok{kind: teTokInfix, op: '*'}
	case '(':
		tok.kind = teTokOpen
	case ')':
		tok.kind = teTokClose
	case ',':
		tok.kind = teTokSep
	case ' ', '\t', '\n', '\r':
		return 1, tok, true, false
	case '=', '>', '<', '&', '|', '!':
		p.setErrorHere(teErrLogicalOperator)
		tok.kind = teTokError
	default:
		p.setErrorHere(teErrMissingOperator)
		tok.kind = teTokError
	}
	return 1, tok, false, false
}

func (p *teParser) next() {
	for {
		n, tok, skip, end := p.token()
		if end {
			p.cur = teTok{kind: teTokEnd}
			return
		}
		p.pos += n
		if !skip {
			p.cur = tok
			return
		}
	}
}

// base = number | constant ["(" ")"] | function-1 power |
// function "(" expr {"," expr} ")" | "(" expr ")"
func (p *teParser) base() float64 {
	switch p.cur.kind {
	case teTokNumber:
		n := p.cur.num
		afterFirst := p.pos
		p.next()
		if p.cur.kind == teTokNumber || p.cur.kind == teTokFunc {
			// Two numbers in a row (math 5 2, math 3 pi): put the error
			// between them.
			ws := 0
			for afterFirst+ws < len(p.src) && strings.ContainsRune(" \t\n\r", p.src[afterFirst+ws]) {
				ws++
			}
			p.setError(teErrMissingOperator, afterFirst, ws)
		}
		return n
	case teTokFunc:
		f := p.cur.fn
		p.next()
		haveOpen := p.cur.kind == teTokOpen
		if haveOpen {
			p.next()
		}
		if f.arity == 0 {
			if haveOpen {
				if p.cur.kind == teTokClose {
					p.next()
				} else if p.noSpecificError() {
					p.setErrorHere(teErrMissingClosingParen)
				}
			}
			return f.val
		}
		var params []float64
		i := 0
		firstErr := -1
		for j := 0; ; j++ {
			if f.arity == j {
				firstErr = p.pos - 1
			}
			params = append(params, p.expr())
			if p.cur.kind != teTokSep {
				break
			}
			p.next()
			i++
		}
		if f.arity < 0 || f.arity == i+1 {
			if !haveOpen {
				return f.call(params)
			}
			if p.cur.kind == teTokClose {
				p.next()
				return f.call(params)
			}
			if p.cur.kind != teTokError {
				p.setErrorHere(teErrMissingClosingParen)
			}
		}
		if p.cur.kind != teTokError || p.err != nil && p.err.kind == teErrUnexpectedToken {
			// Complain about the number of arguments first; a missing
			// parenthesis is more obvious. Variadic functions need one.
			kind := teErrTooManyArgs
			if f.arity < 0 && i == 0 || f.arity >= 0 && i < f.arity {
				kind = teErrTooFewArgs
			}
			if firstErr >= 0 {
				n := p.pos - firstErr
				if p.cur.kind != teTokClose {
					n++
				}
				if p.cur.kind == teTokEnd {
					n-- // no caret past the end of the string
				}
				p.setError(kind, firstErr, n)
			} else {
				p.setErrorHere(kind)
			}
		}
		return math.NaN()
	case teTokOpen:
		p.next()
		v := p.expr()
		if p.cur.kind == teTokClose {
			p.next()
			return v
		}
		if p.cur.kind != teTokError && p.cur.kind != teTokEnd && p.err == nil {
			p.setErrorHere(teErrTooManyArgs)
		} else if p.noSpecificError() {
			p.setErrorHere(teErrMissingClosingParen)
		}
		return math.NaN()
	case teTokEnd:
		// The expression ended early, e.g. `2 -`.
		p.setErrorHere(teErrTooFewArgs)
		return math.NaN()
	}
	if p.noSpecificError() {
		p.setErrorHere(teErrUnexpectedToken)
	}
	return math.NaN()
}

// power = {"-" | "+"} base
func (p *teParser) power() float64 {
	sign := 1.0
	for p.cur.kind == teTokInfix && (p.cur.op == '-' || p.cur.op == '+') {
		if p.cur.op == '-' {
			sign = -sign
		}
		p.next()
	}
	return sign * p.base()
}

// factor = power ["^" factor] (right associative)
func (p *teParser) factor() float64 {
	v := p.power()
	if p.cur.kind == teTokInfix && p.cur.op == '^' {
		p.next()
		v = tePow(v, p.factor())
	}
	return v
}

// term = factor {("*" | "/" | "%") factor}
func (p *teParser) term() float64 {
	v := p.factor()
	for p.cur.kind == teTokInfix && (p.cur.op == '*' || p.cur.op == '/' || p.cur.op == '%') {
		op, opPos := p.cur.op, p.pos-1
		p.next()
		v2 := p.factor()
		switch op {
		case '*':
			v *= v2
		case '/':
			v /= v2
		case '%':
			v = math.Mod(v, v2)
		}
		if v2 == 0 && op != '*' {
			p.setError(teErrDivByZero, opPos, 1)
		}
	}
	return v
}

// expr = term {("+" | "-") term}
func (p *teParser) expr() float64 {
	v := p.term()
	for p.cur.kind == teTokInfix && (p.cur.op == '+' || p.cur.op == '-') {
		op := p.cur.op
		p.next()
		v2 := p.term()
		if op == '+' {
			v += v2
		} else {
			v -= v2
		}
	}
	return v
}

// ---- number scanning (shared with printf) ----

type numErr uint8

const (
	numOK numErr = iota
	numEmpty
	numInvalid
	numOverflow
)

// scanRadixLiteral scans a 0b (binary) or 0o (octal) integer literal, an
// extension: fish only knows decimal and 0x. Underscores may separate
// digits. n is -1 if the value doesn't fit in 64 bits.
func scanRadixLiteral(s []rune) (v float64, n int, ok bool) {
	if len(s) < 3 || s[0] != '0' {
		return 0, 0, false
	}
	base := uint64(0)
	switch s[1] {
	case 'b', 'B':
		base = 2
	case 'o', 'O':
		base = 8
	default:
		return 0, 0, false
	}
	if s[2] < '0' || uint64(s[2]-'0') >= base {
		return 0, 0, false
	}
	var u uint64
	n = 2
	for ; n < len(s); n++ {
		r := s[n]
		if r == '_' {
			continue
		}
		if r < '0' || uint64(r-'0') >= base {
			break
		}
		hi, lo := bits.Mul64(u, base)
		if hi != 0 || lo+uint64(r-'0') < lo {
			return 0, -1, true
		}
		u = lo + uint64(r-'0')
	}
	return float64(u), n, true
}

// scanMathNumber scans the number at the start of s (which begins with a
// digit or '.') like fish's wcstod_underscores: underscores between the
// characters are ignored, and trailing ones are consumed too. It returns
// the value and the runes consumed.
func scanMathNumber(s []rune) (float64, int, numErr) {
	var b strings.Builder
	for _, r := range s {
		if r == '_' {
			continue
		}
		if r >= utf8.RuneSelf || !strings.ContainsRune("0123456789abcdefABCDEFxXpP.+-", r) {
			break // can't be part of a number
		}
		b.WriteRune(r)
	}
	v, n, e := scanFloat(b.String())
	if e != numOK {
		return v, 0, e
	}
	consumed := 0
	for _, r := range s {
		if r != '_' {
			if n == 0 {
				break
			}
			n--
		}
		consumed++
	}
	return v, consumed, numOK
}

// scanFloat parses the floating point number at the start of s the way
// fish's wcstod does: optional whitespace and sign, then a hex float
// (0x1.8p3), inf, infinity, nan, or a decimal number. It returns the value
// and the number of bytes consumed.
func scanFloat(s string) (float64, int, numErr) {
	i := 0
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += n
	}
	if i == len(s) {
		return 0, 0, numEmpty
	}
	rest := s[i:]
	if isHexFloatStart(rest) {
		v, n, ok := parseHexFloat(rest)
		if !ok {
			return 0, 0, numInvalid
		}
		return v, i + n, numOK
	}
	v, n, ok := parseDecFloat(rest)
	if !ok {
		return 0, 0, numInvalid
	}
	// An unsigned decimal that parsed as infinity overflowed. (fish
	// lets "-1e999" through as -inf.)
	if math.IsInf(v, 0) && (rest[0] >= '0' && rest[0] <= '9' || rest[0] == '.') {
		return v, 0, numOverflow
	}
	return v, i + n, numOK
}

// isHexFloatStart reports whether s starts with [+-]0x and a hex digit.
func isHexFloatStart(s string) bool {
	if s != "" && (s[0] == '+' || s[0] == '-') {
		s = s[1:]
	}
	return len(s) >= 3 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') && isHex(s[2])
}

// parseDecFloat parses [+-]digits[.digits][e[+-]digits], or inf/nan.
func parseDecFloat(s string) (float64, int, bool) {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	if i < len(s) && (s[i]|0x20 >= 'a' && s[i]|0x20 <= 'z') {
		return parseInfNan(s, i)
	}
	digits := func() {
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	}
	digits()
	if i < len(s) && s[i] == '.' {
		i++
		digits()
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		// "5e" is the number 5 followed by "e".
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j < len(s) && s[j] >= '0' && s[j] <= '9' {
			i = j
			digits()
		}
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	if err != nil && !math.IsInf(v, 0) {
		return 0, 0, false
	}
	return v, i, true
}

// parseInfNan parses inf, infinity or nan (ASCII, any case) at s[i:];
// s[:i] is an optional sign.
func parseInfNan(s string, i int) (float64, int, bool) {
	neg := i > 0 && s[0] == '-'
	lw := make([]byte, 0, 8)
	for j := i; j < len(s) && j < i+8; j++ {
		c := s[j]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		lw = append(lw, c)
	}
	w := string(lw)
	switch {
	case strings.HasPrefix(w, "nan"):
		if neg {
			return math.Copysign(math.NaN(), -1), i + 3, true
		}
		return math.NaN(), i + 3, true
	case strings.HasPrefix(w, "inf"):
		n := i + 3
		if w == "infinity" {
			n = i + 8
		}
		if neg {
			return math.Inf(-1), n, true
		}
		return math.Inf(1), n, true
	}
	return 0, 0, false
}

// parseHexFloat parses [+-]0x<hex>[.<hex>][p[+-]<dec>], rounding to
// nearest even (a port of fish's hex_float.rs).
func parseHexFloat(s string) (float64, int, bool) {
	i := 0
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	if i+1 >= len(s) || s[i] != '0' || s[i+1]|0x20 != 'x' {
		return 0, 0, false
	}
	i += 2
	seen := false
	for i < len(s) && s[i] == '0' {
		i++
		seen = true
	}
	var mant uint64
	shift := 64
	add := func(d int) {
		seen = true
		if shift > 0 { // ignore digits beyond 64 bits of precision
			shift -= 4
			mant |= uint64(d) << shift
		}
	}
	start := i
	for i < len(s) && isHex(s[i]) {
		add(hexVal(s[i]))
		i++
	}
	pointPos := i - start
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && isHex(s[i]) {
			add(hexVal(s[i]))
			i++
		}
	}
	if !seen {
		return 0, 0, false
	}
	exp := 0
	if i < len(s) && s[i]|0x20 == 'p' {
		i++
		eneg := false
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			eneg = s[i] == '-'
			i++
		}
		j := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			if exp < 1<<24 {
				exp = exp*10 + int(s[i]-'0')
			}
			i++
		}
		if i == j {
			return 0, 0, false
		}
		if eneg {
			exp = -exp
		}
	}
	sign := 1.0
	if neg {
		sign = -1
	}
	if mant == 0 {
		return math.Copysign(0, sign), i, true
	}
	zeros := bits.LeadingZeros64(mant)
	mant <<= zeros
	const trim = 64 - 53
	const half = 1 << (trim - 1)
	round := mant & (1<<trim - 1)
	mant ^= round
	if round > half || round == half && mant&(1<<trim) != 0 {
		if m := mant + 1<<trim; m > mant {
			mant = m
		} else {
			exp++
			mant = 1 << 63
		}
	}
	e := pointPos*4 + exp - 1 - zeros
	if e > 1023 {
		return math.Inf(int(sign)), i, true
	}
	remaining := min(1022+e, 0)
	e = max(e, -1022)
	b := uint64(e+1023)<<52 | (mant<<1)>>12
	if neg {
		b |= 1 << 63
	}
	f := math.Float64frombits(b)
	if remaining < 0 { // denormal
		f = math.Ldexp(f, remaining)
	}
	return f, i, true
}
