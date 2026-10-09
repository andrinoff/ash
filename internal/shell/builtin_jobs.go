package shell

import (
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func init() {
	builtins["jobs"] = bJobs
	builtins["fg"] = bFg
	builtins["bg"] = bBg
	builtins["wait"] = bWait
	builtins["kill"] = bKill
	builtins["disown"] = bDisown
	builtins["trap"] = bTrap
	builtins["suspend"] = bSuspend
	for k, v := range map[string]string{
		"jobs":    "list background jobs",
		"fg":      "bring a job to the foreground",
		"bg":      "resume a job in the background",
		"wait":    "wait for jobs to finish",
		"kill":    "send a signal to processes or jobs",
		"disown":  "forget about a job",
		"trap":    "run a command when a signal arrives",
		"suspend": "suspend the shell",
	} {
		builtinHelp[k] = v
	}
}

var signalNames = map[string]syscall.Signal{
	"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "QUIT": syscall.SIGQUIT, "ILL": syscall.SIGILL,
	"TRAP": syscall.SIGTRAP, "ABRT": syscall.SIGABRT, "BUS": syscall.SIGBUS, "FPE": syscall.SIGFPE,
	"KILL": syscall.SIGKILL, "USR1": syscall.SIGUSR1, "SEGV": syscall.SIGSEGV, "USR2": syscall.SIGUSR2,
	"PIPE": syscall.SIGPIPE, "ALRM": syscall.SIGALRM, "TERM": syscall.SIGTERM, "CHLD": syscall.SIGCHLD,
	"CONT": syscall.SIGCONT, "STOP": syscall.SIGSTOP, "TSTP": syscall.SIGTSTP, "TTIN": syscall.SIGTTIN,
	"TTOU": syscall.SIGTTOU, "URG": syscall.SIGURG, "XCPU": syscall.SIGXCPU, "XFSZ": syscall.SIGXFSZ,
	"VTALRM": syscall.SIGVTALRM, "PROF": syscall.SIGPROF, "WINCH": syscall.SIGWINCH, "IO": syscall.SIGIO,
	"SYS": syscall.SIGSYS,
}

func signalName(s syscall.Signal) string {
	for n, v := range signalNames {
		if v == s {
			return "SIG" + n
		}
	}
	return fmt.Sprintf("signal %d", int(s))
}

func parseSignal(s string) (syscall.Signal, bool) {
	s = strings.TrimPrefix(strings.ToUpper(s), "-")
	if n, err := strconv.Atoi(s); err == nil {
		return syscall.Signal(n), true
	}
	s = strings.TrimPrefix(s, "SIG")
	sig, ok := signalNames[s]
	return sig, ok
}

func bJobs(sh *Shell, io *IO, args []string) int {
	sh.reapJobs()
	mode := ""
	quiet := false
	var specs []string
	for _, a := range args[1:] {
		switch a {
		case "-p", "--pid":
			mode = "pid"
		case "-g", "--group":
			mode = "group"
		case "-c", "--command":
			mode = "command"
		case "-l", "--last":
			mode = "last"
		case "-q", "--query", "--quiet":
			quiet = true
		default:
			specs = append(specs, a)
		}
	}
	jobs := sh.shared.jobs.list()
	if len(specs) > 0 {
		jobs = nil
		for _, s := range specs {
			j, err := sh.jobSpec(s)
			if err != nil {
				if !quiet {
					fmt.Fprintf(io.Err, "jobs: %v\n", err)
				}
				return 1
			}
			jobs = append(jobs, j)
		}
	}
	var visible []*job
	for _, j := range jobs {
		if !j.disowned {
			visible = append(visible, j)
		}
	}
	if quiet {
		if len(visible) == 0 {
			return 1
		}
		return 0
	}
	if len(visible) == 0 {
		if mode == "" {
			fmt.Fprintln(io.Err, "jobs: there are no jobs")
		}
		return 1
	}
	if mode == "last" {
		visible = visible[len(visible)-1:]
	}
	cur := sh.shared.jobs.current()
	for _, j := range visible {
		switch mode {
		case "pid":
			for _, p := range j.procs {
				fmt.Fprintln(io.Out, p.pid)
			}
		case "group":
			fmt.Fprintln(io.Out, j.pgid)
		case "command":
			fmt.Fprintln(io.Out, j.cmd)
		default:
			state := "running"
			switch j.state() {
			case procStopped:
				state = "stopped"
			case procDone:
				state = "done"
			}
			mark := " "
			if j == cur {
				mark = "+"
			}
			fmt.Fprintf(io.Out, "[%d]%s %-8s %-7d %s\n", j.id, mark, state, j.pgid, j.cmd)
		}
	}
	return 0
}

func (sh *Shell) jobArg(io *IO, name string, args []string) *job {
	spec := ""
	if len(args) > 1 {
		spec = args[1]
	}
	j, err := sh.jobSpec(spec)
	if err != nil {
		fmt.Fprintf(io.Err, "%s: %v\n", name, err)
		return nil
	}
	return j
}

func bFg(sh *Shell, io *IO, args []string) int {
	if !sh.JobControl {
		fmt.Fprintln(io.Err, "fg: no job control in this shell")
		return 1
	}
	j := sh.jobArg(io, "fg", args)
	if j == nil {
		return 1
	}
	fmt.Fprintf(io.Err, "ash: sending job %d, '%s' to foreground\n", j.id, j.cmd)
	j.foreground, j.background, j.notified = true, false, false
	return sh.foregroundJob(j, true)
}

func bBg(sh *Shell, io *IO, args []string) int {
	j := sh.jobArg(io, "bg", args)
	if j == nil {
		return 1
	}
	if j.state() != procStopped {
		fmt.Fprintf(io.Err, "bg: job %d is already running\n", j.id)
		return 0
	}
	fmt.Fprintf(io.Err, "ash: sending job %d, '%s' to background\n", j.id, j.cmd)
	j.mu.Lock()
	for _, p := range j.procs {
		if p.state == procStopped {
			p.state = procRunning
		}
	}
	j.mu.Unlock()
	j.background, j.foreground, j.notified = true, false, false
	if j.pgid > 0 {
		unix.Kill(-j.pgid, syscall.SIGCONT)
	}
	return 0
}

// bWait waits for jobs. With no arguments it waits for all of them and
// returns 0; otherwise it returns the status of the last one named (or,
// with -n, of whichever finishes first). Ctrl-C stops the wait, not the
// jobs. It polls so that an interrupt is noticed promptly.
func bWait(sh *Shell, io *IO, args []string) int {
	var targets []*job
	anyOne, named := false, false
	status := 0
	for _, a := range args[1:] {
		if a == "-n" || a == "--any" {
			anyOne = true
			continue
		}
		named = true
		j, err := sh.jobSpec(a)
		if err != nil {
			if n, perr := strconv.Atoi(a); perr == nil {
				var ws syscall.WaitStatus
				if _, err := syscall.Wait4(n, &ws, 0, nil); err == nil {
					_, status, _, _ = waitStatus(ws)
					continue
				}
			}
			fmt.Fprintf(io.Err, "wait: %v\n", err)
			return 127
		}
		targets = append(targets, j)
	}
	if !named {
		targets = sh.shared.jobs.list()
	}
	statuses := make([]int, len(targets))
	done := make([]bool, len(targets))
	left := len(targets)
	delay := time.Millisecond
	for left > 0 {
		for i, j := range targets {
			if done[i] {
				continue
			}
			pollJob(j)
			// With job control, a job that stops ends the wait too.
			st := j.state()
			if st == procRunning || st == procStopped && !sh.JobControl {
				continue
			}
			done[i] = true
			left--
			statuses[i] = j.exitStatus(sh.opts.pipefail)
			if st == procDone {
				sh.shared.jobs.remove(j)
			}
			if anyOne {
				return statuses[i]
			}
		}
		if !sh.subshell {
			// A trapped signal ends the wait, even if the jobs are done
			// by now, as in bash; its handler runs next.
			if sig := sh.shared.traps.pendingSignal(); sig != 0 {
				return 128 + int(sig)
			}
		}
		if left == 0 {
			break
		}
		if sh.interrupted() {
			return 130
		}
		time.Sleep(delay)
		if delay < 20*time.Millisecond {
			delay *= 2
		}
	}
	if named && len(targets) > 0 {
		return statuses[len(targets)-1]
	}
	return status
}

func bKill(sh *Shell, io *IO, args []string) int {
	sig := syscall.SIGTERM
	a := args[1:]
	if len(a) > 0 && (a[0] == "-l" || a[0] == "-L") {
		if len(a) > 1 {
			for _, x := range a[1:] {
				if n, err := strconv.Atoi(x); err == nil {
					if n > 128 {
						n -= 128
					}
					fmt.Fprintln(io.Out, strings.TrimPrefix(signalName(syscall.Signal(n)), "SIG"))
				} else if s, ok := parseSignal(x); ok {
					fmt.Fprintln(io.Out, int(s))
				}
			}
			return 0
		}
		names := make([]string, 0, len(signalNames))
		for n := range signalNames {
			names = append(names, n)
		}
		sort.Slice(names, func(i, j int) bool { return signalNames[names[i]] < signalNames[names[j]] })
		for _, n := range names {
			fmt.Fprintf(io.Out, "%2d) SIG%s\n", int(signalNames[n]), n)
		}
		return 0
	}
	if len(a) > 0 && strings.HasPrefix(a[0], "-") && len(a[0]) > 1 && a[0] != "--" {
		spec := a[0][1:]
		if spec == "s" || spec == "n" {
			if len(a) < 2 {
				fmt.Fprintln(io.Err, "kill: option requires an argument")
				return 2
			}
			spec = a[1]
			a = a[1:]
		}
		s, ok := parseSignal(spec)
		if !ok {
			fmt.Fprintf(io.Err, "kill: %s: invalid signal\n", spec)
			return 2
		}
		sig = s
		a = a[1:]
	}
	if len(a) > 0 && a[0] == "--" {
		a = a[1:]
	}
	if len(a) == 0 {
		fmt.Fprintln(io.Err, "kill: usage: kill [-s sigspec | -signum] pid | %job ...")
		return 2
	}
	status := 0
	for _, t := range a {
		if strings.HasPrefix(t, "%") {
			j, err := sh.jobSpec(t)
			if err != nil {
				fmt.Fprintf(io.Err, "kill: %v\n", err)
				status = 1
				continue
			}
			if j.pgid > 0 {
				if err := unix.Kill(-j.pgid, sig); err != nil {
					fmt.Fprintf(io.Err, "kill: %s: %s\n", t, errText(err))
					status = 1
				}
				if sig == syscall.SIGTERM || sig == syscall.SIGHUP || sig == syscall.SIGINT {
					unix.Kill(-j.pgid, syscall.SIGCONT)
				}
			} else {
				for _, p := range j.procs {
					unix.Kill(p.pid, sig)
				}
			}
			// a backgrounded block or function runs in the shell itself
			j.signalInternal(sig)
			if sig == syscall.SIGTERM || sig == syscall.SIGHUP || sig == syscall.SIGINT {
				j.signalInternal(syscall.SIGCONT)
			}
			continue
		}
		pid, err := strconv.Atoi(t)
		if err != nil {
			fmt.Fprintf(io.Err, "kill: %s: arguments must be process or job IDs\n", t)
			status = 1
			continue
		}
		if pid == os.Getpid() && sh.shared.traps.queue(strings.TrimPrefix(signalName(sig), "SIG")) {
			// A trapped signal to ourselves: queue the handler directly so
			// it runs before the next command, as in bash, instead of
			// whenever the signal reaches its goroutine.
			continue
		}
		if err := unix.Kill(pid, sig); err != nil {
			fmt.Fprintf(io.Err, "kill: (%d) - %s\n", pid, errText(err))
			status = 1
		}
	}
	return status
}

func bDisown(sh *Shell, io *IO, args []string) int {
	var targets []*job
	if len(args) < 2 {
		if j := sh.shared.jobs.current(); j != nil {
			targets = append(targets, j)
		}
	}
	for _, a := range args[1:] {
		if a == "-a" {
			targets = sh.shared.jobs.list()
			continue
		}
		j, err := sh.jobSpec(a)
		if err != nil {
			fmt.Fprintf(io.Err, "disown: %v\n", err)
			return 1
		}
		targets = append(targets, j)
	}
	if len(targets) == 0 {
		fmt.Fprintln(io.Err, "disown: there are no suitable jobs")
		return 1
	}
	for _, j := range targets {
		if j.state() == procStopped && j.pgid > 0 {
			unix.Kill(-j.pgid, syscall.SIGCONT)
		}
		sh.shared.jobs.disown(j)
	}
	return 0
}

func bSuspend(sh *Shell, io *IO, args []string) int {
	if sh.Login && (len(args) < 2 || args[1] != "-f") {
		fmt.Fprintln(io.Err, "suspend: cannot suspend a login shell")
		return 1
	}
	signal.Reset(syscall.SIGTSTP)
	unix.Kill(0, syscall.SIGSTOP)
	return 0
}

// traps holds `trap` handlers.
type traps struct {
	mu       sync.Mutex
	handlers map[string]string // signal name (or EXIT/ERR) -> code
	funcs    map[string]string // signal name -> function (fish --on-signal)
	chans    map[syscall.Signal]chan os.Signal
	ignored  map[syscall.Signal]bool // set to SIG_IGN by trap '' SIG
	pending  []string
	has      atomic.Bool // pending is not empty
}

func newTraps() *traps {
	return &traps{handlers: map[string]string{}, funcs: map[string]string{},
		chans: map[syscall.Signal]chan os.Signal{}, ignored: map[syscall.Signal]bool{}}
}

func normSig(s string) (string, bool) {
	u := strings.ToUpper(s)
	switch u {
	case "EXIT", "0":
		return "EXIT", true
	case "ERR", "DEBUG", "RETURN":
		return u, true
	}
	if sig, ok := parseSignal(u); ok {
		if name := signalName(sig); strings.HasPrefix(name, "SIG") {
			return name[3:], true
		}
	}
	return "", false
}

func (t *traps) setFuncHandler(sh *Shell, sig, fn string) {
	name, ok := normSig(sig)
	if !ok {
		return
	}
	t.mu.Lock()
	t.funcs[name] = fn
	t.mu.Unlock()
	t.sync(sh, name)
}

// sync makes the process's handling of a signal match its trap: caught
// (and run between commands) when there is a handler, ignored when the
// trap is empty, the default after trap - SIG.
func (t *traps) sync(sh *Shell, name string) {
	sig, ok := signalNames[name]
	if !ok || sig == syscall.SIGKILL || sig == syscall.SIGSTOP {
		return
	}
	// The interactive shell handles these itself; the trap is only
	// recorded. For TERM, HUP and QUIT it may also be caught, but their
	// disposition is left alone.
	owned := false
	if sh.Interactive {
		switch sig {
		case syscall.SIGINT, syscall.SIGCHLD, syscall.SIGWINCH, syscall.SIGTSTP, syscall.SIGTTIN, syscall.SIGTTOU:
			return
		case syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT:
			owned = true
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !sh.Interactive && signal.Ignored(sig) && !t.ignored[sig] {
		// POSIX: signals ignored on entry can't be trapped or reset
		return
	}
	code, hasCode := t.handlers[name]
	_, hasFn := t.funcs[name]
	switch {
	case hasFn || hasCode && code != "":
		delete(t.ignored, sig)
		if t.chans[sig] == nil {
			ch := make(chan os.Signal, 4)
			t.chans[sig] = ch
			signal.Notify(ch, sig)
			go func() {
				for range ch {
					t.mu.Lock()
					t.pending = append(t.pending, name)
					t.has.Store(true)
					t.mu.Unlock()
				}
			}()
		}
	case hasCode && !owned:
		t.stop(sig)
		signal.Ignore(sig)
		t.ignored[sig] = true
	default:
		t.stop(sig)
		if t.ignored[sig] {
			signal.Reset(sig)
			delete(t.ignored, sig)
		}
	}
}

// stop stops catching sig. t.mu is held.
func (t *traps) stop(sig syscall.Signal) {
	if ch := t.chans[sig]; ch != nil {
		signal.Stop(ch) // no more sends after this returns
		close(ch)
		delete(t.chans, sig)
	}
}

// pendingSignal returns the first trapped signal waiting to be handled.
func (t *traps) pendingSignal() syscall.Signal {
	if !t.has.Load() {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.pending) == 0 {
		return 0
	}
	return signalNames[t.pending[0]]
}

// queue records that a trapped signal arrived, as its handler would.
func (t *traps) queue(name string) bool {
	if sig := signalNames[name]; sig == 0 || sig == syscall.SIGKILL || sig == syscall.SIGSTOP {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	code := t.handlers[name]
	_, hasFn := t.funcs[name]
	if code == "" && !hasFn {
		return false
	}
	t.pending = append(t.pending, name)
	t.has.Store(true)
	return true
}

// RunPendingTraps runs handlers for signals that arrived. It returns true
// if any trap ran.
func (sh *Shell) RunPendingTraps() bool { return sh.runPendingTraps(sh.StdIO()) }

// runPendingTraps runs the handlers with the streams of the code that
// was running when the signal arrived.
func (sh *Shell) runPendingTraps(io *IO) bool {
	t := sh.shared.traps
	if !t.has.Load() {
		return false
	}
	t.mu.Lock()
	pending := t.pending
	t.pending = nil
	t.has.Store(false)
	t.mu.Unlock()
	for _, name := range pending {
		sh.runSignalTrap(name, io)
	}
	return len(pending) > 0
}

// RunSignalTrap runs the trap or --on-signal handler for a signal name
// like "INT". It reports whether a handler existed.
func (sh *Shell) RunSignalTrap(name string) bool { return sh.runSignalTrap(name, sh.StdIO()) }

func (sh *Shell) runSignalTrap(name string, io *IO) bool {
	t := sh.shared.traps
	t.mu.Lock()
	code, hasCode := t.handlers[name]
	fn, hasFn := t.funcs[name]
	t.mu.Unlock()
	if !hasCode && !hasFn {
		return false
	}
	saved := sh.status
	if name == "INT" {
		sh.ClearInterrupt() // the trap handles ctrl-c
	}
	if hasFn {
		sh.CallFunction(fn, []string{"SIG" + name}, io)
	}
	if hasCode && code != "" {
		sh.RunString(code, "", io)
	}
	sh.status = saved
	return true
}

// RunExitTrap runs the EXIT trap, once.
func (sh *Shell) RunExitTrap() {
	t := sh.shared.traps
	t.mu.Lock()
	code, ok := t.handlers["EXIT"]
	delete(t.handlers, "EXIT")
	t.mu.Unlock()
	if ok && code != "" {
		ctrl := sh.ctrl
		sh.ctrl = ctrlNone
		sh.RunString(code, "", sh.StdIO())
		sh.ctrl = ctrl
	}
}

// HasTrap reports whether a trap is set for a signal (e.g. "INT").
func (sh *Shell) HasTrap(name string) bool {
	t := sh.shared.traps
	t.mu.Lock()
	defer t.mu.Unlock()
	_, a := t.handlers[name]
	_, b := t.funcs[name]
	return a || b
}

func bTrap(sh *Shell, io *IO, args []string) int {
	t := sh.shared.traps
	a := args[1:]
	if len(a) > 0 && a[0] == "--" {
		a = a[1:]
	}
	if len(a) == 0 || a[0] == "-p" {
		t.mu.Lock()
		var names []string
		if len(a) > 1 {
			for _, s := range a[1:] {
				if n, ok := normSig(s); ok {
					names = append(names, n)
				}
			}
		} else {
			for n := range t.handlers {
				names = append(names, n)
			}
			sort.Strings(names)
		}
		for _, n := range names {
			if code, ok := t.handlers[n]; ok {
				fmt.Fprintf(io.Out, "trap -- %s %s\n", Quote(code), n)
			}
		}
		t.mu.Unlock()
		return 0
	}
	if a[0] == "-l" {
		return bKill(sh, io, []string{"kill", "-l"})
	}
	code := a[0]
	sigs := a[1:]
	if len(sigs) == 0 {
		// trap SIG: reset
		sigs, code = a, "-"
	}
	if _, err := strconv.Atoi(code); err == nil && len(a) > 0 {
		// trap 2 3: reset these
		sigs, code = a, "-"
	}
	for _, s := range sigs {
		name, ok := normSig(s)
		if !ok {
			fmt.Fprintf(io.Err, "trap: %s: invalid signal specification\n", s)
			return 1
		}
		t.mu.Lock()
		if code == "-" {
			delete(t.handlers, name)
		} else {
			t.handlers[name] = code
		}
		t.mu.Unlock()
		t.sync(sh, name)
	}
	return 0
}
