package shell

import (
	"strconv"
	"strings"
)

// arith evaluates a bash-style integer arithmetic expression.
func (sh *Shell) arith(expr string) (int64, error) {
	p := &arithParser{sh: sh, s: expr}
	if strings.TrimSpace(expr) == "" {
		return 0, nil
	}
	v, err := p.parse()
	if err != nil {
		return 0, err
	}
	return v, nil
}

type arithParser struct {
	sh     *Shell
	s      string
	pos    int
	noEval int
	depth  int
}

type arithErr struct{ msg string }

func (p *arithParser) fail(format string, args ...any) {
	panic(&arithErr{msg: errorf(format, args...).Error()})
}

func (p *arithParser) parse() (v int64, err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*arithErr); ok {
				err = errorf("arithmetic: %s (in %q)", e.msg, strings.TrimSpace(p.s))
				return
			}
			panic(r)
		}
	}()
	v = p.comma()
	p.skip()
	if p.pos < len(p.s) {
		p.fail("unexpected %q", p.s[p.pos:])
	}
	return v, nil
}

func (p *arithParser) skip() {
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t' || p.s[p.pos] == '\n') {
		p.pos++
	}
}

func (p *arithParser) peekOp(ops ...string) string {
	p.skip()
	for _, op := range ops {
		if strings.HasPrefix(p.s[p.pos:], op) {
			return op
		}
	}
	return ""
}

func (p *arithParser) comma() int64 {
	v := p.assign()
	for p.peekOp(",") != "" {
		p.pos++
		v = p.assign()
	}
	return v
}

var assignOps = []string{"<<=", ">>=", "**=", "*=", "/=", "%=", "+=", "-=", "&=", "^=", "|=", "="}

func (p *arithParser) assign() int64 {
	p.skip()
	save := p.pos
	// Look for "name[sub] op=" without side effects first, so that the
	// subscript in a[i++] is evaluated once whether or not this turns
	// out to be an assignment.
	p.noEval++
	op := ""
	if _, _, ok := p.lvalue(); ok {
		p.skip()
		for _, o := range assignOps {
			if strings.HasPrefix(p.s[p.pos:], o) && !(o == "=" && strings.HasPrefix(p.s[p.pos:], "==")) {
				op = o
				break
			}
		}
	}
	p.noEval--
	p.pos = save
	if op == "" {
		return p.ternary()
	}
	name, idx, _ := p.lvalue()
	p.skip()
	p.pos += len(op)
	rhs := p.assign()
	var v int64
	if op == "=" {
		v = rhs
	} else {
		cur := p.getVar(name, idx)
		v = p.binop(op[:len(op)-1], cur, rhs)
	}
	p.setVar(name, idx, v)
	return v
}

// lvalue parses name or name[expr].
func (p *arithParser) lvalue() (string, *int64, bool) {
	p.skip()
	start := p.pos
	if p.pos >= len(p.s) || !isNameStartByte(p.s[p.pos]) {
		return "", nil, false
	}
	for p.pos < len(p.s) && isNameByte(p.s[p.pos]) {
		p.pos++
	}
	name := p.s[start:p.pos]
	if p.pos < len(p.s) && p.s[p.pos] == '[' {
		p.pos++
		i := p.comma()
		p.skip()
		if p.pos >= len(p.s) || p.s[p.pos] != ']' {
			p.fail("expected ']'")
		}
		p.pos++
		return name, &i, true
	}
	return name, nil, true
}

func isNameStartByte(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isNameByte(c byte) bool      { return isNameStartByte(c) || c >= '0' && c <= '9' }

func (p *arithParser) getVar(name string, idx *int64) int64 {
	vals, _ := p.sh.getVar(name)
	var s string
	if idx != nil {
		if v, ok := bashIndex(vals, *idx); ok {
			s = v
		}
	} else if len(vals) > 0 {
		s = vals[0]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, ok := parseArithNumber(s); ok {
		return n
	}
	if p.depth > 32 {
		p.fail("expression recursion level exceeded")
	}
	sub := &arithParser{sh: p.sh, s: s, depth: p.depth + 1, noEval: p.noEval}
	v := sub.comma()
	return v
}

func (p *arithParser) setVar(name string, idx *int64, v int64) {
	if p.noEval > 0 {
		return
	}
	s := strconv.FormatInt(v, 10)
	if idx != nil {
		vals, _ := p.sh.getVar(name)
		n := *idx
		if n < 0 {
			n += int64(len(vals))
		}
		if n < 0 {
			p.fail("bad array subscript")
		}
		nv := append([]string(nil), vals...)
		for int64(len(nv)) <= n {
			nv = append(nv, "")
		}
		nv[n] = s
		p.sh.SetVar(name, nv, setOpts{scope: setShAuto})
		return
	}
	if err := p.sh.SetVar(name, []string{s}, setOpts{scope: setShAuto}); err != nil {
		p.fail("%v", err)
	}
}

func (p *arithParser) ternary() int64 {
	c := p.binary(0)
	if p.peekOp("?") == "" {
		return c
	}
	p.pos++
	if c == 0 {
		p.noEval++
	}
	a := p.assign()
	if c == 0 {
		p.noEval--
	}
	if p.peekOp(":") == "" {
		p.fail("expected ':'")
	}
	p.pos++
	if c != 0 {
		p.noEval++
	}
	b := p.assign()
	if c != 0 {
		p.noEval--
	}
	if c != 0 {
		return a
	}
	return b
}

var precTable = [][]string{
	{"||"},
	{"&&"},
	{"|"},
	{"^"},
	{"&"},
	{"==", "!="},
	{"<=", ">=", "<", ">"},
	{"<<", ">>"},
	{"+", "-"},
	{"*", "/", "%"},
}

func (p *arithParser) binary(level int) int64 {
	if level == len(precTable) {
		return p.power()
	}
	x := p.binary(level + 1)
	for {
		op := p.peekOp(precTable[level]...)
		if op == "" {
			return x
		}
		// don't confuse | with ||, & with &&, < with <<, etc.
		rest := p.s[p.pos+len(op):]
		if (op == "|" || op == "&") && strings.HasPrefix(rest, op) {
			return x
		}
		if (op == "<" || op == ">") && (strings.HasPrefix(rest, op) || strings.HasPrefix(rest, "=")) {
			return x
		}
		if (op == "<<" || op == ">>" || op == "+" || op == "-" || op == "*" || op == "/" || op == "%" || op == "&" || op == "|" || op == "^") && strings.HasPrefix(rest, "=") {
			return x
		}
		if op == "*" && strings.HasPrefix(rest, "*") {
			return x
		}
		if (op == "+" || op == "-") && strings.HasPrefix(rest, op) {
			// x++ handled in postfix; here it's binary + followed by unary +
		}
		p.pos += len(op)
		switch op {
		case "&&":
			if x == 0 {
				p.noEval++
				p.binary(level + 1)
				p.noEval--
				x = 0
			} else {
				y := p.binary(level + 1)
				x = b2i(y != 0)
			}
			continue
		case "||":
			if x != 0 {
				p.noEval++
				p.binary(level + 1)
				p.noEval--
				x = 1
			} else {
				y := p.binary(level + 1)
				x = b2i(y != 0)
			}
			continue
		}
		y := p.binary(level + 1)
		x = p.binop(op, x, y)
	}
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func (p *arithParser) binop(op string, x, y int64) int64 {
	switch op {
	case "+":
		return x + y
	case "-":
		return x - y
	case "*":
		return x * y
	case "/", "%":
		if y == 0 {
			if p.noEval > 0 {
				return 0
			}
			p.fail("division by zero")
		}
		if op == "/" {
			return x / y
		}
		return x % y
	case "**":
		if y < 0 {
			p.fail("exponent less than 0")
		}
		// Square and multiply: a loop of y steps hangs on 2**1e18.
		r := int64(1)
		for ; y > 0; y >>= 1 {
			if y&1 != 0 {
				r *= x
			}
			x *= x
		}
		return r
	case "<<":
		return x << uint64(y)
	case ">>":
		return x >> uint64(y)
	case "&":
		return x & y
	case "|":
		return x | y
	case "^":
		return x ^ y
	case "==":
		return b2i(x == y)
	case "!=":
		return b2i(x != y)
	case "<":
		return b2i(x < y)
	case "<=":
		return b2i(x <= y)
	case ">":
		return b2i(x > y)
	case ">=":
		return b2i(x >= y)
	}
	p.fail("unknown operator %s", op)
	return 0
}

func (p *arithParser) power() int64 {
	x := p.unary()
	if p.peekOp("**") != "" && !strings.HasPrefix(p.s[p.pos+2:], "=") {
		p.pos += 2
		y := p.power() // right associative
		return p.binop("**", x, y)
	}
	return x
}

func (p *arithParser) unary() int64 {
	p.skip()
	if p.pos >= len(p.s) {
		p.fail("expected an operand")
	}
	switch {
	case strings.HasPrefix(p.s[p.pos:], "++"), strings.HasPrefix(p.s[p.pos:], "--"):
		op := p.s[p.pos : p.pos+2]
		p.pos += 2
		name, idx, ok := p.lvalue()
		if !ok {
			p.fail("%s needs a variable", op)
		}
		v := p.getVar(name, idx)
		if op == "++" {
			v++
		} else {
			v--
		}
		p.setVar(name, idx, v)
		return v
	}
	switch p.s[p.pos] {
	case '!':
		p.pos++
		return b2i(p.unary() == 0)
	case '~':
		p.pos++
		return ^p.unary()
	case '-':
		p.pos++
		return -p.unary()
	case '+':
		p.pos++
		return p.unary()
	}
	return p.postfix()
}

func (p *arithParser) postfix() int64 {
	p.skip()
	save := p.pos
	if name, idx, ok := p.lvalue(); ok {
		p.skip()
		if op := p.peekOp("++", "--"); op != "" {
			p.pos += 2
			v := p.getVar(name, idx)
			nv := v + 1
			if op == "--" {
				nv = v - 1
			}
			p.setVar(name, idx, nv)
			return v
		}
		return p.getVar(name, idx)
	}
	p.pos = save
	return p.primary()
}

func (p *arithParser) primary() int64 {
	p.skip()
	if p.pos >= len(p.s) {
		p.fail("expected an operand")
	}
	if p.s[p.pos] == '(' {
		p.pos++
		v := p.comma()
		p.skip()
		if p.pos >= len(p.s) || p.s[p.pos] != ')' {
			p.fail("expected ')'")
		}
		p.pos++
		return v
	}
	start := p.pos
	hash := false
	for p.pos < len(p.s) && (isNameByte(p.s[p.pos]) || p.s[p.pos] == '#' || hash && p.s[p.pos] == '@') {
		hash = hash || p.s[p.pos] == '#'
		p.pos++
	}
	tok := p.s[start:p.pos]
	if tok == "" {
		p.fail("unexpected %q", p.s[p.pos:])
	}
	n, ok := parseArithNumber(tok)
	if !ok {
		p.fail("invalid number %q", tok)
	}
	return n
}

// parseArithNumber parses 42, 0x2a, 052, 2#101010, 36#z and 64#_ the
// way bash does: digits above 9 are letters (lower case before upper
// case in bases above 36, then @ and _), and a constant too big for 64
// bits wraps around instead of failing, so 0xffffffffffffffff is -1 and
// -9223372036854775808 (which $(( -2**63 )) prints) reads back.
func parseArithNumber(s string) (int64, bool) {
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	} else if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	base := uint64(10)
	switch {
	case strings.Contains(s, "#"):
		i := strings.IndexByte(s, '#')
		b, err := strconv.Atoi(s[:i])
		if err != nil || b < 2 || b > 64 {
			return 0, false
		}
		base = uint64(b)
		s = s[i+1:]
	case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
		base = 16
		s = s[2:]
	case len(s) > 1 && s[0] == '0':
		base = 8
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		var d uint64
		switch {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case c >= 'a' && c <= 'z':
			d = uint64(c-'a') + 10
		case c >= 'A' && c <= 'Z' && base <= 36:
			d = uint64(c-'A') + 10
		case c >= 'A' && c <= 'Z':
			d = uint64(c-'A') + 36
		case c == '@':
			d = 62
		case c == '_':
			d = 63
		default:
			return 0, false
		}
		if d >= base {
			return 0, false
		}
		n = n*base + d
	}
	v := int64(n)
	if neg {
		v = -v
	}
	return v, true
}
