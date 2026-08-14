package shell

import (
	"fmt"
	"strconv"
	"strings"
)

var shOptionLetters = "euxfvnCabhmBH"

// isPosixSet decides whether `set` arguments are sh-style options
// (set -e, set -euo pipefail, set -- a b) rather than fish variables.
func isPosixSet(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "--" || args[0] == "-o" || args[0] == "+o" {
		return true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return true
		}
		if strings.HasPrefix(a, "+") && len(a) > 1 {
			if strings.Trim(a[1:], shOptionLetters+"o") == "" {
				if strings.Contains(a, "o") {
					i++
				}
				continue
			}
			return false
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 && !strings.HasPrefix(a, "--") {
			if strings.Trim(a[1:], shOptionLetters+"o") != "" {
				return false
			}
			if strings.Contains(a, "o") {
				i++ // option name follows
			}
			continue
		}
		return false
	}
	return true
}

func (sh *Shell) setShOption(name string, on bool) bool {
	switch name {
	case "errexit", "e":
		sh.opts.errexit = on
	case "nounset", "u":
		sh.opts.nounset = on
	case "xtrace", "x":
		sh.opts.xtrace = on
	case "pipefail":
		sh.opts.pipefail = on
	case "noclobber", "C":
		sh.opts.noclobber = on
	case "noglob", "f":
		sh.opts.noglob = on
	case "verbose", "v":
		sh.opts.verbose = on
	case "vi":
		sh.opts.vi = on
		if sh.Editor != nil {
			if on {
				sh.Editor.SetKeymap("vi")
			} else {
				sh.Editor.SetKeymap("emacs")
			}
		}
	case "emacs":
		sh.opts.vi = !on
		if sh.Editor != nil && on {
			sh.Editor.SetKeymap("emacs")
		}
	case "monitor", "m", "notify", "b", "hashall", "h", "braceexpand", "B", "histexpand", "H", "allexport", "a", "noexec", "n", "interactive-comments", "ignoreeof", "posix":
		// accepted, not implemented
	default:
		return false
	}
	return true
}

func bSetPosix(sh *Shell, io *IO, args []string) int {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			sh.setArgs(args[i+1:])
			return 0
		}
		on := a[0] == '-'
		for _, c := range a[1:] {
			if c == 'o' {
				if i+1 >= len(args) {
					// set -o: list options
					for _, o := range []struct {
						n string
						v bool
					}{{"errexit", sh.opts.errexit}, {"nounset", sh.opts.nounset}, {"xtrace", sh.opts.xtrace}, {"pipefail", sh.opts.pipefail}, {"noclobber", sh.opts.noclobber}, {"noglob", sh.opts.noglob}, {"vi", sh.opts.vi}, {"emacs", !sh.opts.vi}} {
						state := "off"
						if o.v {
							state = "on"
						}
						fmt.Fprintf(io.Out, "%-15s %s\n", o.n, state)
					}
					return 0
				}
				i++
				if !sh.setShOption(args[i], on) {
					fmt.Fprintf(io.Err, "set: %s: unknown option name\n", args[i])
					return 2
				}
				continue
			}
			sh.setShOption(string(c), on)
		}
	}
	return 0
}

func (sh *Shell) setArgs(vals []string) {
	_, where := sh.lookupVar("argv")
	if where == nil {
		where = sh.funcScope()
	}
	where.put("argv", &Var{Vals: append([]string(nil), vals...)})
}

// bSet is fish's set, falling back to sh's set for option syntax.
func bSet(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	if isPosixSet(a) {
		return bSetPosix(sh, io, a)
	}
	o := setOpts{scope: setAuto}
	var (
		erase, query, names, long, show, appendM, prepend bool
		scoped                                            bool
	)
	i := 0
	for ; i < len(a); i++ {
		arg := a[i]
		if arg == "--" {
			i++
			break
		}
		if !strings.HasPrefix(arg, "-") || len(arg) < 2 {
			break
		}
		if strings.HasPrefix(arg, "--") {
			switch arg {
			case "--local":
				o.scope, scoped = setLocal, true
			case "--global":
				o.scope, scoped = setGlobal, true
			case "--universal":
				o.scope, scoped = setUniversal, true
			case "--function":
				o.scope, scoped = setFunction, true
			case "--export":
				o.export = 1
			case "--unexport":
				o.export = -1
			case "--erase":
				erase = true
			case "--query":
				query = true
			case "--names":
				names = true
			case "--long":
				long = true
			case "--show":
				show = true
			case "--append":
				appendM = true
			case "--prepend":
				prepend = true
			case "--path":
				o.path = 1
			case "--unpath":
				o.path = -1
			default:
				fmt.Fprintf(io.Err, "set: unknown option %s\n", arg)
				return 2
			}
			continue
		}
		if isNegativeNumber(arg) {
			break
		}
		for _, c := range arg[1:] {
			switch c {
			case 'l':
				o.scope, scoped = setLocal, true
			case 'g':
				o.scope, scoped = setGlobal, true
			case 'U':
				o.scope, scoped = setUniversal, true
			case 'f':
				o.scope, scoped = setFunction, true
			case 'x':
				o.export = 1
			case 'u':
				o.export = -1
			case 'e':
				erase = true
			case 'q':
				query = true
			case 'n':
				names = true
			case 'L':
				long = true
			case 'S':
				show = true
			case 'a':
				appendM = true
			case 'p':
				prepend = true
			default:
				fmt.Fprintf(io.Err, "set: unknown option -%c\n", c)
				return 2
			}
		}
	}
	rest := a[i:]
	_ = long
	if query {
		missing := 0
		for _, n := range rest {
			name, idx, err := splitVarIndex(n)
			if err != nil {
				missing++
				continue
			}
			vals, ok := sh.queryVar(name, o.scope, scoped)
			if !ok {
				missing++
				continue
			}
			if idx != "" {
				if sel, err := selectIndex(vals, idx); err != nil || len(sel) == 0 {
					missing++
				}
			}
		}
		return missing
	}
	if show {
		for _, n := range rest {
			sh.showVar(io, n)
		}
		if len(rest) == 0 {
			for _, n := range sh.VarNames() {
				sh.showVar(io, n)
			}
		}
		return 0
	}
	if erase {
		status := 0
		for _, n := range rest {
			name, idx, err := splitVarIndex(n)
			if err != nil {
				fmt.Fprintf(io.Err, "set: %v\n", err)
				status = 1
				continue
			}
			sc := o.scope
			if !scoped {
				sc = setAuto
			}
			if idx != "" {
				vals := sh.GetList(name)
				keep, err := eraseIndices(vals, idx)
				if err != nil {
					fmt.Fprintf(io.Err, "set: %v\n", err)
					status = 1
					continue
				}
				sh.SetVar(name, keep, setOpts{scope: setAuto})
				continue
			}
			if err := sh.UnsetVar(name, sc); err != nil {
				status = 4
			}
		}
		return status
	}
	if len(rest) == 0 {
		// list variables
		for _, n := range sh.VarNames() {
			v, _ := sh.lookupVar(n)
			if o.export == 1 && (v == nil || !v.Export) {
				continue
			}
			if o.export == -1 && v != nil && v.Export {
				continue
			}
			if names {
				fmt.Fprintln(io.Out, n)
				continue
			}
			vals, _ := sh.getVar(n)
			q := make([]string, len(vals))
			for k, x := range vals {
				q[k] = Quote(x)
			}
			fmt.Fprintf(io.Out, "%s %s\n", n, strings.Join(q, " "))
		}
		return 0
	}
	name, idx, err := splitVarIndex(rest[0])
	if err != nil {
		fmt.Fprintf(io.Err, "set: %v\n", err)
		return 2
	}
	if !validVarName(name) {
		fmt.Fprintf(io.Err, "set: %s: invalid variable name\n", name)
		return 2
	}
	vals := rest[1:]
	if !scoped {
		o.scope = setAuto
	}
	if idx != "" {
		cur := sh.GetList(name)
		nv, err := assignIndices(cur, idx, vals)
		if err != nil {
			fmt.Fprintf(io.Err, "set: %v\n", err)
			return 2
		}
		vals = nv
	} else if appendM || prepend {
		cur := sh.GetList(name)
		if appendM {
			vals = append(append([]string(nil), cur...), vals...)
		} else {
			vals = append(append([]string(nil), vals...), cur...)
		}
	}
	if err := sh.SetVar(name, append([]string{}, vals...), o); err != nil {
		fmt.Fprintf(io.Err, "set: %v\n", err)
		return 1
	}
	return sh.status
}

func isNegativeNumber(s string) bool {
	if len(s) < 2 || s[0] != '-' {
		return false
	}
	_, err := strconv.Atoi(s[1:])
	return err == nil
}

func (sh *Shell) queryVar(name string, sc setScope, scoped bool) ([]string, bool) {
	if !scoped {
		return sh.getVar(name)
	}
	var s *scope
	switch sc {
	case setGlobal, setUniversal:
		s = sh.global
	case setLocal:
		s = sh.scope
	case setFunction:
		s = sh.funcScope()
	}
	if v := s.get(name); v != nil {
		return v.Vals, true
	}
	return nil, false
}

func (sh *Shell) showVar(io *IO, name string) {
	v, where := sh.lookupVar(name)
	if v == nil {
		vals, ok := sh.specialVar(name)
		if !ok {
			return
		}
		fmt.Fprintf(io.Out, "$%s: set in global scope, unexported, with %d elements\n", name, len(vals))
		for i, x := range vals {
			fmt.Fprintf(io.Out, "$%s[%d]: |%s|\n", name, i+1, x)
		}
		return
	}
	scopeName := "global"
	if where != sh.global {
		scopeName = "local"
	}
	exp := "unexported"
	if v.Export {
		exp = "exported"
	}
	fmt.Fprintf(io.Out, "$%s: set in %s scope, %s, with %d elements\n", name, scopeName, exp, len(v.Vals))
	for i, x := range v.Vals {
		fmt.Fprintf(io.Out, "$%s[%d]: |%s|\n", name, i+1, x)
	}
}

// splitVarIndex splits "name[1..2]" into name and index text.
func splitVarIndex(s string) (string, string, error) {
	i := strings.IndexByte(s, '[')
	if i < 0 {
		return s, "", nil
	}
	if !strings.HasSuffix(s, "]") {
		return "", "", errorf("%s: invalid index", s)
	}
	return s[:i], s[i+1 : len(s)-1], nil
}

func parseIndexList(idx string, n int) ([]int, error) {
	var out []int
	for _, one := range strings.Fields(idx) {
		if i := strings.Index(one, ".."); i >= 0 {
			a, b := one[:i], one[i+2:]
			start, end := 1, n
			var err error
			if a != "" {
				if start, err = strconv.Atoi(a); err != nil {
					return nil, errorf("invalid index %q", one)
				}
			}
			if b != "" {
				if end, err = strconv.Atoi(b); err != nil {
					return nil, errorf("invalid index %q", one)
				}
			}
			if start < 0 {
				start += n + 1
			}
			if end < 0 {
				end += n + 1
			}
			if start <= end {
				for k := start; k <= end; k++ {
					out = append(out, k)
				}
			} else {
				for k := start; k >= end; k-- {
					out = append(out, k)
				}
			}
			continue
		}
		v, err := strconv.Atoi(one)
		if err != nil {
			return nil, errorf("invalid index %q", one)
		}
		if v < 0 {
			v += n + 1
		}
		out = append(out, v)
	}
	return out, nil
}

func selectIndex(vals []string, idx string) ([]string, error) {
	ix, err := parseIndexList(idx, len(vals))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, i := range ix {
		if i >= 1 && i <= len(vals) {
			out = append(out, vals[i-1])
		}
	}
	return out, nil
}

func eraseIndices(vals []string, idx string) ([]string, error) {
	ix, err := parseIndexList(idx, len(vals))
	if err != nil {
		return nil, err
	}
	drop := map[int]bool{}
	for _, i := range ix {
		drop[i] = true
	}
	var out []string
	for i, v := range vals {
		if !drop[i+1] {
			out = append(out, v)
		}
	}
	return out, nil
}

func assignIndices(cur []string, idx string, vals []string) ([]string, error) {
	ix, err := parseIndexList(idx, len(cur))
	if err != nil {
		return nil, err
	}
	if len(ix) != len(vals) {
		return nil, errorf("given %d indexes but %d values", len(ix), len(vals))
	}
	out := append([]string(nil), cur...)
	for k, i := range ix {
		if i < 1 {
			return nil, errorf("array index out of bounds")
		}
		for len(out) < i {
			out = append(out, "")
		}
		out[i-1] = vals[k]
	}
	return out, nil
}
