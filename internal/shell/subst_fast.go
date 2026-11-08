package shell

import (
	"strings"

	"github.com/andrinoff/ash/internal/syntax"
)

// A command substitution runs in a subshell, a clone of the shell. For
// the commonest ones, `(math $i + 1)` or `$(printf %s "$x")`, the clone
// is most of the work, and it buys nothing: the command can't change
// the shell. Those run in the shell itself, with their output captured.

// pureBuiltins only read the shell's state, so running one in the
// shell itself or in a subshell comes to the same thing.
var pureBuiltins = map[string]bool{
	"true": true, "false": true, ":": true,
	"echo": true, "printf": true,
	"math": true, "string": true, "path": true,
	"test": true, "[": true,
	"count": true, "contains": true, "realpath": true,
}

// noSubstFastPath turns the fast path off (tests compare the two).
var noSubstFastPath = false

// pureCall returns the command if stmts is one call of a pure builtin
// whose arguments can be expanded without side effects. Whether a
// function, alias or plugin command hides the builtin is checked when it
// runs.
func pureCall(stmts []*syntax.Stmt) *syntax.CallExpr {
	if noSubstFastPath || len(stmts) != 1 {
		return nil
	}
	st := stmts[0]
	if st.Background || st.Cond != syntax.CondNone {
		return nil
	}
	c, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || !pureCallExpr(c) {
		return nil
	}
	return c
}

// pureCallExpr reports whether c calls a pure builtin, without
// redirections or assignments, with arguments that expand quietly.
func pureCallExpr(c *syntax.CallExpr) bool {
	if len(c.Assigns) != 0 || len(c.Redirs) != 0 || len(c.Args) == 0 || c.DeclArgs != nil {
		return false
	}
	name, ok := c.Args[0].IsUnquotedLit()
	if !ok || !pureBuiltins[name] {
		return false
	}
	// printf -v NAME sets a variable
	if name == "printf" && len(c.Args) > 1 && !startsPlain(c.Args[1]) {
		return false
	}
	for _, w := range c.Args {
		if !quietWord(w) {
			return false
		}
	}
	return true
}

// hidesBuiltin reports whether a function or alias named like the pure
// builtin c calls would run instead.
func (sh *Shell) hidesBuiltin(c *syntax.CallExpr) bool {
	name, _ := c.Args[0].IsUnquotedLit()
	if _, alias := sh.aliases[name]; alias || sh.lookupFunc(name) != nil {
		return true
	}
	return builtins[name] == nil
}

// startsPlain reports whether a word starts with literal text that isn't
// an option.
func startsPlain(w *syntax.Word) bool {
	if len(w.Parts) == 0 {
		return true
	}
	switch p := w.Parts[0].(type) {
	case *syntax.Lit:
		return !strings.HasPrefix(p.Value, "-")
	case *syntax.Quoted:
		return !strings.HasPrefix(p.Value, "-")
	case *syntax.DblQuoted:
		if len(p.Parts) == 0 {
			return true
		}
		l, ok := p.Parts[0].(*syntax.Lit)
		return ok && !strings.HasPrefix(l.Value, "-")
	}
	return false
}

// quietWord reports whether expanding w can't change the shell: no
// ${x:=y}, no arithmetic (which can assign), no ${x:?} (which exits).
// Command substitutions inside are fine; they run in subshells of
// their own.
func quietWord(w *syntax.Word) bool {
	if w == nil {
		return true
	}
	return quietParts(w.Parts)
}

func quietParts(parts []syntax.WordPart) bool {
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit, *syntax.Quoted, *syntax.Tilde, *syntax.CmdSubst:
		case *syntax.DblQuoted:
			if !quietParts(p.Parts) {
				return false
			}
		case *syntax.BraceExp:
			for _, alt := range p.Alts {
				if !quietParts(alt) {
					return false
				}
			}
		case *syntax.ParamExp:
			switch p.Op {
			case syntax.OpAssign, syntax.OpAssignUnset, syntax.OpError, syntax.OpErrorUnset, syntax.OpBad:
				return false
			}
			if p.Index != nil {
				if !p.Index.Fish && p.Index.Expr != nil && !p.Index.All {
					return false // a bash subscript is arithmetic
				}
				for _, w := range p.Index.Words {
					if !quietWord(w) {
						return false
					}
				}
			}
			if !quietWord(p.Arg) || !quietWord(p.Arg2) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// substPure runs the substitution c in the shell itself, the way
// cmdSubst would run it in a subshell. ok is false if c isn't the
// builtin after all (a function or alias of that name).
func (sh *Shell) substPure(c *syntax.CallExpr, io *IO) (out string, ok bool, err error) {
	if sh.hidesBuiltin(c) {
		return "", false, nil
	}
	sh.substCount++
	// what running a command changes, and a subshell would have kept
	lastArg, lineno, ctrl, exitStatus := sh.lastArg, sh.lineno, sh.ctrl, sh.exitStatus
	buf := &lockedBuffer{}
	subIO := &IO{In: io.In, Out: &Stream{W: buf}, Err: io.Err}
	sh.inSubst++
	status := sh.execCall(c, subIO)
	sh.inSubst--
	if sh.ctrl == ctrlExit && ctrl != ctrlExit {
		status = sh.exitStatus // set -u: the subshell would have exited
	}
	sh.lastArg, sh.lineno, sh.ctrl, sh.exitStatus = lastArg, lineno, ctrl, exitStatus
	sh.status = status
	if sh.interrupted() {
		return "", true, errInterrupted
	}
	return buf.String(), true, nil
}
