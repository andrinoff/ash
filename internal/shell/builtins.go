package shell

import (
	"bufio"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/andrinoff/ash/internal/syntax"
)

// BuiltinFunc implements a builtin command. args[0] is the command name.
type BuiltinFunc func(sh *Shell, io *IO, args []string) int

var builtins = map[string]BuiltinFunc{}

// builtinHelp holds one-line descriptions shown by completion and help.
var builtinHelp = map[string]string{}

func init() {
	for k, v := range map[string]BuiltinFunc{
		":":                  bTrue,
		"true":               bTrue,
		"false":              bFalse,
		"cd":                 bCd,
		"pwd":                bPwd,
		"echo":               bEcho,
		"exit":               bExit,
		"return":             bReturn,
		"break":              bBreak,
		"continue":           bContinue,
		"eval":               bEval,
		"source":             bSource,
		".":                  bSource,
		"exec":               bExec,
		"shift":              bShift,
		"alias":              bAlias,
		"unalias":            bUnalias,
		"type":               bType,
		"command":            bCommand,
		"builtin":            bBuiltin,
		"hash":               bHash,
		"umask":              bUmask,
		"pushd":              bPushd,
		"popd":               bPopd,
		"dirs":               bDirs,
		"prevd":              bPrevd,
		"nextd":              bNextd,
		"cdh":                bCdh,
		"let":                bLet,
		"functions":          bFunctions,
		"emit":               bEmit,
		"contains":           bContains,
		"count":              bCount,
		"isatty":             bIsatty,
		"realpath":           bRealpath,
		"add_path":           bAddPath,
		"fish_add_path":      bAddPath,
		"help":               bHelp,
		"times":              bTimes,
		"ulimit":             bUlimit,
		"getopts":            bGetopts,
		"shopt":              bShopt,
		"enable_transience":  bEnableTransience,
		"disable_transience": bDisableTransience,
	} {
		builtins[k] = v
	}
	describe := map[string]string{
		":":             "do nothing, successfully",
		"true":          "do nothing, successfully",
		"false":         "do nothing, unsuccessfully",
		"cd":            "change directory",
		"pwd":           "print working directory",
		"echo":          "print arguments",
		"exit":          "exit the shell",
		"return":        "return from a function",
		"break":         "stop the current loop",
		"continue":      "skip to the next loop iteration",
		"eval":          "run arguments as a command",
		"source":        "run a file in the current shell",
		".":             "run a file in the current shell",
		"exec":          "replace the shell with a command",
		"shift":         "shift positional arguments",
		"alias":         "define or list aliases",
		"unalias":       "remove aliases",
		"type":          "describe what a command name is",
		"command":       "run an external command, skipping functions",
		"builtin":       "run a builtin, skipping functions",
		"hash":          "forget remembered command locations",
		"umask":         "set the file creation mask",
		"pushd":         "push a directory onto the stack",
		"popd":          "pop a directory from the stack",
		"dirs":          "show the directory stack",
		"prevd":         "go to the previous directory",
		"nextd":         "go to the next directory",
		"cdh":           "pick a recent directory",
		"let":           "evaluate arithmetic",
		"functions":     "list, show or erase functions",
		"emit":          "fire an event",
		"contains":      "test if a word is in a list",
		"count":         "count arguments",
		"isatty":        "test if a file descriptor is a terminal",
		"realpath":      "resolve a path",
		"add_path":      "add directories to $PATH",
		"fish_add_path": "add directories to $PATH",
		"help":          "show help",
		"times":         "show process times",
		"ulimit":        "get or set resource limits",
		"getopts":       "parse positional options",
		"shopt":         "accepted for bash compatibility",
	}
	for k, v := range describe {
		builtinHelp[k] = v
	}
}

// RegisterBuiltin adds a builtin (used by the integrations and editor).
func RegisterBuiltin(name string, f BuiltinFunc, help string) {
	builtins[name] = f
	if help != "" {
		builtinHelp[name] = help
	}
}

// BuiltinNames lists builtins.
func BuiltinNames() []string {
	out := make([]string, 0, len(builtins))
	for n := range builtins {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// BuiltinDescription returns a one-line description of a builtin.
func BuiltinDescription(name string) string { return builtinHelp[name] }

func bTrue(sh *Shell, io *IO, args []string) int  { return 0 }
func bFalse(sh *Shell, io *IO, args []string) int { return 1 }

func (sh *Shell) usage(io *IO, name, msg string) int {
	fmt.Fprintf(io.Err, "%s: %s\n", name, msg)
	return 2
}

func bEcho(sh *Shell, io *IO, args []string) int {
	newline, escapes, spaces := true, false, true
	i := 1
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if len(a) < 2 || a[0] != '-' || strings.Trim(a[1:], "neEs") != "" {
			break
		}
		for _, c := range a[1:] {
			switch c {
			case 'n':
				newline = false
			case 'e':
				escapes = true
			case 'E':
				escapes = false
			case 's':
				spaces = false
			}
		}
	}
	var b strings.Builder
	for j, a := range args[i:] {
		if j > 0 && spaces {
			b.WriteByte(' ')
		}
		if escapes {
			u := unescapeEcho(a)
			b.WriteString(u)
			if strings.Contains(a, "\\c") {
				io.Out.WriteString(b.String())
				return 0
			}
		} else {
			b.WriteString(a)
		}
	}
	if newline {
		b.WriteByte('\n')
	}
	if _, err := io.Out.WriteString(b.String()); err != nil {
		return 1
	}
	return 0
}

func bExit(sh *Shell, io *IO, args []string) int {
	status := sh.status
	if len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(io.Err, "exit: %s: numeric argument required\n", args[1])
			status = 2
		} else {
			status = n & 0xff
		}
	}
	sh.ctrl = ctrlExit
	sh.exitStatus = status
	return status
}

func bReturn(sh *Shell, io *IO, args []string) int {
	status := sh.status
	if len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(io.Err, "return: %s: numeric argument required\n", args[1])
			return 2
		}
		status = n & 0xff
	}
	sh.status = status
	if len(sh.callStack) == 0 && len(sh.srcStack) == 0 {
		// fish: return at top level of a script exits it
		if !sh.Interactive {
			sh.ctrl = ctrlExit
			sh.exitStatus = status
		}
		return status
	}
	sh.ctrl = ctrlReturn
	return status
}

func loopLevel(io *IO, name string, args []string) (int, bool) {
	n := 1
	if len(args) > 1 {
		v, err := strconv.Atoi(args[1])
		if err != nil || v < 1 {
			fmt.Fprintf(io.Err, "%s: %s: loop count out of range\n", name, args[1])
			return 0, false
		}
		n = v
	}
	return n, true
}

func bBreak(sh *Shell, io *IO, args []string) int {
	n, ok := loopLevel(io, "break", args)
	if !ok {
		return 1
	}
	if sh.inBlock == 0 {
		fmt.Fprintln(io.Err, "break: not inside of a loop")
		return 1
	}
	sh.ctrl, sh.ctrlLevel = ctrlBreak, n
	return 0
}

func bContinue(sh *Shell, io *IO, args []string) int {
	n, ok := loopLevel(io, "continue", args)
	if !ok {
		return 1
	}
	if sh.inBlock == 0 {
		fmt.Fprintln(io.Err, "continue: not inside of a loop")
		return 1
	}
	sh.ctrl, sh.ctrlLevel = ctrlContinue, n
	return 0
}

func bEval(sh *Shell, io *IO, args []string) int {
	src := strings.Join(args[1:], " ")
	if strings.TrimSpace(src) == "" {
		return 0
	}
	f, err := syntax.Parse(src)
	if err != nil {
		fmt.Fprint(io.Err, "eval: "+FormatParseError(src, "", err))
		return 2
	}
	return sh.runStmts(f.Stmts, io)
}

func bShift(sh *Shell, io *IO, args []string) int {
	n := 1
	if len(args) > 1 {
		v, err := strconv.Atoi(args[1])
		if err != nil || v < 0 {
			fmt.Fprintf(io.Err, "shift: %s: numeric argument required\n", args[1])
			return 1
		}
		n = v
	}
	cur := sh.args()
	if n > len(cur) {
		return 1
	}
	_, where := sh.lookupVar("argv")
	if where == nil {
		where = sh.funcScope()
	}
	where.put("argv", &Var{Vals: append([]string(nil), cur[n:]...)})
	return 0
}

func bLet(sh *Shell, io *IO, args []string) int {
	var v int64
	for _, a := range args[1:] {
		n, err := sh.arith(a)
		if err != nil {
			fmt.Fprintf(io.Err, "let: %v\n", err)
			return 1
		}
		v = n
	}
	if v == 0 {
		return 1
	}
	return 0
}

func bEmit(sh *Shell, io *IO, args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(io.Err, "emit: expected an event name")
		return 2
	}
	sh.Emit(args[1], args[2:]...)
	return 0
}

func bContains(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	index := false
	for len(a) > 0 {
		if a[0] == "-i" || a[0] == "--index" {
			index = true
			a = a[1:]
			continue
		}
		if a[0] == "--" {
			a = a[1:]
		}
		break
	}
	if len(a) == 0 {
		fmt.Fprintln(io.Err, "contains: key not specified")
		return 2
	}
	key := a[0]
	for i, v := range a[1:] {
		if v == key {
			if index {
				fmt.Fprintln(io.Out, i+1)
			}
			return 0
		}
	}
	return 1
}

func bCount(sh *Shell, io *IO, args []string) int {
	n := len(args) - 1
	if n == 0 && !io.In.TTY && (io.In.File != nil || io.In.R != nil) {
		// count lines on stdin
		r := bufio.NewReader(io.In)
		for {
			line, err := r.ReadString('\n')
			if len(line) > 0 {
				n++
			}
			if err != nil {
				break
			}
		}
	}
	fmt.Fprintln(io.Out, n)
	if n == 0 {
		return 1
	}
	return 0
}

func bIsatty(sh *Shell, io *IO, args []string) int {
	fd := 0
	if len(args) > 1 {
		switch args[1] {
		case "stdin":
			fd = 0
		case "stdout":
			fd = 1
		case "stderr":
			fd = 2
		default:
			n, err := strconv.Atoi(args[1])
			if err != nil {
				return 1
			}
			fd = n
		}
	}
	s := io.fd(fd)
	if s == nil || s.File == nil {
		return 1
	}
	if _, err := unix.IoctlGetWinsize(int(s.File.Fd()), unix.TIOCGWINSZ); err != nil {
		return 1
	}
	return 0
}

func bHelp(sh *Shell, io *IO, args []string) int {
	if len(args) > 1 {
		if d := builtinHelp[args[1]]; d != "" {
			fmt.Fprintf(io.Out, "%s - %s\n", args[1], d)
			return 0
		}
	}
	fmt.Fprintln(io.Out, "ash "+Version+" - builtins:")
	names := BuiltinNames()
	w := 0
	for _, n := range names {
		if len(n) > w {
			w = len(n)
		}
	}
	for _, n := range names {
		if strings.HasPrefix(n, "_") {
			continue
		}
		fmt.Fprintf(io.Out, "  %-*s  %s\n", w, n, builtinHelp[n])
	}
	fmt.Fprintln(io.Out, "\nThe docs live in the docs/ folder of the ash repository.")
	return 0
}

func bEnableTransience(sh *Shell, io *IO, args []string) int {
	sh.shared.transient.Store(true)
	return 0
}

func bDisableTransience(sh *Shell, io *IO, args []string) int {
	sh.shared.transient.Store(false)
	return 0
}

// Transient reports whether transient prompts are enabled.
func (sh *Shell) Transient() bool { return sh.shared.transient.Load() }

// SetTransient turns transient prompts on or off.
func (sh *Shell) SetTransient(on bool) { sh.shared.transient.Store(on) }
