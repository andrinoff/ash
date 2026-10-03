package shell

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/andrinoff/ash/internal/term"
)

type procState uint8

const (
	procRunning procState = iota
	procStopped
	procDone
)

type process struct {
	pid    int
	state  procState
	status int // exit status, or 128+signal
	signal syscall.Signal
	core   bool
}

type job struct {
	id         int
	pgid       int
	cmd        string
	procs      []*process
	foreground bool
	background bool
	notified   bool
	disowned   bool
	tmodes     *unix.Termios
	wg         sync.WaitGroup // in-process pipeline elements
	mu         sync.Mutex
	internal   int // number of in-process elements still running
}

func (j *job) state() procState {
	j.mu.Lock()
	defer j.mu.Unlock()
	allDone := true
	anyStopped := false
	for _, p := range j.procs {
		switch p.state {
		case procRunning:
			allDone = false
		case procStopped:
			allDone = false
			anyStopped = true
		}
	}
	if j.internal > 0 {
		allDone = false
	}
	if allDone {
		return procDone
	}
	if anyStopped {
		return procStopped
	}
	return procRunning
}

// lastStatus is the status of the last process (the pipeline status).
func (j *job) statuses() []int {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]int, len(j.procs))
	for i, p := range j.procs {
		out[i] = p.status
	}
	return out
}

type jobTable struct {
	mu   sync.Mutex
	jobs []*job
	cur  int // id of the "current" job (%+)
	prev int
}

func newJobTable() *jobTable { return &jobTable{} }

func (t *jobTable) add(j *job) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := 1
	used := map[int]bool{}
	for _, x := range t.jobs {
		used[x.id] = true
	}
	for used[id] {
		id++
	}
	j.id = id
	t.jobs = append(t.jobs, j)
	t.prev, t.cur = t.cur, id
}

func (t *jobTable) remove(j *job) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, x := range t.jobs {
		if x == j {
			t.jobs = append(t.jobs[:i], t.jobs[i+1:]...)
			break
		}
	}
	if t.cur == j.id {
		t.cur, t.prev = t.prev, 0
		if t.cur == 0 && len(t.jobs) > 0 {
			t.cur = t.jobs[len(t.jobs)-1].id
		}
	}
	if t.prev == j.id {
		t.prev = 0
	}
}

func (t *jobTable) list() []*job {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := append([]*job(nil), t.jobs...)
	sort.Slice(out, func(a, b int) bool { return out[a].id < out[b].id })
	return out
}

func (t *jobTable) byID(id int) *job {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, j := range t.jobs {
		if j.id == id {
			return j
		}
	}
	return nil
}

func (t *jobTable) current() *job {
	t.mu.Lock()
	cur := t.cur
	t.mu.Unlock()
	if j := t.byID(cur); j != nil {
		return j
	}
	l := t.list()
	if len(l) == 0 {
		return nil
	}
	return l[len(l)-1]
}

// Jobs reports the number of jobs, for prompts.
func (sh *Shell) Jobs() int {
	n := 0
	for _, j := range sh.shared.jobs.list() {
		if !j.disowned {
			n++
		}
	}
	return n
}

// waitStatus converts a wait status into a shell exit status.
func waitStatus(ws syscall.WaitStatus) (procState, int, syscall.Signal, bool) {
	if sig, ok := wsStopped(ws); ok {
		return procStopped, 128 + int(sig), sig, false
	}
	switch {
	case wsContinued(ws):
		return procRunning, 0, 0, false
	case ws.Exited():
		return procDone, ws.ExitStatus(), 0, false
	case ws.Signaled():
		return procDone, 128 + int(ws.Signal()), ws.Signal(), ws.CoreDump()
	}
	return procDone, 1, 0, false
}

// waitProc blocks until p changes state.
func waitProc(p *process, flags int) bool {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(p.pid, &ws, flags, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			// ECHILD: someone else reaped it
			p.state = procDone
			if p.status == 0 {
				p.status = 0
			}
			return true
		}
		if pid == 0 {
			return false
		}
		p.state, p.status, p.signal, p.core = waitStatus(ws)
		return true
	}
}

// waitJob waits for a foreground job to finish or stop.
func (sh *Shell) waitJob(j *job) {
	for _, p := range j.procs {
		j.mu.Lock()
		st := p.state
		j.mu.Unlock()
		for st == procRunning {
			waitProc(p, syscall.WUNTRACED)
			st = p.state
			if st == procStopped {
				// Everything in the group is stopping; collect the rest without blocking.
				for _, q := range j.procs {
					if q != p && q.state == procRunning {
						waitProc(q, syscall.WUNTRACED|syscall.WNOHANG)
					}
				}
				goto stopped
			}
		}
	}
	j.wg.Wait()
	return
stopped:
	if sh.shared.ttyFd >= 0 {
		if t, err := term.GetState(sh.shared.ttyFd); err == nil {
			j.tmodes = t
		}
	}
}

// reapJobs collects status changes of background jobs without blocking.
func (sh *Shell) reapJobs() {
	for _, j := range sh.shared.jobs.list() {
		for _, p := range j.procs {
			if p.state != procDone {
				waitProc(p, syscall.WUNTRACED|syscall.WCONTINUED|syscall.WNOHANG)
			}
		}
	}
}

// NotifyJobs prints finished/stopped background jobs and forgets the
// finished ones. It returns true if anything was printed.
func (sh *Shell) NotifyJobs(w io.Writer) bool {
	sh.reapJobs()
	printed := false
	for _, j := range sh.shared.jobs.list() {
		st := j.state()
		switch st {
		case procDone:
			if j.background && sh.Interactive && !j.disowned {
				fmt.Fprintf(w, "ash: job %d, '%s' has ended%s\n", j.id, j.cmd, signalNote(j))
				printed = true
			}
			sh.shared.jobs.remove(j)
		case procStopped:
			if !j.notified {
				j.notified = true
			}
		}
	}
	return printed
}

func signalNote(j *job) string {
	for _, p := range j.procs {
		if p.signal != 0 && p.signal != syscall.SIGINT && p.signal != syscall.SIGPIPE && p.state == procDone {
			return fmt.Sprintf(" (%s)", sigDescription(p.signal))
		}
	}
	return ""
}

// foregroundJob runs a job in the foreground until it ends or stops.
func (sh *Shell) foregroundJob(j *job, cont bool) int {
	tty := sh.shared.ttyFd
	control := sh.JobControl && tty >= 0 && j.pgid > 0
	if control {
		if j.tmodes != nil && cont {
			term.SetState(tty, j.tmodes)
		}
		term.SetForeground(tty, j.pgid)
	}
	if cont {
		j.mu.Lock()
		for _, p := range j.procs {
			if p.state == procStopped {
				p.state = procRunning
			}
		}
		j.mu.Unlock()
		if j.pgid > 0 {
			unix.Kill(-j.pgid, syscall.SIGCONT)
		}
	}
	sh.waitJob(j)
	if control {
		term.SetForeground(tty, sh.shared.shellPgid)
		sh.settleTerminal(j)
	}
	return sh.finishJob(j)
}

// settleTerminal decides the terminal modes after a foreground job. If
// every process exited normally and left the terminal in a sane cooked
// state, keep it (so `stty` changes stick, as in bash). Otherwise put
// back the shell's modes.
func (sh *Shell) settleTerminal(j *job) {
	tty := sh.shared.ttyFd
	tm := sh.shared.tmodes.Load()
	clean := j.state() == procDone
	for _, p := range j.procs {
		if p.signal != 0 {
			clean = false
		}
	}
	if clean {
		if cur, err := term.GetState(tty); err == nil && cur.Lflag&unix.ICANON != 0 && cur.Lflag&unix.ECHO != 0 && cur.Lflag&unix.ISIG != 0 {
			sh.shared.tmodes.Store(&shellTmodes{cooked: cur})
			return
		}
	}
	if tm != nil && tm.cooked != nil {
		term.SetState(tty, tm.cooked)
	}
}

// finishJob records statuses of a job that finished or stopped.
func (sh *Shell) finishJob(j *job) int {
	st := j.state()
	statuses := j.statuses()
	status := 0
	if len(statuses) > 0 {
		status = statuses[len(statuses)-1]
		if sh.opts.pipefail {
			for _, s := range statuses {
				if s != 0 {
					status = s
				}
			}
		}
	}
	if st == procStopped {
		j.foreground = false
		j.background = true
		if !sh.inJobTable(j) {
			sh.shared.jobs.add(j)
		}
		if sh.Interactive {
			fmt.Fprintf(sh.Stderr, "\nash: job %d, '%s' has stopped\n", j.id, j.cmd)
		}
		j.notified = true
		return status
	}
	if sh.inJobTable(j) {
		sh.shared.jobs.remove(j)
	}
	for _, p := range j.procs {
		if p.state == procDone && p.signal != 0 {
			switch p.signal {
			case syscall.SIGINT, syscall.SIGQUIT:
				if sh.Interactive || sh.JobControl {
					sh.shared.intr.Store(true)
				}
			case syscall.SIGPIPE:
			default:
				if p == j.procs[len(j.procs)-1] || sh.Interactive {
					core := ""
					if p.core {
						core = " (core dumped)"
					}
					fmt.Fprintf(sh.Stderr, "ash: '%s' terminated by %s%s\n", j.cmd, sigDescription(p.signal), core)
				}
			}
		}
	}
	return status
}

func (sh *Shell) inJobTable(j *job) bool {
	for _, x := range sh.shared.jobs.list() {
		if x == j {
			return true
		}
	}
	return false
}

func sigDescription(s syscall.Signal) string {
	name := signalName(s)
	switch s {
	case syscall.SIGSEGV:
		return name + " (address boundary error)"
	case syscall.SIGBUS:
		return name + " (misaligned address error)"
	case syscall.SIGABRT:
		return name + " (abort)"
	case syscall.SIGKILL:
		return name + " (forced quit)"
	case syscall.SIGTERM:
		return name + " (polite quit request)"
	case syscall.SIGILL:
		return name + " (illegal instruction)"
	case syscall.SIGFPE:
		return name + " (floating point exception)"
	}
	return name
}

// HangupJobs sends SIGHUP (and SIGCONT) to all jobs; used when exiting.
func (sh *Shell) HangupJobs() {
	for _, j := range sh.shared.jobs.list() {
		if j.disowned || j.pgid <= 0 {
			continue
		}
		unix.Kill(-j.pgid, syscall.SIGHUP)
		unix.Kill(-j.pgid, syscall.SIGCONT)
	}
}

// StoppedJobs reports whether any job is stopped.
func (sh *Shell) StoppedJobs() bool {
	for _, j := range sh.shared.jobs.list() {
		if j.state() == procStopped {
			return true
		}
	}
	return false
}

// RunningJobs reports whether any background job is still running.
func (sh *Shell) RunningJobs() bool {
	sh.reapJobs()
	for _, j := range sh.shared.jobs.list() {
		if j.state() == procRunning && !j.disowned {
			return true
		}
	}
	return false
}

type shellTmodes struct {
	cooked *unix.Termios
}

// setupTerminal takes control of the terminal for job control.
func (sh *Shell) setupTerminal() {
	fd := int(sh.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return
	}
	sh.shared.ttyFd = fd
	// Wait until we are in the foreground.
	for i := 0; i < 100; i++ {
		fg, err := term.Foreground(fd)
		if err != nil || fg == unix.Getpgrp() {
			break
		}
		unix.Kill(-unix.Getpgrp(), syscall.SIGTTIN)
	}
	pid := os.Getpid()
	if unix.Getpgrp() != pid {
		if err := unix.Setpgid(0, 0); err != nil {
			// Not allowed (e.g. session leader): keep the current group.
		}
	}
	sh.shared.shellPgid = unix.Getpgrp()
	if err := term.SetForeground(fd, sh.shared.shellPgid); err != nil {
		return
	}
	if t, err := term.GetState(fd); err == nil {
		sh.shared.tmodes.Store(&shellTmodes{cooked: t})
	}
	sh.JobControl = true
}

// SaveTermModes records the terminal settings to restore after jobs.
func (sh *Shell) SaveTermModes() {
	if sh.shared.ttyFd < 0 {
		return
	}
	if t, err := term.GetState(sh.shared.ttyFd); err == nil {
		sh.shared.tmodes.Store(&shellTmodes{cooked: t})
	}
}

// TTYFd returns the controlling terminal's fd, or -1.
func (sh *Shell) TTYFd() int { return sh.shared.ttyFd }

// jobSpec resolves %1, %+, %-, %name, %?name or a pid.
func (sh *Shell) jobSpec(spec string) (*job, error) {
	jobs := sh.shared.jobs.list()
	if spec == "" || spec == "%" || spec == "%%" || spec == "%+" {
		if j := sh.shared.jobs.current(); j != nil {
			return j, nil
		}
		return nil, errorf("no current job")
	}
	if spec == "%-" {
		sh.shared.jobs.mu.Lock()
		prev := sh.shared.jobs.prev
		sh.shared.jobs.mu.Unlock()
		if j := sh.shared.jobs.byID(prev); j != nil {
			return j, nil
		}
		return nil, errorf("no previous job")
	}
	if strings.HasPrefix(spec, "%") {
		rest := spec[1:]
		if n, ok := parseArithNumber(rest); ok {
			if j := sh.shared.jobs.byID(int(n)); j != nil {
				return j, nil
			}
			return nil, errorf("%s: no such job", spec)
		}
		var match *job
		for _, j := range jobs {
			if strings.HasPrefix(rest, "?") && strings.Contains(j.cmd, rest[1:]) || strings.HasPrefix(j.cmd, rest) {
				if match != nil {
					return nil, errorf("%s: ambiguous job spec", spec)
				}
				match = j
			}
		}
		if match != nil {
			return match, nil
		}
		return nil, errorf("%s: no such job", spec)
	}
	if n, ok := parseArithNumber(spec); ok {
		for _, j := range jobs {
			if j.pgid == int(n) {
				return j, nil
			}
			for _, p := range j.procs {
				if p.pid == int(n) {
					return j, nil
				}
			}
		}
		// fish: a bare number is a job id
		if j := sh.shared.jobs.byID(int(n)); j != nil {
			return j, nil
		}
	}
	return nil, errorf("%s: no such job", spec)
}
