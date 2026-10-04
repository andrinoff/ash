package shell

import (
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/andrinoff/ash/internal/syntax"
)

// runPipeline runs a | b | c.
func (sh *Shell) runPipeline(p *syntax.Pipeline, io *IO, bg bool) int {
	var start time.Time
	var ru0 syscall.Rusage
	if p.Time {
		start = time.Now()
		syscall.Getrusage(syscall.RUSAGE_CHILDREN, &ru0)
	}
	var status int
	if len(p.Cmds) == 1 {
		if p.Negate {
			sh.noErrexit++
		}
		status = sh.runCommand(p.Cmds[0], io)
		if p.Negate {
			sh.noErrexit--
		}
		sh.pipestatus = []int{status}
	} else {
		status = sh.runPipe(p, io, bg)
	}
	if p.Negate {
		if status == 0 {
			status = 1
		} else {
			status = 0
		}
	}
	if p.Time {
		var ru1 syscall.Rusage
		syscall.Getrusage(syscall.RUSAGE_CHILDREN, &ru1)
		real := time.Since(start)
		user := tvDur(ru1.Utime) - tvDur(ru0.Utime)
		sys := tvDur(ru1.Stime) - tvDur(ru0.Stime)
		fmt.Fprintf(io.Err, "\nreal  %s\nuser  %s\nsys   %s\n", fmtDur(real), fmtDur(user), fmtDur(sys))
	}
	return status
}

func tvDur(tv syscall.Timeval) time.Duration {
	return time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%.0fµs", float64(d)/float64(time.Microsecond))
	case d < time.Second:
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	case d < time.Minute:
		return fmt.Sprintf("%.3fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%.3fs", int(d.Minutes()), d.Seconds()-float64(int(d.Minutes())*60))
}

type pipeElem struct {
	cmd      syntax.Command
	io       *IO
	sub      *Shell
	external bool
	path     string
	args     []string
	env      []string
	procFds  []*os.File
	err      int // non-zero: element failed before running
	closers  []*os.File
}

func (sh *Shell) runPipe(p *syntax.Pipeline, io *IO, bg bool) int {
	n := len(p.Cmds)
	j := &job{cmd: sh.pipeText(p), foreground: !bg, background: bg}
	if bg {
		j.intr = &atomic.Bool{}
	}
	readers := make([]*os.File, n)
	writers := make([]*os.File, n)
	for i := 0; i < n-1; i++ {
		r, w, err := os.Pipe()
		if err != nil {
			sh.errorMsg(io.Err, "pipe: %v", err)
			return 1
		}
		writers[i], readers[i+1] = w, r
	}
	elems := make([]*pipeElem, n)
	broken := make([]*atomic.Bool, n)
	for i, cmd := range p.Cmds {
		eio := io.clone()
		if i > 0 {
			eio.In = fileStream(readers[i], false)
		}
		if i < n-1 {
			broken[i] = &atomic.Bool{}
			eio.Out = &Stream{File: writers[i], broken: broken[i]}
			if p.PipeErr[i] {
				eio.Err = eio.Out
			}
			if p.ErrOnly != nil && p.ErrOnly[i] {
				eio.Err = eio.Out
				eio.Out = io.Out
			}
		}
		e := &pipeElem{cmd: cmd, io: eio}
		last := i == n-1 && !bg
		if last {
			e.sub = sh
		} else {
			e.sub = sh.clone()
			e.sub.jobCtx = j
			if bg {
				e.sub.bgIntr = j.intr
			}
		}
		// Find out up front whether this element is a plain external
		// command so all externals can share a process group.
		if call, ok := cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 {
			sh.classify(e, call)
		}
		elems[i] = e
	}
	// 1. spawn external commands in order
	group := sh.JobControl && sh.inSubst == 0 && !sh.subshell
	var dones []func()
	for i, e := range elems {
		if !e.external || e.err != 0 {
			continue
		}
		files, done, err := sh.childFiles(e.io, e.procFds)
		if err != nil {
			sh.errorMsg(io.Err, "%v", err)
			e.err = 1
			continue
		}
		if _, err := sh.spawn(j, e.path, e.args, e.env, files, group); err != nil {
			e.err = sh.execError(e.args[0], err, io)
		}
		closeChildEnds(files)
		dones = append(dones, done)
		e.closers = append(e.closers, e.procFds...)
		if i > 0 {
			readers[i].Close()
			readers[i] = nil
		}
		if i < n-1 {
			writers[i].Close()
			writers[i] = nil
		}
	}
	// 2. run in-process elements
	statusCh := make([]int, n)
	lastInline := -1
	for i, e := range elems {
		if e.external {
			continue
		}
		if e.sub == sh {
			lastInline = i
			continue
		}
		j.wg.Add(1)
		j.mu.Lock()
		j.internal++
		j.mu.Unlock()
		go func(i int, e *pipeElem) {
			defer func() {
				if i > 0 && readers[i] != nil {
					readers[i].Close()
				}
				if i < n-1 && writers[i] != nil {
					writers[i].Close()
				}
				j.mu.Lock()
				j.internal--
				j.mu.Unlock()
				j.wg.Done()
			}()
			st := e.err
			if st == 0 {
				st = e.sub.runCommand(e.cmd, e.io)
				if e.sub.ctrl == ctrlExit {
					st = e.sub.exitStatus
				}
			}
			statusCh[i] = st
			if bg && i == n-1 {
				j.setInternalStatus(st)
			}
		}(i, e)
	}
	if bg {
		sh.shared.jobs.add(j)
		sh.lastBg = 0 // no process to name if everything runs in-process
		if len(j.procs) > 0 {
			sh.lastBg = j.procs[len(j.procs)-1].pid
		}
		if sh.Interactive {
			fmt.Fprintf(sh.Stderr, "[%d] %d\n", j.id, j.pgid)
		}
		go func() {
			j.wg.Wait()
			for _, d := range dones {
				d()
			}
		}()
		return 0
	}
	if lastInline >= 0 {
		if group && j.pgid > 0 {
			sh.watchProcs(j)
		}
		e := elems[lastInline]
		st := e.err
		if st == 0 {
			st = sh.runCommand(e.cmd, e.io)
		}
		statusCh[lastInline] = st
		if readers[lastInline] != nil {
			readers[lastInline].Close()
		}
	}
	// 3. wait
	var status int
	if group && j.pgid > 0 {
		status = sh.foregroundJob(j, false)
	} else {
		sh.waitJob(j)
		status = sh.finishJob(j)
	}
	j.wg.Wait()
	for _, d := range dones {
		d()
	}
	for _, e := range elems {
		closeAll(e.closers)
	}
	// Assemble pipestatus in pipeline order.
	procStatus := j.statuses()
	k := 0
	ps := make([]int, n)
	for i, e := range elems {
		if e.external && e.err == 0 {
			if k < len(procStatus) {
				ps[i] = procStatus[k]
			}
			k++
		} else {
			ps[i] = statusCh[i]
			if e.err != 0 {
				ps[i] = e.err
			}
		}
	}
	sh.pipestatus = ps
	status = ps[n-1]
	if sh.opts.pipefail {
		for _, s := range ps {
			if s != 0 {
				status = s
			}
		}
	}
	return status
}

// classify expands a pipeline element that is a simple command and
// decides whether it runs as an external process.
func (sh *Shell) classify(e *pipeElem, call *syntax.CallExpr) {
	if len(call.Redirs) > 0 {
		return
	}
	lit, ok := call.Args[0].IsUnquotedLit()
	if !ok {
		return
	}
	if _, isAlias := sh.aliases[lit]; isAlias {
		return
	}
	if sh.lookupFunc(lit) != nil {
		return
	}
	if _, isBuiltin := builtins[lit]; isBuiltin {
		return
	}
	path, err := sh.lookPath(lit)
	if err != nil {
		return
	}
	args, err := e.sub.expandFields(call.Args, e.io, 0)
	if err != nil || len(args) == 0 || args[0] != lit {
		return
	}
	tmp, err := e.sub.evalAssigns(call.Assigns, e.io)
	if err != nil {
		return
	}
	env := e.sub.Environ()
	if len(tmp) > 0 {
		env = overrideEnv(env, tmp)
	}
	e.external, e.path, e.args, e.env = true, path, args, env
	e.procFds = e.sub.takeProcFds()
}

func (sh *Shell) pipeText(p *syntax.Pipeline) string {
	if sh.curStmt != "" && len(sh.callStack) == 0 {
		return sh.curStmt
	}
	return "pipeline"
}

// runBackground starts a statement as a background job.
func (sh *Shell) runBackground(st *syntax.Stmt, io *IO) {
	sh.reapBeforeBackground()
	text := sh.stmtText(st)
	if p, ok := st.Cmd.(*syntax.Pipeline); ok && len(p.Cmds) > 1 {
		prev := sh.curStmt
		sh.curStmt = text
		before := sh.shared.jobs.current()
		sh.runPipe(p, io, true)
		sh.curStmt = prev
		if j := sh.shared.jobs.current(); st.Disown && j != nil && j != before {
			sh.shared.jobs.disown(j)
		}
		sh.status = 0
		return
	}
	j := &job{cmd: text, background: true}
	if call, ok := st.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 {
		e := &pipeElem{io: io, sub: sh}
		sh.classify(e, call)
		if e.external {
			files, done, err := sh.childFiles(io, e.procFds)
			if err != nil {
				sh.errorMsg(io.Err, "%v", err)
				sh.status = 1
				return
			}
			pid, err := sh.spawn(j, e.path, e.args, e.env, files, sh.JobControl && !sh.subshell && sh.inSubst == 0)
			closeChildEnds(files)
			closeAll(e.procFds)
			if err != nil {
				done()
				sh.status = sh.execError(e.args[0], err, io)
				return
			}
			go done()
			sh.lastBg = pid
			if st.Disown {
				sh.shared.jobs.disown(j)
				return
			}
			sh.shared.jobs.add(j)
			if sh.Interactive {
				fmt.Fprintf(sh.Stderr, "[%d] %d\n", j.id, pid)
			}
			sh.status = 0
			return
		}
	}
	sub := sh.clone()
	sub.jobCtx = j
	// its own interrupt: ctrl-c in the foreground doesn't stop it, kill %N does
	j.intr = &atomic.Bool{}
	sub.bgIntr = j.intr
	io = io.clone() // the job's own: exec redirections inside stay there
	j.wg.Add(1)
	j.internal = 1
	// $! names a process; this job has none.
	sh.lastBg = 0
	if !st.Disown {
		sh.shared.jobs.add(j)
		if sh.Interactive {
			fmt.Fprintf(sh.Stderr, "[%d]\n", j.id)
		}
	}
	go func() {
		defer func() {
			j.mu.Lock()
			j.internal--
			j.mu.Unlock()
			j.wg.Done()
		}()
		status := sub.runCommand(st.Cmd, io)
		if sub.ctrl == ctrlExit {
			status = sub.exitStatus
		}
		j.setInternalStatus(status)
	}()
	sh.status = 0
}

// procSubst implements <(cmd) and >(cmd).
func (sh *Shell) procSubst(p *syntax.ProcSubst, io *IO) (string, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	sub := sh.clone()
	sub.inSubst++
	if p.Out {
		go func() {
			sub.runStmts(p.Stmts, &IO{In: fileStream(r, false), Out: io.Out, Err: io.Err})
			r.Close()
		}()
		w = sh.clearOfShellFds(w)
		sh.procFds = append(sh.procFds, w)
		return fmt.Sprintf("/dev/fd/%d", w.Fd()), nil
	}
	go func() {
		sub.runStmts(p.Stmts, &IO{In: io.In, Out: fileStream(w, false), Err: io.Err})
		w.Close()
	}()
	r = sh.clearOfShellFds(r)
	sh.procFds = append(sh.procFds, r)
	return fmt.Sprintf("/dev/fd/%d", r.Fd()), nil
}

// clearOfShellFds renumbers f if its descriptor number is one the script
// uses for something else (exec 3>file keeps that file elsewhere, leaving
// 3 free for the next pipe), so /dev/fd/N names f in the command.
func (sh *Shell) clearOfShellFds(f *os.File) *os.File {
	if _, clash := sh.fds[int(f.Fd())]; !clash {
		return f
	}
	nf, err := dupFile(f, sh.maxShellFd()+1)
	if err != nil {
		return f
	}
	f.Close()
	return nf
}

func (sh *Shell) takeProcFds() []*os.File {
	f := sh.procFds
	sh.procFds = nil
	return f
}
