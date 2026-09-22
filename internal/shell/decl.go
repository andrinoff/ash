package shell

import (
	"fmt"
	"os"

	"github.com/andrinoff/ash/internal/syntax"
)

// declArray is an array value given to a declaration builtin, as in
// local arr=(a b).
type declArray struct {
	name   string
	vals   []string
	append bool
}

// expandCallArgs expands the arguments of a simple command. The
// assignments given to local, declare, typeset, readonly and export
// (c.DeclArgs) expand like assignments: name=value stays one argument,
// without word splitting or globbing. An array can't be passed as an
// argument, so only its name is, and its value is returned for
// declareArrays.
func (sh *Shell) expandCallArgs(c *syntax.CallExpr, io *IO) ([]string, []declArray, error) {
	if c.DeclArgs == nil {
		args, err := sh.expandFields(c.Args, io, 0)
		return args, nil, err
	}
	var args []string
	var arrays []declArray
	for i, w := range c.Args {
		var a *syntax.Assign
		if i < len(c.DeclArgs) {
			a = c.DeclArgs[i]
		}
		switch {
		case a == nil:
			fs, err := sh.expandWord(w, io, 0)
			if err != nil {
				return nil, nil, err
			}
			args = append(args, fs...)
		case a.IsArray:
			vals, err := sh.expandFields(a.Array, io, 0)
			if err != nil {
				return nil, nil, err
			}
			arrays = append(arrays, declArray{name: a.Name, vals: vals, append: a.Append})
			args = append(args, a.Name)
		default:
			v, err := sh.expandString(a.Value, io)
			if err != nil {
				return nil, nil, err
			}
			op := "="
			if a.Append {
				op = "+="
			}
			args = append(args, a.Name+op+v)
		}
	}
	return args, arrays, nil
}

// declareArrays runs a declaration builtin given arrays, such as
// local arr=(a b): the builtin declares arr, then arr gets its value.
func (sh *Shell) declareArrays(args []string, arrays []declArray, io *IO, tmp []tmpVar, aliasOK bool, extraFds []*os.File) int {
	if _, ok := sh.aliases[args[0]]; ok && aliasOK || sh.lookupFunc(args[0]) != nil {
		return sh.dispatch(args, io, tmp, aliasOK, extraFds)
	}
	before := make([]*Var, len(arrays))
	for i, a := range arrays {
		before[i], _ = sh.lookupVar(a.name)
	}
	status := sh.dispatch(args, io, tmp, aliasOK, extraFds)
	for i, a := range arrays {
		v, where := sh.lookupVar(a.name)
		if v == nil {
			continue
		}
		if v == before[i] && v.ReadOnly {
			fmt.Fprintf(io.Err, "%s: %s: read-only variable\n", args[0], a.name)
			status = 1
			continue
		}
		vals := a.vals
		if a.append {
			vals = append(append([]string(nil), v.Vals...), vals...)
		}
		nv := *v
		nv.Vals = vals
		if nv.Path {
			nv.Vals = splitPathList(vals)
		}
		where.put(a.name, &nv)
		sh.varChanged(a.name)
	}
	return status
}
