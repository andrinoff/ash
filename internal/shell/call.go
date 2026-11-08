package shell

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"

	"github.com/andrinoff/ash/internal/syntax"
)

// execCall runs a simple command.
func (sh *Shell) execCall(c *syntax.CallExpr, io *IO) int {
	if sh.depth > maxDepth {
		sh.errorMsg(io.Err, "maximum recursion depth reached")
		return 1
	}
	sh.lineno = c.Pos
	substBefore := sh.substCount
	args, arrays, err := sh.expandCallArgs(c, io)
	procFds := sh.takeProcFds()
	defer closeAll(procFds)
	if err != nil {
		if err != errInterrupted {
			sh.errorMsg(io.Err, "%v", err)
		}
		if sh.opts.nounset && strings.Contains(err.Error(), "unbound variable") && !sh.Interactive {
			sh.ctrl = ctrlExit
			sh.exitStatus = 1
		}
		return 1
	}
	if len(args) == 0 {
		for _, a := range c.Assigns {
			if err := sh.doAssign(a, io, setShAuto, false); err != nil {
				sh.errorMsg(io.Err, "%v", err)
				return 1
			}
		}
		substStatus := 0
		if sh.substCount != substBefore {
			substStatus = sh.status
		}
		if len(c.Redirs) > 0 {
			_, cleanup, err := sh.redirect(c.Redirs, io)
			cleanup()
			if err != nil {
				sh.errorMsg(io.Err, "%v", err)
				return 1
			}
		}
		return substStatus
	}
	tmp, err := sh.evalAssigns(c.Assigns, io)
	if err != nil {
		sh.errorMsg(io.Err, "%v", err)
		return 1
	}
	if sh.opts.xtrace {
		var b strings.Builder
		b.WriteString("+ ")
		for i, a := range args {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(Quote(a))
		}
		b.WriteByte('\n')
		io.Err.WriteString(b.String())
	}
	rio := io
	if len(c.Redirs) > 0 {
		var cleanup func()
		rio, cleanup, err = sh.redirect(c.Redirs, io)
		defer cleanup()
		if err != nil {
			sh.errorMsg(io.Err, "%v", err)
			return 1
		}
	}
	sh.lastArg = args[len(args)-1]
	aliasOK := false
	if len(c.Args) > 0 {
		_, aliasOK = c.Args[0].IsUnquotedLit()
	}
	// `exec 3>file` needs to know which descriptors its own redirections
	// changed.
	prevBase := sh.redirBase
	sh.redirBase = nil
	if len(c.Redirs) > 0 {
		sh.redirBase = io
	}
	defer func() { sh.redirBase = prevBase }()
	if len(arrays) > 0 {
		return sh.declareArrays(args, arrays, rio, tmp, aliasOK, procFds)
	}
	return sh.dispatch(args, rio, tmp, aliasOK, procFds)
}

// dispatch finds and runs a command by name.
func (sh *Shell) dispatch(args []string, io *IO, tmp []tmpVar, aliasOK bool, extraFds []*os.File) int {
	name := args[0]
	if aliasOK {
		if val, ok := sh.aliases[name]; ok && !sh.inAlias(name) {
			return sh.runAlias(name, val, args, io, tmp)
		}
	}
	if fn := sh.lookupFunc(name); fn != nil {
		return sh.withTempVars(tmp, func() int { return sh.callFunc(fn, args, io) })
	}
	if b, ok := sh.builtinFunc(name); ok {
		return sh.withTempVars(tmp, func() int { return b(sh, io, args) })
	}
	return sh.runExternal(args, io, tmp, extraFds)
}

func (sh *Shell) inAlias(name string) bool {
	for _, a := range sh.aliasStack {
		if a == name {
			return true
		}
	}
	return false
}

func (sh *Shell) runAlias(name, val string, args []string, io *IO, tmp []tmpVar) int {
	src := val
	for _, a := range args[1:] {
		src += " " + Quote(a)
	}
	f, err := syntax.Parse(src)
	if err != nil {
		sh.errorMsg(io.Err, "alias %s: %v", name, err)
		return 2
	}
	sh.aliasStack = append(sh.aliasStack, name)
	defer func() { sh.aliasStack = sh.aliasStack[:len(sh.aliasStack)-1] }()
	return sh.withTempVars(tmp, func() int { return sh.runStmts(f.Stmts, io) })
}

// runExternal runs a program found in $PATH (or by path).
func (sh *Shell) runExternal(args []string, io *IO, tmp []tmpVar, extraFds []*os.File) int {
	path, err := sh.lookPath(args[0])
	if err != nil {
		return sh.commandNotFound(args, io, err)
	}
	env := sh.Environ()
	if len(tmp) > 0 {
		env = overrideEnv(env, tmp)
	}
	j := &job{cmd: sh.jobText(args), foreground: true}
	ownGroup := sh.JobControl && sh.jobCtx == nil && sh.inSubst == 0 && !sh.subshell
	if sh.jobCtx != nil {
		return sh.spawnInJob(sh.jobCtx, path, args, env, io, extraFds)
	}
	files, done, err := sh.childFiles(io, extraFds)
	if err != nil {
		sh.errorMsg(io.Err, "%v", err)
		return 1
	}
	pid, err := sh.spawn(j, path, args, env, files, ownGroup)
	closeChildEnds(files)
	if err != nil {
		done()
		return sh.execError(args[0], err, io)
	}
	_ = pid
	var status int
	if ownGroup {
		status = sh.foregroundJob(j, false)
		if j.state() == procStopped {
			// ctrl-z: the job keeps its end of any output pipe (cached
			// captures stdout through one), so the copiers finish when
			// it does, after fg. Waiting for them here would hang.
			return status
		}
	} else {
		sh.waitJob(j)
		status = sh.finishJob(j)
	}
	done()
	return status
}

// spawnInJob runs an external command inside an existing job (from a
// pipeline element or background block running in a subshell).
func (sh *Shell) spawnInJob(j *job, path string, args, env []string, io *IO, extraFds []*os.File) int {
	files, done, err := sh.childFiles(io, extraFds)
	if err != nil {
		sh.errorMsg(io.Err, "%v", err)
		return 1
	}
	j.mu.Lock()
	p := &job{cmd: j.cmd, pgid: j.pgid}
	j.mu.Unlock()
	// The commands of a backgrounded block or function get a process
	// group of their own (the job has none), so that ctrl-c at the
	// terminal doesn't reach them.
	group := sh.JobControl && (sh.inSubst == 0 && p.pgid > 0 || j.intr != nil)
	_, err = sh.spawnRaw(p, path, args, env, files, group, false)
	if err != nil && group {
		_, err = sh.spawnRaw(p, path, args, env, files, false, false)
	}
	closeChildEnds(files)
	if err != nil {
		done()
		return sh.execError(args[0], err, io)
	}
	j.addLive(p)
	sh.waitJob(p)
	j.removeLive(p)
	status := sh.finishJob(p)
	done()
	return status
}

func (sh *Shell) execError(name string, err error, io *IO) int {
	switch {
	case errors.Is(err, syscall.ENOENT):
		sh.errorMsg(io.Err, "%s: no such file or directory", name)
		return 127
	case errors.Is(err, syscall.EACCES):
		sh.errorMsg(io.Err, "%s: permission denied", name)
		return 126
	case errors.Is(err, syscall.EISDIR):
		sh.errorMsg(io.Err, "%s: is a directory", name)
		return 126
	}
	sh.errorMsg(io.Err, "%s: %s", name, errText(err))
	return 126
}

func errText(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Path + ": " + errText(pe.Err)
	}
	var se syscall.Errno
	if errors.As(err, &se) {
		s := se.Error()
		return strings.ToLower(s[:1]) + s[1:]
	}
	return err.Error()
}

func overrideEnv(env []string, tmp []tmpVar) []string {
	out := make([]string, 0, len(env)+len(tmp))
	skip := map[string]bool{}
	for _, t := range tmp {
		skip[t.name] = true
	}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 && skip[kv[:i]] {
			continue
		}
		out = append(out, kv)
	}
	for _, t := range tmp {
		sep := " "
		if IsPathVar(t.name) {
			sep = ":"
		}
		out = append(out, t.name+"="+strings.Join(t.vals, sep))
	}
	return out
}

func (sh *Shell) jobText(args []string) string {
	if sh.curStmt != "" && len(sh.callStack) == 0 {
		return sh.curStmt
	}
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = Quote(a)
	}
	s := strings.Join(parts, " ")
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}

// childFile is what a child process gets for one fd.
type childFile struct {
	f     *os.File
	close bool // parent closes after spawn
}

// childFiles maps an IO to real files for a child process. done waits for
// output copy goroutines and must be called after the child exits.
func (sh *Shell) childFiles(sio *IO, extra []*os.File) ([]childFile, func(), error) {
	var wg sync.WaitGroup
	files := make([]childFile, 3)
	streams := []*Stream{sio.In, sio.Out, sio.Err}
	maxFd := sh.maxShellFd()
	for n := range sio.Extra {
		if n > maxFd {
			maxFd = n
		}
	}
	for _, f := range extra {
		if int(f.Fd()) > maxFd {
			maxFd = int(f.Fd())
		}
	}
	for i := 3; i <= maxFd; i++ {
		files = append(files, childFile{})
		streams = append(streams, sh.ioFd(sio, i)) // incl. exec 3>file, inherited
	}
	for i, s := range streams {
		if s == nil || s.closed {
			continue
		}
		if s.File != nil {
			files[i] = childFile{f: s.File}
			continue
		}
		r, w, err := os.Pipe()
		if err != nil {
			closeChildEnds(files) // (which also ends the copies started so far)
			return nil, func() {}, err
		}
		if i == 0 || s.R != nil {
			files[i] = childFile{f: r, close: true}
			src := s.R
			if src == nil {
				src = strings.NewReader("")
			}
			go func() {
				io.Copy(w, src)
				w.Close()
			}()
			continue
		}
		files[i] = childFile{f: w, close: true}
		dst := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			io.Copy(dst, r)
			r.Close()
		}()
	}
	for _, f := range extra {
		n := int(f.Fd())
		if n < len(files) && files[n].f == nil {
			files[n] = childFile{f: f}
		}
	}
	return files, func() { wg.Wait() }, nil
}

func closeChildEnds(files []childFile) {
	for _, cf := range files {
		if cf.close && cf.f != nil {
			cf.f.Close()
		}
	}
}

func closeAll(fs []*os.File) {
	for _, f := range fs {
		f.Close()
	}
}

// spawn starts a process for job j.
func (sh *Shell) spawn(j *job, path string, args, env []string, files []childFile, ownGroup bool) (int, error) {
	return sh.spawnRaw(j, path, args, env, files, ownGroup, true)
}

func (sh *Shell) spawnRaw(j *job, path string, args, env []string, files []childFile, group, track bool) (int, error) {
	fds := make([]uintptr, len(files))
	for i, cf := range files {
		if cf.f == nil {
			fds[i] = ^uintptr(0)
		} else {
			fds[i] = cf.f.Fd()
		}
	}
	attr := &syscall.ProcAttr{Dir: sh.dir, Env: env, Files: fds}
	if group {
		j.mu.Lock()
		pgid := j.pgid
		j.mu.Unlock()
		sys := &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
		if j.foreground && pgid == 0 && sh.shared.ttyFd >= 0 {
			sys.Foreground = true
			sys.Ctty = sh.shared.ttyFd
		}
		attr.Sys = sys
	}
	pid, err := startProcess(path, args, attr)
	if errors.Is(err, syscall.ENOEXEC) {
		// A script without a shebang: run it with ash, like sh does.
		nargs := append([]string{sh.Self(), path}, args[1:]...)
		pid, err = startProcess(sh.Self(), nargs, attr)
	}
	if err != nil {
		return 0, err
	}
	j.mu.Lock()
	j.procs = append(j.procs, &process{pid: pid})
	if group && j.pgid == 0 {
		j.pgid = pid
	}
	j.mu.Unlock()
	sh.shared.paths.ranCommand()
	return pid, nil
}

// commandNotFound handles an unknown command: implicit cd, the
// user's handler, or an error with suggestions.
func (sh *Shell) commandNotFound(args []string, io *IO, err error) int {
	name := args[0]
	if errors.Is(err, errIsDir) || (len(args) == 1 && strings.ContainsRune(name, '/')) || name == ".." || name == "..." || name == "~" {
		target := name
		if name == "..." {
			target = "../.."
		}
		if st, err2 := os.Stat(sh.absPath(sh.expandTildeString(target))); err2 == nil && st.IsDir() && len(args) == 1 && sh.Interactive {
			return sh.changeDir(sh.expandTildeString(target), io, true)
		}
		if errors.Is(err, errIsDir) {
			sh.errorMsg(io.Err, "%s: is a directory", name)
			return 126
		}
	}
	for _, h := range []string{"ash_command_not_found", "fish_command_not_found"} {
		if fn := sh.lookupFunc(h); fn != nil {
			sh.callFunc(fn, append([]string{h}, args...), io)
			return 127
		}
	}
	if strings.ContainsRune(name, '/') {
		if errors.Is(err, syscall.EACCES) {
			sh.errorMsg(io.Err, "%s: permission denied", name)
			return 126
		}
		sh.errorMsg(io.Err, "%s: no such file or directory", name)
		return 127
	}
	msg := fmt.Sprintf("ash: unknown command: %s", name)
	if s := sh.suggestCommand(name); s != "" {
		msg += fmt.Sprintf(" (did you mean %s?)", s)
		sh.noteFix(name, s)
	}
	fmt.Fprintln(io.Err, msg)
	return 127
}

func (sh *Shell) expandTildeString(s string) string {
	if s == "~" {
		return sh.HomeDir()
	}
	if strings.HasPrefix(s, "~/") {
		return sh.HomeDir() + s[1:]
	}
	return s
}
