package shell

import (
	"bytes"

	"github.com/andrinoff/ash/internal/syntax"
)

// A pipeline of pure builtins, like `string length -- $l | string join +`,
// doesn't need pipes, goroutines and subshells: each element can run in
// turn, in the shell, reading what the one before it printed. With pipes
// the thread handoffs cost about 30µs a pipeline.

// purePipe reports whether pipeline p can run that way.
func (sh *Shell) purePipe(p *syntax.Pipeline) bool {
	if noSubstFastPath || len(p.Cmds) < 2 {
		return false
	}
	for i, cmd := range p.Cmds {
		c, ok := cmd.(*syntax.CallExpr)
		if !ok || !pureCallExpr(c) || sh.hidesBuiltin(c) || p.PipeErr[i] || (p.ErrOnly != nil && p.ErrOnly[i]) {
			return false
		}
	}
	// Nothing else may read the pipeline's input: run one after the other,
	// the first would have to read all of it before the second starts.
	first := p.Cmds[0].(*syntax.CallExpr)
	name, _ := first.Args[0].IsUnquotedLit()
	switch name {
	case "echo", "printf", "math", "true", "false", ":", "test", "[":
		return true
	}
	return len(first.Args) >= 3 // string SUB ARG, path SUB ARG: no stdin
}

// runPurePipe runs a pipeline purePipe accepted, the way runPipe would:
// every element but the last as if in a subshell.
func (sh *Shell) runPurePipe(p *syntax.Pipeline, io *IO) int {
	n := len(p.Cmds)
	ps := make([]int, n)
	in := io.In
	for i, cmd := range p.Cmds {
		eio := io.clone()
		eio.In = in
		if i == n-1 {
			ps[i] = sh.runCommand(cmd, eio)
			break
		}
		buf := &bytes.Buffer{}
		eio.Out = &Stream{W: buf}
		lastArg, lineno, ctrl, exitStatus := sh.lastArg, sh.lineno, sh.ctrl, sh.exitStatus
		st := sh.execCall(cmd.(*syntax.CallExpr), eio)
		if sh.ctrl == ctrlExit && ctrl != ctrlExit {
			st = sh.exitStatus
		}
		sh.lastArg, sh.lineno, sh.ctrl, sh.exitStatus = lastArg, lineno, ctrl, exitStatus
		ps[i] = st
		in = &Stream{R: bytes.NewReader(buf.Bytes())}
		if sh.interrupted() {
			ps[n-1] = 130
			break
		}
	}
	sh.pipestatus = ps
	status := ps[n-1]
	if sh.opts.pipefail {
		for _, s := range ps {
			if s != 0 {
				status = s
			}
		}
	}
	return status
}
