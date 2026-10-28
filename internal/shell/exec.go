package shell

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/andrinoff/ash/internal/syntax"
)

const maxDepth = 1000

// RunString parses and runs src. file names the source for messages.
func (sh *Shell) RunString(src, file string, io *IO) int {
	f, err := parse(src, file)
	if err != nil {
		fmt.Fprint(io.Err, "ash: "+FormatParseError(src, file, err))
		sh.status = 2
		if sh.Interactive {
			sh.status = 127
		}
		return sh.status
	}
	prev := sh.curSrc
	sh.curSrc = src
	defer func() { sh.curSrc = prev }()
	return sh.runTop(f.Stmts, io, file, src, false)
}

// RunFile runs a script file in the current shell (like source).
func (sh *Shell) RunFile(path string, args []string, io *IO) int {
	b, err := os.ReadFile(path)
	if err != nil {
		sh.errorMsg(io.Err, "%s", errText(err))
		return 127
	}
	src := string(b)
	if strings.HasPrefix(src, "#!") {
		// keep line numbers: blank out the shebang line
		if i := strings.IndexByte(src, '\n'); i >= 0 {
			src = strings.Repeat(" ", i) + src[i:]
		} else {
			src = ""
		}
	}
	if sh.prof.p != nil {
		defer sh.profFile(path)()
	}
	f, err := parse(src, path)
	if err != nil {
		fmt.Fprint(io.Err, "ash: "+FormatParseError(src, path, err))
		return 2
	}
	sh.srcStack = append(sh.srcStack, path)
	defer func() { sh.srcStack = sh.srcStack[:len(sh.srcStack)-1] }()
	if args != nil {
		saved, _ := sh.lookupVar("argv")
		sh.global.put("argv", &Var{Vals: args})
		defer func() {
			if saved != nil {
				sh.global.put("argv", saved)
			}
		}()
	}
	prev := sh.curSrc
	sh.curSrc = src
	defer func() { sh.curSrc = prev }()
	status := sh.runTop(f.Stmts, io, path, src, true)
	if sh.ctrl == ctrlReturn {
		sh.ctrl = ctrlNone
		status = sh.status
	}
	return status
}

// ShouldExit reports whether `exit` was run, and with which status.
func (sh *Shell) ShouldExit() (bool, int) {
	return sh.ctrl == ctrlExit, sh.exitStatus
}

// ResetControl clears pending break/continue/return at the top level.
func (sh *Shell) ResetControl() {
	if sh.ctrl != ctrlExit {
		sh.ctrl = ctrlNone
	}
}

func (sh *Shell) runStmts(stmts []*syntax.Stmt, io *IO) int {
	for _, st := range stmts {
		if sh.ctrl != ctrlNone {
			break
		}
		if sh.interrupted() {
			if sh.status == 0 {
				sh.status = 130
			}
			break
		}
		if sh.prof.p != nil {
			sh.profStmt(st, io)
		} else {
			sh.runStmt(st, io)
		}
		if !sh.subshell && sh.shared.traps.has.Load() {
			// run handlers for trapped signals between commands
			sh.runPendingTraps(io)
		}
		if sh.subshell && io.Out != nil && io.Out.broken != nil && io.Out.broken.Load() {
			sh.ctrl = ctrlExit
			sh.exitStatus = 141
		}
	}
	return sh.status
}

func (sh *Shell) runStmt(st *syntax.Stmt, io *IO) {
	switch st.Cond {
	case syntax.CondAnd:
		if sh.status != 0 {
			return
		}
	case syntax.CondOr:
		if sh.status == 0 {
			return
		}
	}
	sh.lineno = st.Pos
	if st.Background {
		sh.runBackground(st, io)
		return
	}
	shortCircuit := false
	if b, ok := st.Cmd.(*syntax.BinaryCmd); ok {
		sh.status, shortCircuit = sh.runBinary(b, io)
	} else {
		sh.status = sh.runCommand(st.Cmd, io)
	}
	if sh.opts.errexit && sh.status != 0 && sh.noErrexit == 0 && !shortCircuit && sh.ctrl == ctrlNone {
		if p, ok := st.Cmd.(*syntax.Pipeline); !ok || !p.Negate {
			sh.ctrl = ctrlExit
			sh.exitStatus = sh.status
		}
	}
}

func (sh *Shell) runBinary(b *syntax.BinaryCmd, io *IO) (int, bool) {
	sh.noErrexit++
	var x int
	if inner, ok := b.X.(*syntax.BinaryCmd); ok {
		x, _ = sh.runBinary(inner, io)
	} else {
		x = sh.runCommand(b.X, io)
	}
	sh.noErrexit--
	sh.status = x
	if sh.ctrl != ctrlNone || sh.interrupted() {
		return x, true
	}
	if (b.Op == syntax.AndOp) != (x == 0) {
		return x, true
	}
	if inner, ok := b.Y.(*syntax.BinaryCmd); ok {
		return sh.runBinary(inner, io)
	}
	return sh.runCommand(b.Y, io), false
}

func (sh *Shell) runCommand(cmd syntax.Command, io *IO) int {
	switch c := cmd.(type) {
	case *syntax.CallExpr:
		return sh.execCall(c, io)
	case *syntax.Pipeline:
		return sh.runPipeline(c, io, false)
	case *syntax.BinaryCmd:
		s, _ := sh.runBinary(c, io)
		return s
	case *syntax.Block:
		return sh.withRedirs(c.Redirs, io, func(io *IO) int {
			if c.Fish {
				return sh.inBlockScope(func() int { return sh.runStmts(c.Stmts, io) })
			}
			return sh.runStmts(c.Stmts, io)
		})
	case *syntax.Subshell:
		return sh.withRedirs(c.Redirs, io, func(io *IO) int {
			sub := sh.clone()
			// its own IO: exec redirections inside must not leak out
			sio := io.clone()
			sub.runStmts(c.Stmts, sio)
			sub.finishSubshell(sio)
			if sub.ctrl == ctrlExit {
				return sub.exitStatus
			}
			return sub.status
		})
	case *syntax.IfClause:
		return sh.withRedirs(c.Redirs, io, func(io *IO) int { return sh.runIf(c, io) })
	case *syntax.WhileClause:
		return sh.withRedirs(c.Redirs, io, func(io *IO) int { return sh.runWhile(c, io) })
	case *syntax.ForClause:
		return sh.withRedirs(c.Redirs, io, func(io *IO) int { return sh.runFor(c, io) })
	case *syntax.CForClause:
		return sh.withRedirs(c.Redirs, io, func(io *IO) int { return sh.runCFor(c, io) })
	case *syntax.CaseClause:
		return sh.withRedirs(c.Redirs, io, func(io *IO) int { return sh.runCase(c, io) })
	case *syntax.FuncDecl:
		return sh.defineFunc(c, io)
	case *syntax.ArithCmd:
		return sh.withRedirs(c.Redirs, io, func(io *IO) int {
			s, err := sh.expandString(c.Expr, io)
			if err != nil {
				sh.errorMsg(io.Err, "%v", err)
				return 1
			}
			n, err := sh.arith(s)
			if err != nil {
				sh.errorMsg(io.Err, "%v", err)
				return 1
			}
			if n != 0 {
				return 0
			}
			return 1
		})
	case *syntax.TestClause:
		return sh.withRedirs(c.Redirs, io, func(io *IO) int {
			ok, err := sh.evalTest(c.Expr, io)
			if err != nil {
				sh.errorMsg(io.Err, "%v", err)
				return 2
			}
			if ok {
				return 0
			}
			return 1
		})
	case *syntax.EnvCmd:
		tmp, err := sh.evalAssigns(c.Assigns, io)
		if err != nil {
			sh.errorMsg(io.Err, "%v", err)
			return 1
		}
		return sh.withTempVars(tmp, func() int { return sh.runCommand(c.Cmd, io) })
	}
	return 0
}

func (sh *Shell) withRedirs(rs []*syntax.Redirect, io *IO, f func(*IO) int) int {
	if len(rs) == 0 {
		return f(io)
	}
	rio, cleanup, err := sh.redirect(rs, io)
	if err != nil {
		cleanup()
		sh.errorMsg(io.Err, "%v", err)
		return 1
	}
	defer cleanup()
	return f(rio)
}

func (sh *Shell) inBlockScope(f func() int) int {
	saved := sh.scope
	sh.scope = newScope(saved, scopeBlock)
	defer func() { sh.scope = saved }()
	return f()
}

func (sh *Shell) runCond(stmts []*syntax.Stmt, io *IO) bool {
	sh.noErrexit++
	sh.runStmts(stmts, io)
	sh.noErrexit--
	return sh.status == 0
}

func (sh *Shell) runIf(c *syntax.IfClause, io *IO) int {
	run := func() int {
		if sh.runCond(c.Cond, io) {
			if sh.ctrl != ctrlNone {
				return sh.status
			}
			return sh.runBody(c.Then, io)
		}
		if sh.ctrl != ctrlNone {
			return sh.status
		}
		for _, e := range c.Elifs {
			if sh.runCond(e.Cond, io) {
				if sh.ctrl != ctrlNone {
					return sh.status
				}
				return sh.runBody(e.Then, io)
			}
			if sh.ctrl != ctrlNone {
				return sh.status
			}
		}
		if c.Else != nil {
			return sh.runBody(c.Else, io)
		}
		return 0
	}
	if c.Fish {
		return sh.inBlockScope(run)
	}
	return run()
}

func (sh *Shell) runBody(stmts []*syntax.Stmt, io *IO) int {
	if len(stmts) == 0 {
		sh.status = 0
		return 0
	}
	return sh.runStmts(stmts, io)
}

// loopControl handles break/continue at the end of a loop iteration.
// It returns true if the loop should stop.
func (sh *Shell) loopControl() bool {
	switch sh.ctrl {
	case ctrlBreak:
		if sh.ctrlLevel > 1 {
			sh.ctrlLevel--
			return true
		}
		sh.ctrl = ctrlNone
		return true
	case ctrlContinue:
		if sh.ctrlLevel > 1 {
			sh.ctrlLevel--
			sh.ctrl = ctrlBreak
			return true
		}
		sh.ctrl = ctrlNone
		return false
	case ctrlReturn, ctrlExit:
		return true
	}
	return sh.interrupted()
}

func (sh *Shell) runWhile(c *syntax.WhileClause, io *IO) int {
	status := 0
	sh.inBlock++
	defer func() { sh.inBlock-- }()
	for {
		ok := sh.runCond(c.Cond, io)
		if sh.ctrl != ctrlNone {
			if sh.loopControl() {
				break
			}
			continue
		}
		if ok == c.Until || sh.interrupted() {
			break
		}
		if c.Fish {
			status = sh.inBlockScope(func() int { return sh.runBody(c.Body, io) })
		} else {
			status = sh.runBody(c.Body, io)
		}
		if sh.loopControl() {
			break
		}
	}
	sh.status = status
	return status
}

func (sh *Shell) runFor(c *syntax.ForClause, io *IO) int {
	var items []string
	if c.HasIn {
		var err error
		items, err = sh.expandFields(c.Items, io, expNullGlob)
		if err != nil {
			sh.errorMsg(io.Err, "%v", err)
			return 1
		}
	} else {
		items = append([]string(nil), sh.args()...)
	}
	mode := setShAuto
	if c.Fish {
		mode = setAuto
	}
	status := 0
	sh.inBlock++
	defer func() { sh.inBlock-- }()
	for _, it := range items {
		if err := sh.SetVar(c.Var, []string{it}, setOpts{scope: mode}); err != nil {
			sh.errorMsg(io.Err, "%v", err)
			return 1
		}
		if c.Fish {
			status = sh.inBlockScope(func() int { return sh.runBody(c.Body, io) })
		} else {
			status = sh.runBody(c.Body, io)
		}
		if sh.loopControl() {
			break
		}
	}
	sh.status = status
	return status
}

func (sh *Shell) runCFor(c *syntax.CForClause, io *IO) int {
	eval := func(w *syntax.Word) (int64, error) {
		s, err := sh.expandString(w, io)
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(s) == "" {
			return 1, nil
		}
		return sh.arith(s)
	}
	if _, err := eval(c.Init); err != nil {
		sh.errorMsg(io.Err, "%v", err)
		return 1
	}
	status := 0
	sh.inBlock++
	defer func() { sh.inBlock-- }()
	for {
		v, err := eval(c.Cond)
		if err != nil {
			sh.errorMsg(io.Err, "%v", err)
			return 1
		}
		if v == 0 || sh.interrupted() {
			break
		}
		status = sh.runBody(c.Body, io)
		if sh.loopControl() {
			break
		}
		if _, err := eval(c.Post); err != nil {
			sh.errorMsg(io.Err, "%v", err)
			return 1
		}
	}
	sh.status = status
	return status
}

func (sh *Shell) runCase(c *syntax.CaseClause, io *IO) int {
	word, err := sh.expandString(c.Word, io)
	if err != nil {
		sh.errorMsg(io.Err, "%v", err)
		return 1
	}
	run := func() int {
		fallthru := false
		for i, item := range c.Items {
			matched := fallthru
			if !matched {
				for _, pw := range item.Patterns {
					var pats []string
					if c.Fish {
						// fish: each pattern may expand to several (a list variable)
						fs, err := sh.expandFields([]*syntax.Word{pw}, io, expNoGlob)
						if err != nil {
							sh.errorMsg(io.Err, "%v", err)
							return 1
						}
						pats = fs
					} else {
						p, err := sh.expandPattern(pw, io, false)
						if err != nil {
							sh.errorMsg(io.Err, "%v", err)
							return 1
						}
						pats = []string{p}
					}
					for _, p := range pats {
						if matchPattern(p, word) {
							matched = true
							break
						}
					}
					if matched {
						break
					}
				}
			}
			if !matched {
				continue
			}
			status := sh.runBody(item.Body, io)
			if c.Fish || sh.ctrl != ctrlNone {
				return status
			}
			switch item.Op {
			case syntax.CaseFall:
				fallthru = true
				if i == len(c.Items)-1 {
					return status
				}
				continue
			case syntax.CaseContinue:
				fallthru = false
				continue
			}
			return status
		}
		return 0
	}
	if c.Fish {
		return sh.inBlockScope(run)
	}
	return run()
}

// evalAssigns expands prefix assignments (VAR=x cmd) into name/value pairs.
type tmpVar struct {
	name string
	vals []string
}

func (sh *Shell) evalAssigns(as []*syntax.Assign, io *IO) ([]tmpVar, error) {
	var out []tmpVar
	for _, a := range as {
		if a.IsArray {
			vals, err := sh.expandFields(a.Array, io, 0)
			if err != nil {
				return nil, err
			}
			out = append(out, tmpVar{a.Name, vals})
			continue
		}
		v, err := sh.expandString(a.Value, io)
		if err != nil {
			return nil, err
		}
		if a.Append {
			v = sh.Get(a.Name) + v
		}
		out = append(out, tmpVar{a.Name, []string{v}})
	}
	return out, nil
}

func (sh *Shell) withTempVars(tmp []tmpVar, f func() int) int {
	if len(tmp) == 0 {
		return f()
	}
	saved := sh.scope
	sh.scope = newScope(saved, scopeBlock)
	for _, t := range tmp {
		v := &Var{Vals: t.vals, Export: true, Path: IsPathVar(t.name)}
		if v.Path {
			v.Vals = splitPathList(v.Vals)
		}
		sh.scope.put(t.name, v)
		sh.varChanged(t.name)
	}
	defer func() {
		sh.scope = saved
		for _, t := range tmp {
			sh.varChanged(t.name)
		}
	}()
	return f()
}

// doAssign performs a standalone assignment (x=1, arr=(a b), arr[2]=x).
func (sh *Shell) doAssign(a *syntax.Assign, io *IO, sc setScope, export bool) error {
	o := setOpts{scope: sc}
	if export {
		o.export = 1
	}
	if a.IsArray {
		vals, err := sh.expandFields(a.Array, io, 0)
		if err != nil {
			return err
		}
		if a.Append {
			vals = append(append([]string(nil), sh.GetList(a.Name)...), vals...)
		}
		o.path = -1
		if IsPathVar(a.Name) {
			o.path = 0
		}
		return sh.SetVar(a.Name, vals, o)
	}
	v, err := sh.expandString(a.Value, io)
	if err != nil {
		return err
	}
	if a.Index != nil {
		is, err := sh.expandString(a.Index, io)
		if err != nil {
			return err
		}
		n, err := sh.arith(is)
		if err != nil {
			return err
		}
		vals := append([]string(nil), sh.GetList(a.Name)...)
		if n < 0 {
			n += int64(len(vals))
		}
		if n < 0 {
			return errorf("%s[%s]: bad array subscript", a.Name, is)
		}
		for int64(len(vals)) <= n {
			vals = append(vals, "")
		}
		if a.Append {
			vals[n] += v
		} else {
			vals[n] = v
		}
		return sh.SetVar(a.Name, vals, o)
	}
	if a.Append {
		cur := sh.GetList(a.Name)
		if len(cur) > 1 && !IsPathVar(a.Name) {
			// x+=y on a list appends to the last element like bash does to [0]... keep it simple: append element
			vals := append(append([]string(nil), cur...), v)
			return sh.SetVar(a.Name, vals, o)
		}
		v = sh.Get(a.Name) + v
	}
	if v == "" && !IsPathVar(a.Name) {
		return sh.SetVar(a.Name, []string{}, o)
	}
	return sh.SetVar(a.Name, []string{v}, o)
}

func (sh *Shell) stmtText(st *syntax.Stmt) string {
	if sh.curSrc != "" && st.Pos >= 0 && st.End <= len(sh.curSrc) && st.Pos < st.End {
		return strings.TrimSpace(sh.curSrc[st.Pos:st.End])
	}
	return "job"
}

// RunTopLevel runs one interactive command line, keeping job text.
func (sh *Shell) RunTopLevel(src string, io *IO) int {
	f, err := parse(src, "")
	if err != nil {
		fmt.Fprint(io.Err, "ash: "+FormatParseError(src, "", err))
		sh.status = 127
		return sh.status
	}
	prev := sh.curSrc
	sh.curSrc = src
	defer func() { sh.curSrc = prev; sh.curStmt = "" }()
	for _, st := range f.Stmts {
		if sh.ctrl != ctrlNone || sh.interrupted() {
			break
		}
		sh.curStmt = sh.stmtText(st)
		sh.runStmt(st, io)
		if sh.shared.traps.has.Load() {
			sh.runPendingTraps(io)
		}
	}
	return sh.status
}

// streamWriter makes io.Writer from a Stream for fmt functions.
var _ io.Writer = (*Stream)(nil)
