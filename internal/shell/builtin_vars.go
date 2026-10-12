package shell

import (
	"fmt"
	"strconv"
	"strings"
)

func init() {
	builtins["set"] = bSet
	builtins["export"] = bExport
	builtins["local"] = bLocal
	builtins["declare"] = bDeclare
	builtins["typeset"] = bDeclare
	builtins["readonly"] = bReadonly
	builtins["unset"] = bUnset
	builtins["read"] = bRead
	for k, v := range map[string]string{
		"set":      "set or list variables (fish style), or shell options (sh style)",
		"export":   "export variables to child processes",
		"local":    "declare function-local variables",
		"declare":  "declare variables",
		"typeset":  "declare variables",
		"readonly": "make variables read-only",
		"unset":    "remove variables or functions",
		"read":     "read a line into variables",
	} {
		builtinHelp[k] = v
	}
}

// declArgs handles name=value / name arguments for export, local, etc.
func (sh *Shell) declArgs(io *IO, cmd string, args []string, o setOpts) int {
	status := 0
	for _, a := range args {
		name, val, hasVal := strings.Cut(a, "=")
		app := false
		if strings.HasSuffix(name, "+") && hasVal {
			name = name[:len(name)-1]
			app = true
		}
		if !validVarName(name) {
			fmt.Fprintf(io.Err, "%s: '%s': not a valid identifier\n", cmd, a)
			status = 1
			continue
		}
		if !hasVal {
			// Like bash, a name declared without a value (local x,
			// export x, declare x) stays unset until it is assigned:
			// ${x-default} gives default and export x puts nothing in
			// the environment.
			v, where := sh.lookupVar(name)
			switch {
			case o.scope == setFunction || o.scope == setLocal:
				// local x: a new unset local (keeping the value if already local)
				target := sh.funcScope()
				if where == target && v != nil {
					break
				}
				lo := o
				lo.unset = true
				sh.SetVar(name, []string{}, lo)
			case v != nil:
				sh.SetVar(name, v.Vals, setOpts{scope: setShAuto, export: o.export, readOnly: o.readOnly, unset: v.unset})
			default:
				sh.SetVar(name, []string{}, setOpts{scope: setGlobal, export: o.export, readOnly: o.readOnly, unset: true})
			}
			continue
		}
		if app {
			val = sh.Get(name) + val
		}
		var vals []string
		if val != "" || IsPathVar(name) {
			vals = []string{val}
		} else {
			vals = []string{}
		}
		if err := sh.SetVar(name, vals, o); err != nil {
			fmt.Fprintf(io.Err, "%s: %v\n", cmd, err)
			status = 1
		}
	}
	return status
}

// declValue is "=value" for listings, or nothing for a variable that
// was declared without a value.
func declValue(v *Var) string {
	if v.unset {
		return ""
	}
	return "=" + Quote(v.String())
}

func bExport(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	unexport := false
	for len(a) > 0 && strings.HasPrefix(a[0], "-") {
		switch a[0] {
		case "-n":
			unexport = true
		case "-p":
		case "--":
		default:
			fmt.Fprintf(io.Err, "export: unknown option %s\n", a[0])
			return 2
		}
		a = a[1:]
	}
	if len(a) == 0 {
		names := sh.VarNames()
		for _, n := range names {
			v, _ := sh.lookupVar(n)
			if v != nil && v.Export {
				fmt.Fprintln(io.Out, "export "+n+declValue(v))
			}
		}
		return 0
	}
	if unexport {
		for _, n := range a {
			if v, _ := sh.lookupVar(n); v != nil {
				sh.SetVar(n, v.Vals, setOpts{scope: setShAuto, export: -1})
			}
		}
		return 0
	}
	return sh.declArgs(io, "export", a, setOpts{scope: setShAuto, export: 1})
}

func bLocal(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	o := setOpts{scope: setFunction}
	for len(a) > 0 && strings.HasPrefix(a[0], "-") {
		for _, c := range a[0][1:] {
			switch c {
			case 'x':
				o.export = 1
			case 'r':
				o.readOnly = true
			}
		}
		a = a[1:]
	}
	return sh.declArgs(io, "local", a, o)
}

func bReadonly(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	for len(a) > 0 && strings.HasPrefix(a[0], "-") {
		a = a[1:]
	}
	if len(a) == 0 {
		for _, n := range sh.VarNames() {
			if v, _ := sh.lookupVar(n); v != nil && v.ReadOnly {
				fmt.Fprintln(io.Out, "readonly "+n+declValue(v))
			}
		}
		return 0
	}
	return sh.declArgs(io, "readonly", a, setOpts{scope: setShAuto, readOnly: true})
}

func bDeclare(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	o := setOpts{scope: setShAuto}
	if sh.inFunction() {
		o.scope = setFunction
	}
	print, funcs := false, false
	for len(a) > 0 && (strings.HasPrefix(a[0], "-") || strings.HasPrefix(a[0], "+")) && len(a[0]) > 1 {
		on := a[0][0] == '-'
		for _, c := range a[0][1:] {
			switch c {
			case 'x':
				if on {
					o.export = 1
				} else {
					o.export = -1
				}
			case 'r':
				o.readOnly = on
			case 'g':
				o.scope = setGlobal
			case 'p':
				print = true
			case 'f', 'F':
				funcs = true
			case 'a', 'A', 'i', 'l', 'u', 'n', 't':
			}
		}
		a = a[1:]
	}
	if funcs {
		if len(a) == 0 {
			for _, n := range sh.FunctionNames(true) {
				fmt.Fprintln(io.Out, n)
			}
			return 0
		}
		status := 0
		for _, n := range a {
			if fn := sh.lookupFunc(n); fn != nil {
				fmt.Fprintln(io.Out, fn.Src)
			} else {
				status = 1
			}
		}
		return status
	}
	if print || len(a) == 0 {
		names := a
		if len(names) == 0 {
			names = sh.VarNames()
		}
		for _, n := range names {
			v, _ := sh.lookupVar(n)
			if v == nil {
				continue
			}
			flags := "-"
			if v.Export {
				flags += "x"
			}
			if v.ReadOnly {
				flags += "r"
			}
			if flags == "-" {
				flags = "--"
			}
			if len(v.Vals) > 1 && !v.Path {
				q := make([]string, len(v.Vals))
				for i, x := range v.Vals {
					q[i] = Quote(x)
				}
				fmt.Fprintf(io.Out, "declare -a %s=(%s)\n", n, strings.Join(q, " "))
				continue
			}
			fmt.Fprintf(io.Out, "declare %s %s%s\n", flags, n, declValue(v))
		}
		return 0
	}
	return sh.declArgs(io, "declare", a, o)
}

func bUnset(sh *Shell, io *IO, args []string) int {
	funcs := false
	status := 0
	for _, n := range args[1:] {
		switch n {
		case "-f":
			funcs = true
			continue
		case "-v":
			funcs = false
			continue
		}
		if funcs {
			if !sh.EraseFunction(n) {
				status = 1
			}
			continue
		}
		name, idx, err := splitVarIndex(n)
		if err == nil && idx != "" {
			vals := sh.GetList(name)
			if i, err := strconv.Atoi(idx); err == nil {
				if i < 0 {
					i += len(vals)
				}
				if i >= 0 && i < len(vals) {
					nv := append(append([]string(nil), vals[:i]...), vals[i+1:]...)
					sh.SetVar(name, nv, setOpts{scope: setShAuto})
				}
			}
			continue
		}
		if err := sh.UnsetVar(name, setShAuto); err != nil && err != errNotSet {
			fmt.Fprintf(io.Err, "unset: %v\n", err)
			status = 1
		} else if err == errNotSet && sh.lookupFunc(name) != nil && !sh.IsSet(name) {
			// bash: unset with no -v also removes functions when no variable exists
			sh.EraseFunction(name)
		}
	}
	return status
}
