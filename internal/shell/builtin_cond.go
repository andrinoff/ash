package shell

import (
	"cmp"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/andrinoff/ash/internal/syntax"
	"golang.org/x/sys/unix"
)

func init() {
	builtins["test"] = bTest
	builtins["["] = bTest
	builtinHelp["test"] = "evaluate a conditional expression"
	builtinHelp["["] = "evaluate a conditional expression"
}

func bTest(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	if args[0] == "[" {
		if len(a) == 0 || a[len(a)-1] != "]" {
			fmt.Fprintln(io.Err, "[: missing ']'")
			return 2
		}
		a = a[:len(a)-1]
	}
	t := &testParser{sh: sh, io: io, args: a}
	ok, err := t.run()
	if err != nil {
		fmt.Fprintf(io.Err, "%s: %v\n", args[0], err)
		return 2
	}
	if ok {
		return 0
	}
	return 1
}

type testParser struct {
	sh   *Shell
	io   *IO
	args []string
	pos  int
}

func (t *testParser) run() (bool, error) {
	if len(t.args) == 0 {
		return false, nil
	}
	// POSIX rules for small argument counts avoid misparsing "-n" etc.
	switch len(t.args) {
	case 1:
		return t.args[0] != "", nil
	case 2:
		if t.args[0] == "!" {
			return t.args[1] == "", nil
		}
		if isUnaryOp(t.args[0]) {
			return t.sh.unaryTest(t.args[0], t.args[1], t.io)
		}
	case 3:
		if isBinaryOp(t.args[1]) {
			return t.sh.binaryTest(t.args[1], t.args[0], t.args[2])
		}
		if t.args[0] == "!" {
			ok, err := (&testParser{sh: t.sh, io: t.io, args: t.args[1:]}).run()
			return !ok, err
		}
		if t.args[0] == "(" && t.args[2] == ")" {
			return t.args[1] != "", nil
		}
	}
	v, err := t.or()
	if err != nil {
		return false, err
	}
	if t.pos < len(t.args) {
		return false, errorf("unexpected argument '%s'", t.args[t.pos])
	}
	return v, nil
}

func (t *testParser) peek() string {
	if t.pos < len(t.args) {
		return t.args[t.pos]
	}
	return ""
}

func (t *testParser) or() (bool, error) {
	v, err := t.and()
	if err != nil {
		return false, err
	}
	for t.peek() == "-o" {
		t.pos++
		w, err := t.and()
		if err != nil {
			return false, err
		}
		v = v || w
	}
	return v, nil
}

func (t *testParser) and() (bool, error) {
	v, err := t.not()
	if err != nil {
		return false, err
	}
	for t.peek() == "-a" {
		t.pos++
		w, err := t.not()
		if err != nil {
			return false, err
		}
		v = v && w
	}
	return v, nil
}

func (t *testParser) not() (bool, error) {
	if t.peek() == "!" {
		t.pos++
		v, err := t.not()
		return !v, err
	}
	return t.primary()
}

func (t *testParser) primary() (bool, error) {
	if t.pos >= len(t.args) {
		return false, errorf("missing argument")
	}
	a := t.args[t.pos]
	if a == "(" {
		t.pos++
		v, err := t.or()
		if err != nil {
			return false, err
		}
		if t.peek() != ")" {
			return false, errorf("missing ')'")
		}
		t.pos++
		return v, nil
	}
	if t.pos+2 < len(t.args)+0 && t.pos+2 <= len(t.args)-1 && isBinaryOp(t.args[t.pos+1]) {
		x, op, y := t.args[t.pos], t.args[t.pos+1], t.args[t.pos+2]
		t.pos += 3
		return t.sh.binaryTest(op, x, y)
	}
	if isUnaryOp(a) && t.pos+1 < len(t.args) {
		t.pos += 2
		return t.sh.unaryTest(a, t.args[t.pos-1], t.io)
	}
	t.pos++
	return a != "", nil
}

func isUnaryOp(s string) bool {
	switch s {
	case "-b", "-c", "-d", "-e", "-f", "-g", "-G", "-h", "-k", "-L", "-n", "-N", "-O", "-p", "-r", "-s", "-S", "-t", "-u", "-w", "-x", "-z", "-a", "-v":
		return true
	}
	return false
}

func isBinaryOp(s string) bool {
	switch s {
	case "=", "==", "!=", "<", ">", "-eq", "-ne", "-lt", "-le", "-gt", "-ge", "-nt", "-ot", "-ef":
		return true
	}
	return false
}

func (sh *Shell) unaryTest(op, arg string, io *IO) (bool, error) {
	switch op {
	case "-n":
		return arg != "", nil
	case "-z":
		return arg == "", nil
	case "-v":
		return sh.IsSet(arg), nil
	case "-t":
		fd, err := strconv.Atoi(arg)
		if err != nil {
			return false, errorf("%s: invalid file descriptor", arg)
		}
		s := io.fd(fd)
		if s == nil || s.File == nil {
			return false, nil
		}
		_, err = unix.IoctlGetWinsize(int(s.File.Fd()), unix.TIOCGWINSZ)
		return err == nil, nil
	}
	path := sh.absPath(arg)
	if arg == "" {
		return false, nil
	}
	if op == "-h" || op == "-L" {
		st, err := os.Lstat(path)
		return err == nil && st.Mode()&os.ModeSymlink != 0, nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return false, nil
	}
	m := st.Mode()
	switch op {
	case "-e", "-a":
		return true, nil
	case "-f":
		return m.IsRegular(), nil
	case "-d":
		return m.IsDir(), nil
	case "-b":
		return m&os.ModeDevice != 0 && m&os.ModeCharDevice == 0, nil
	case "-c":
		return m&os.ModeCharDevice != 0, nil
	case "-p":
		return m&os.ModeNamedPipe != 0, nil
	case "-S":
		return m&os.ModeSocket != 0, nil
	case "-s":
		return st.Size() > 0, nil
	case "-g":
		return m&os.ModeSetgid != 0, nil
	case "-u":
		return m&os.ModeSetuid != 0, nil
	case "-k":
		return m&os.ModeSticky != 0, nil
	case "-r":
		return unix.Access(path, unix.R_OK) == nil, nil
	case "-w":
		return unix.Access(path, unix.W_OK) == nil, nil
	case "-x":
		return unix.Access(path, unix.X_OK) == nil, nil
	case "-O":
		if s, ok := st.Sys().(*syscall.Stat_t); ok {
			return int(s.Uid) == os.Geteuid(), nil
		}
	case "-G":
		if s, ok := st.Sys().(*syscall.Stat_t); ok {
			return int(s.Gid) == os.Getegid(), nil
		}
	case "-N":
		if s, ok := st.Sys().(*syscall.Stat_t); ok {
			return mtime(s) > atime(s), nil
		}
	}
	return false, nil
}

func (sh *Shell) binaryTest(op, x, y string) (bool, error) {
	switch op {
	case "=", "==":
		return x == y, nil
	case "!=":
		return x != y, nil
	case "<":
		return x < y, nil
	case ">":
		return x > y, nil
	case "-eq", "-ne", "-lt", "-le", "-gt", "-ge":
		c, err := compareNumbers(x, y)
		if err != nil {
			return false, err
		}
		switch op {
		case "-eq":
			return c == 0, nil
		case "-ne":
			return c != 0, nil
		case "-lt":
			return c < 0, nil
		case "-le":
			return c <= 0, nil
		case "-gt":
			return c > 0, nil
		}
		return c >= 0, nil
	case "-nt", "-ot":
		s1, e1 := os.Stat(sh.absPath(x))
		s2, e2 := os.Stat(sh.absPath(y))
		if op == "-nt" {
			if e1 != nil {
				return false, nil
			}
			if e2 != nil {
				return true, nil
			}
			return s1.ModTime().After(s2.ModTime()), nil
		}
		if e2 != nil {
			return false, nil
		}
		if e1 != nil {
			return true, nil
		}
		return s1.ModTime().Before(s2.ModTime()), nil
	case "-ef":
		s1, e1 := os.Stat(sh.absPath(x))
		s2, e2 := os.Stat(sh.absPath(y))
		return e1 == nil && e2 == nil && os.SameFile(s1, s2), nil
	}
	return false, errorf("unknown operator %s", op)
}

// compareNumbers compares two numbers for -eq and friends: exactly when
// both are integers (going through float64 lost precision past 2^53, so
// large values compared equal), and like fish when either is a decimal.
func compareNumbers(x, y string) (int, error) {
	a, err1 := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
	b, err2 := strconv.ParseInt(strings.TrimSpace(y), 10, 64)
	if err1 == nil && err2 == nil {
		return cmp.Compare(a, b), nil
	}
	fa, err := parseTestNumber(x)
	if err != nil {
		return 0, errorf("invalid integer '%s'", x)
	}
	fb, err := parseTestNumber(y)
	if err != nil {
		return 0, errorf("invalid integer '%s'", y)
	}
	return cmp.Compare(fa, fb), nil
}

// parseTestNumber accepts integers and (like fish) decimals.
func parseTestNumber(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return float64(n), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err == nil && (math.IsNaN(f) || math.IsInf(f, 0)) {
		return 0, errorf("invalid number")
	}
	return f, err
}

// evalTest evaluates a [[ ]] expression.
func (sh *Shell) evalTest(e syntax.TestExpr, io *IO) (bool, error) {
	switch e := e.(type) {
	case *syntax.TestParen:
		return sh.evalTest(e.X, io)
	case *syntax.TestUnary:
		if e.Op == "!" {
			ok, err := sh.evalTest(e.X, io)
			return !ok, err
		}
		w := e.X.(*syntax.TestWord)
		s, err := sh.expandString(w.W, io)
		if err != nil {
			return false, err
		}
		if e.Op == "-v" {
			return sh.IsSet(s), nil
		}
		return sh.unaryTest(e.Op, s, io)
	case *syntax.TestWord:
		s, err := sh.expandString(e.W, io)
		return s != "", err
	case *syntax.TestBinary:
		switch e.Op {
		case "&&":
			ok, err := sh.evalTest(e.X, io)
			if err != nil || !ok {
				return false, err
			}
			return sh.evalTest(e.Y, io)
		case "||":
			ok, err := sh.evalTest(e.X, io)
			if err != nil || ok {
				return ok, err
			}
			return sh.evalTest(e.Y, io)
		}
		x, err := sh.expandString(e.X.(*syntax.TestWord).W, io)
		if err != nil {
			return false, err
		}
		yw := e.Y.(*syntax.TestWord).W
		switch e.Op {
		case "==", "=", "!=":
			pat, err := sh.expandPattern(yw, io, false)
			if err != nil {
				return false, err
			}
			m := matchPattern(pat, x)
			if e.Op == "!=" {
				return !m, nil
			}
			return m, nil
		case "=~":
			re, err := sh.expandRegex(yw, io)
			if err != nil {
				return false, err
			}
			rx, err := regexp.Compile(re)
			if err != nil {
				return false, errorf("invalid regex %q: %v", re, err)
			}
			m := rx.FindStringSubmatch(x)
			if m == nil {
				sh.SetVar("BASH_REMATCH", []string{}, setOpts{scope: setGlobal})
				return false, nil
			}
			sh.SetVar("BASH_REMATCH", m, setOpts{scope: setGlobal, path: -1})
			return true, nil
		}
		y, err := sh.expandString(yw, io)
		if err != nil {
			return false, err
		}
		return sh.binaryTest(e.Op, x, y)
	}
	return false, nil
}
