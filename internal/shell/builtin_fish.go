package shell

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func init() {
	builtins["status"] = bStatus
	builtins["functions"] = bFunctions
	builtins["random"] = bRandom
	builtins["abbr"] = bAbbr
	builtins["history"] = bHistory
	builtins["commandline"] = bCommandline
	builtins["bind"] = bBind
	builtins["funcsave"] = bFuncsave
	builtins["fish_config"] = bFishConfig
	builtins["vared"] = bVared
	for k, v := range map[string]string{
		"status":      "query shell state (is-interactive, current-filename, ...)",
		"functions":   "list, show or erase functions",
		"random":      "generate random numbers or pick a random item",
		"abbr":        "manage abbreviations",
		"history":     "show or edit command history",
		"commandline": "read or change the command line being edited",
		"bind":        "bind keys to commands",
		"funcsave":    "save a function to the functions directory",
		"fish_config": "fish compatibility: theme choose",
		"vared":       "edit a variable interactively",
	} {
		builtinHelp[k] = v
	}
}

func bStatus(sh *Shell, io *IO, args []string) int {
	if len(args) < 2 {
		if sh.Login {
			fmt.Fprintln(io.Out, "This is a login shell")
		} else {
			fmt.Fprintln(io.Out, "This is not a login shell")
		}
		if sh.JobControl {
			fmt.Fprintln(io.Out, "Job control: enabled")
		}
		return 0
	}
	b2s := func(b bool) int {
		if b {
			return 0
		}
		return 1
	}
	switch strings.TrimPrefix(args[1], "--") {
	case "is-interactive", "i":
		return b2s(sh.Interactive)
	case "is-login", "l":
		return b2s(sh.Login)
	case "is-command-substitution", "c":
		return b2s(sh.inSubst > 0)
	case "is-block", "b":
		return b2s(sh.inBlock > 0)
	case "is-breakpoint":
		return 1
	case "is-full-job-control":
		return b2s(sh.JobControl)
	case "is-interactive-job-control":
		return b2s(sh.JobControl && sh.Interactive)
	case "is-no-job-control":
		return b2s(!sh.JobControl)
	case "is-interactive-read":
		return 1
	case "current-filename", "filename", "f":
		if len(sh.srcStack) > 0 {
			fmt.Fprintln(io.Out, sh.srcStack[len(sh.srcStack)-1])
		} else if len(sh.callStack) > 0 && sh.callStack[len(sh.callStack)-1].file != "" {
			fmt.Fprintln(io.Out, sh.callStack[len(sh.callStack)-1].file)
		} else {
			fmt.Fprintln(io.Out, "Standard input")
		}
		return 0
	case "dirname", "current-dirname":
		if len(sh.srcStack) > 0 {
			fmt.Fprintln(io.Out, filepath.Dir(sh.srcStack[len(sh.srcStack)-1]))
		} else {
			fmt.Fprintln(io.Out, ".")
		}
		return 0
	case "basename", "current-basename":
		if len(sh.srcStack) > 0 {
			fmt.Fprintln(io.Out, filepath.Base(sh.srcStack[len(sh.srcStack)-1]))
		} else {
			fmt.Fprintln(io.Out, "Standard input")
		}
		return 0
	case "current-function", "function", "u":
		if len(sh.callStack) > 0 {
			fmt.Fprintln(io.Out, sh.callStack[len(sh.callStack)-1].name)
		} else {
			fmt.Fprintln(io.Out, "Not a function")
		}
		return 0
	case "current-line-number", "line-number", "n":
		line := 1
		if sh.curSrc != "" && sh.lineno <= len(sh.curSrc) {
			line = 1 + strings.Count(sh.curSrc[:sh.lineno], "\n")
		}
		fmt.Fprintln(io.Out, line)
		return 0
	case "current-command":
		if len(sh.callStack) > 0 {
			fmt.Fprintln(io.Out, sh.callStack[len(sh.callStack)-1].name)
		} else {
			fmt.Fprintln(io.Out, "ash")
		}
		return 0
	case "current-commandline":
		fmt.Fprintln(io.Out, sh.curStmt)
		return 0
	case "fish-path", "ash-path":
		fmt.Fprintln(io.Out, sh.Self())
		return 0
	case "job-control":
		if len(args) > 2 {
			switch args[2] {
			case "full", "interactive":
				sh.JobControl = sh.shared.ttyFd >= 0
			case "none":
				sh.JobControl = false
			}
		}
		return 0
	case "features":
		fmt.Fprintln(io.Out, "qmark-noglob   off")
		fmt.Fprintln(io.Out, "regex-easyesc  on")
		return 0
	case "test-feature":
		return 1
	case "print-stack-trace", "stack-trace", "t":
		for i := len(sh.callStack) - 1; i >= 0; i-- {
			fmt.Fprintf(io.Out, "in function '%s'\n", sh.callStack[i].name)
		}
		return 0
	case "buildinfo":
		fmt.Fprintf(io.Out, "ash %s\n", Version)
		return 0
	case "terminal":
		fmt.Fprintln(io.Out, sh.Get("TERM"))
		return 0
	case "list-files", "get-file":
		return 1
	}
	fmt.Fprintf(io.Err, "status: unknown subcommand '%s'\n", args[1])
	return 2
}

func bFunctions(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	all, erase, query, names, details, verbose := false, false, false, false, false, false
	desc := ""
	hasDesc := false
	copyMode := false
	for len(a) > 0 && strings.HasPrefix(a[0], "-") && len(a[0]) > 1 {
		o := a[0]
		a = a[1:]
		switch o {
		case "--":
			goto done
		case "-a", "--all":
			all = true
		case "-e", "--erase":
			erase = true
		case "-q", "--query":
			query = true
		case "-n", "--names":
			names = true
		case "-D", "--details":
			details = true
		case "-v", "--verbose":
			verbose = true
		case "-c", "--copy":
			copyMode = true
		case "-d", "--description":
			if len(a) > 0 {
				desc, hasDesc = a[0], true
				a = a[1:]
			}
		case "-H", "--handlers":
			ev := sh.shared.events
			ev.mu.Lock()
			for k, v := range ev.events {
				for _, f := range v {
					fmt.Fprintf(io.Out, "event %s %s\n", k, f)
				}
			}
			for k, v := range ev.vars {
				for _, f := range v {
					fmt.Fprintf(io.Out, "variable %s %s\n", k, f)
				}
			}
			ev.mu.Unlock()
			return 0
		default:
			if strings.HasPrefix(o, "--description=") {
				desc, hasDesc = strings.TrimPrefix(o, "--description="), true
				continue
			}
			fmt.Fprintf(io.Err, "functions: unknown option %s\n", o)
			return 2
		}
	}
done:
	_ = verbose
	if copyMode {
		if len(a) != 2 {
			fmt.Fprintln(io.Err, "functions --copy: expected two names")
			return 2
		}
		fn := sh.lookupFunc(a[0])
		if fn == nil {
			fmt.Fprintf(io.Err, "functions: function '%s' does not exist\n", a[0])
			return 1
		}
		cp := *fn
		cp.Name = a[1]
		cp.OnEvent, cp.OnVariable, cp.OnSignal = nil, nil, nil
		sh.setFunc(a[1], &cp)
		return 0
	}
	if erase {
		for _, n := range a {
			sh.EraseFunction(n)
		}
		return 0
	}
	if query {
		for _, n := range a {
			if sh.lookupFunc(n) == nil {
				return 1
			}
		}
		if len(a) == 0 {
			return 1
		}
		return 0
	}
	if hasDesc {
		if len(a) != 1 {
			return 2
		}
		fn := sh.lookupFunc(a[0])
		if fn == nil {
			return 1
		}
		cp := *fn
		cp.Description = desc
		sh.setFunc(a[0], &cp)
		return 0
	}
	if details {
		for _, n := range a {
			fn := sh.lookupFunc(n)
			if fn == nil {
				fmt.Fprintln(io.Out, "n/a")
				continue
			}
			if fn.File != "" {
				fmt.Fprintln(io.Out, fn.File)
			} else {
				fmt.Fprintln(io.Out, "stdin")
			}
		}
		return 0
	}
	if len(a) == 0 || names {
		list := sh.FunctionNames(all)
		if names {
			fmt.Fprintln(io.Out, strings.Join(list, ", "))
			return 0
		}
		for _, n := range list {
			fmt.Fprintln(io.Out, n)
		}
		return 0
	}
	status := 0
	for i, n := range a {
		fn := sh.lookupFunc(n)
		if fn == nil {
			fmt.Fprintf(io.Err, "functions: no function '%s'\n", n)
			status = 1
			continue
		}
		if i > 0 {
			fmt.Fprintln(io.Out)
		}
		if fn.File != "" {
			fmt.Fprintf(io.Out, "# Defined in %s\n", fn.File)
		}
		fmt.Fprintln(io.Out, strings.TrimRight(fn.Src, "\n"))
	}
	return status
}

func bRandom(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	if len(a) > 0 && a[0] == "choice" {
		if len(a) < 2 {
			fmt.Fprintln(io.Err, "random: nothing to choose from")
			return 2
		}
		fmt.Fprintln(io.Out, a[1+sh.rand.Intn(len(a)-1)])
		return 0
	}
	nums := make([]int64, len(a))
	for i, x := range a {
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			fmt.Fprintf(io.Err, "random: %s: invalid integer\n", x)
			return 2
		}
		nums[i] = n
	}
	var lo, hi, step int64 = 0, 32767, 1
	switch len(nums) {
	case 0:
	case 1:
		sh.rand.Seed(nums[0])
		return 0
	case 2:
		lo, hi = nums[0], nums[1]
	case 3:
		lo, step, hi = nums[0], nums[1], nums[2]
	default:
		fmt.Fprintln(io.Err, "random: too many arguments")
		return 2
	}
	if hi < lo || step <= 0 {
		fmt.Fprintln(io.Err, "random: invalid range")
		return 2
	}
	// Work in uint64: hi-lo can be up to 2^64-1, which overflows int64
	// (random 0 9223372036854775807 used to panic in Int63n).
	steps := (uint64(hi) - uint64(lo)) / uint64(step)
	var k uint64
	if steps == math.MaxUint64 {
		k = sh.rand.Uint64()
	} else {
		k = randUint64n(sh.rand, steps+1)
	}
	fmt.Fprintln(io.Out, int64(uint64(lo)+k*uint64(step)))
	return 0
}

// randUint64n returns a uniform random number in [0, n), n > 0.
func randUint64n(r *rand.Rand, n uint64) uint64 {
	if n <= math.MaxInt64 {
		return uint64(r.Int63n(int64(n)))
	}
	for {
		if v := r.Uint64(); v < n {
			return v
		}
	}
}

// Abbreviations.

func bFuncsave(sh *Shell, io *IO, args []string) int {
	status := 0
	for _, n := range args[1:] {
		if strings.HasPrefix(n, "-") {
			continue
		}
		fn := sh.lookupFunc(n)
		if fn == nil {
			fmt.Fprintf(io.Err, "funcsave: no function '%s'\n", n)
			status = 1
			continue
		}
		dir := filepath.Join(sh.ConfigDir(), "functions")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(io.Err, "funcsave: %v\n", err)
			return 1
		}
		p := filepath.Join(dir, n+".ash")
		if err := os.WriteFile(p, []byte(strings.TrimRight(fn.Src, "\n")+"\n"), 0o644); err != nil {
			fmt.Fprintf(io.Err, "funcsave: %v\n", err)
			status = 1
			continue
		}
		fmt.Fprintf(io.Err, "funcsave: wrote %s\n", p)
	}
	return status
}

func bFishConfig(sh *Shell, io *IO, args []string) int {
	if len(args) >= 4 && args[1] == "theme" && (args[2] == "choose" || args[2] == "save") {
		return bTheme(sh, io, []string{"theme", "choose", args[3]})
	}
	return 0
}

func bVared(sh *Shell, io *IO, args []string) int {
	if len(args) < 2 || sh.shared.readLine == nil {
		return 1
	}
	name := args[1]
	if sh.Editor != nil {
		sh.Editor.SetBuffer(sh.Get(name), len(sh.Get(name)))
	}
	v, err := sh.shared.readLine(name + "> ")
	if err != nil {
		return 1
	}
	sh.SetVar(name, []string{v}, setOpts{scope: setAuto})
	return 0
}
