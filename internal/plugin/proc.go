package plugin

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	maxLine     = 64 << 20 // longer lines from a plugin are skipped
	maxLogSize  = 1 << 20  // a bigger log is moved to NAME.log.old at load
	abandonedAt = 10 * time.Second
)

// proc is one plugin process.
type proc struct {
	m       *Manager
	spec    Spec
	seq     uint64 // load order
	logPath string

	// Set by start before it closes started; read only after that.
	cmd     *exec.Cmd
	stdin   *os.File
	stdout  *os.File
	started chan struct{} // the process runs, or couldn't be started

	logMu sync.Mutex
	log   *os.File

	out       chan []byte   // lines waiting for the writer
	quit      chan struct{} // closed to make the writer flush and close stdin
	quitOnce  sync.Once
	wdone     chan struct{} // the writer has stopped
	rdone     chan struct{} // stdout is closed
	done      chan struct{} // the process exited and was reaped
	ready     chan struct{} // closed by the first hello
	readyOnce sync.Once

	dropped atomic.Uint64 // events dropped because out was full
	lost    atomic.Uint64 // actions dropped because too many were waiting

	mu       sync.Mutex
	pid      int // 0 until it runs
	hello    *Hello
	events   uint8
	pending  map[uint64]*waiter // nil once the process is gone
	exit     string             // how it ended
	stopping bool               // an exit is expected (unload, shell exit)
	segments map[string]bool
}

// waiter is a request waiting for its answer.
type waiter struct {
	ch        chan *inbound // gets the answer, or nil if the plugin died
	key       string        // completion cache key
	abandoned time.Time     // the requester gave up (completions only)
}

func (m *Manager) newProc(s Spec, seq uint64) *proc {
	p := &proc{
		m:        m,
		spec:     s,
		seq:      seq,
		started:  make(chan struct{}),
		out:      make(chan []byte, m.queue),
		quit:     make(chan struct{}),
		wdone:    make(chan struct{}),
		rdone:    make(chan struct{}),
		done:     make(chan struct{}),
		ready:    make(chan struct{}),
		pending:  map[uint64]*waiter{},
		segments: map[string]bool{},
	}
	if m.opts.LogDir != "" {
		p.logPath = filepath.Join(m.opts.LogDir, s.Name+".log")
	}
	return p
}

// start starts the process. Load runs it in the background, because
// starting a process takes a millisecond or two. If it fails, the plugin
// is dead from the start, and reported like a crash.
func (p *proc) start() {
	defer close(p.started)
	err := p.spawn()
	if err == nil {
		go p.writeLoop()
		go p.readLoop()
		go p.waitLoop()
		return
	}
	how := "could not start: " + err.Error()
	p.logf("%s", how)
	p.mu.Lock()
	p.exit = how
	expected := p.stopping
	p.pending = nil
	p.mu.Unlock()
	close(p.wdone)
	close(p.rdone)
	p.m.died(p, how, expected)
	close(p.done)
	p.closeLog()
}

func (p *proc) spawn() error {
	s := p.spec
	f := openLog(p.logPath)
	p.logMu.Lock()
	p.log = f
	p.logMu.Unlock()
	inR, inW, err := os.Pipe()
	if err != nil {
		return err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return err
	}
	cmd := p.command(s.Path, s.Args, inR, outW)
	p.logf("starting %s", strings.Join(s.Args, " "))
	err = cmd.Start()
	if errors.Is(err, syscall.ENOEXEC) && s.Shell != "" {
		// a script without a #! line
		cmd = p.command(s.Shell, append([]string{s.Shell, s.Path}, s.Args[1:]...), inR, outW)
		err = cmd.Start()
	}
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return err
	}
	p.cmd, p.stdin, p.stdout = cmd, inW, outR
	p.mu.Lock()
	p.pid = cmd.Process.Pid
	p.mu.Unlock()
	return nil
}

func (p *proc) command(path string, args []string, stdin, stdout *os.File) *exec.Cmd {
	cmd := &exec.Cmd{Path: path, Args: args, Dir: p.spec.Dir, Env: p.spec.Env, Stdin: stdin, Stdout: stdout}
	p.logMu.Lock()
	if p.log != nil {
		cmd.Stderr = p.log
	}
	p.logMu.Unlock()
	// Its own process group: ctrl-c at the prompt must not reach it, and
	// unloading can kill whatever it started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// openLog opens a plugin's log for appending, moving a big old one aside
// first.
func openLog(path string) *os.File {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil
	}
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogSize {
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// logf writes one line, marked as coming from ash, to the plugin's log.
func (p *proc) logf(format string, args ...any) {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	if p.log == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(p.log, "ash %s [%d]: %s\n", time.Now().Format("2006-01-02 15:04:05"), os.Getpid(), msg)
}

func (p *proc) closeLog() {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	if p.log != nil {
		p.log.Close()
		p.log = nil
	}
}

// post queues a line without blocking. It reports false (and counts the
// line as dropped) if the queue is full.
func (p *proc) post(line []byte) bool {
	select {
	case p.out <- line:
		return true
	default:
		p.dropped.Add(1)
		return false
	}
}

// quitWriter makes the writer flush what is queued, with a short
// deadline, and close the plugin's stdin.
func (p *proc) quitWriter() {
	<-p.started
	p.quitOnce.Do(func() {
		if p.stdin != nil {
			p.stdin.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		}
		close(p.quit)
	})
}

func (p *proc) writeLoop() {
	defer close(p.wdone)
	defer p.stdin.Close()
	for {
		select {
		case b := <-p.out:
			if !p.write(b) {
				return
			}
		case <-p.quit:
			for {
				select {
				case b := <-p.out:
					if !p.write(b) {
						return
					}
				default:
					return
				}
			}
		}
	}
}

func (p *proc) write(b []byte) bool {
	if _, err := p.stdin.Write(b); err != nil {
		if !errors.Is(err, syscall.EPIPE) && !errors.Is(err, os.ErrDeadlineExceeded) {
			p.logf("writing to the plugin: %v", err)
		}
		return false
	}
	return true
}

func (p *proc) readLoop() {
	defer close(p.rdone)
	r := bufio.NewReaderSize(p.stdout, 64<<10)
	for {
		line, err := readLine(r, maxLine)
		if len(line) > 0 {
			p.handle(line)
		}
		if err == errLong {
			p.logf("skipped a line longer than %d bytes", maxLine)
			continue
		}
		if err != nil {
			return
		}
	}
}

var errLong = errors.New("line too long")

// readLine reads one line, without its newline. A line longer than max is
// skipped and errLong returned.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > max {
			for err == bufio.ErrBufferFull {
				_, err = r.ReadSlice('\n')
			}
			if err != nil {
				return nil, err
			}
			return nil, errLong
		}
		if err == bufio.ErrBufferFull {
			buf = append(buf, chunk...)
			continue
		}
		if buf != nil {
			chunk = append(buf, chunk...)
		}
		return bytes.TrimRight(chunk, "\r\n"), err
	}
}

// handle acts on one line from the plugin.
func (p *proc) handle(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	msg, err := parseLine(line)
	if err != nil {
		p.logf("skipped a line that isn't a JSON object (%v): %s", err, clip(line))
		return
	}
	if msg.empty() {
		p.logf("skipped a line with nothing ash knows: %s", clip(line))
		return
	}
	if msg.Hello != nil {
		p.m.hello(p, msg.Hello)
	}
	if msg.ID != nil {
		p.reply(*msg.ID, msg)
	}
	acts := msg.actions(p.spec.Name)
	keep := acts[:0]
	for _, a := range acts {
		if a.Kind == ActSegment {
			if !validSegment(a.Name) {
				p.logf("segment %q: names are letters, digits and '_'; ignored", a.Name)
				continue
			}
			p.mu.Lock()
			if a.Text == "" {
				delete(p.segments, a.Name)
			} else {
				p.segments[a.Name] = true
			}
			p.mu.Unlock()
		}
		keep = append(keep, a)
	}
	if len(keep) > 0 {
		p.m.push(p, keep)
	}
}

func clip(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}

// reply hands an answer to the request waiting for it.
func (p *proc) reply(id uint64, msg *inbound) {
	p.mu.Lock()
	w := p.pending[id]
	delete(p.pending, id)
	p.mu.Unlock()
	if w == nil {
		p.logf("skipped an answer to request %d, which nobody is waiting for", id)
		return
	}
	if w.key != "" {
		p.m.cache.put(w.key, msg.Candidates)
	}
	w.ch <- msg
}

// request sends a request built by build (given its id) and waits for the
// answer. A completion request (key != "") whose requester gives up stays
// registered for a while, so that a late answer still lands in the cache.
func (p *proc) request(ctx context.Context, key string, build func(id uint64) any) (*inbound, error) {
	id := p.m.nextID.Add(1)
	w := &waiter{ch: make(chan *inbound, 1), key: key}
	p.mu.Lock()
	if p.pending == nil {
		p.mu.Unlock()
		return nil, p.deadErr()
	}
	now := time.Now()
	for k, x := range p.pending {
		if !x.abandoned.IsZero() && now.Sub(x.abandoned) > abandonedAt {
			delete(p.pending, k)
		}
	}
	p.pending[id] = w
	p.mu.Unlock()

	line := encode(build(id))
	select {
	case p.out <- line:
	case <-ctx.Done():
		p.forget(id)
		return nil, ctx.Err()
	case <-p.wdone:
		p.forget(id)
		return nil, p.deadErr()
	}
	select {
	case msg := <-w.ch:
		if msg == nil {
			return nil, p.deadErr()
		}
		return msg, nil
	case <-ctx.Done():
		if key != "" {
			p.mu.Lock()
			if p.pending != nil && p.pending[id] == w {
				w.abandoned = time.Now()
			}
			p.mu.Unlock()
		} else {
			p.forget(id)
			p.post(encode(&cancelRequest{ID: id, Request: "cancel"}))
		}
		return nil, ctx.Err()
	case <-p.rdone:
		// No more answers can come; take one that just did.
		select {
		case msg := <-w.ch:
			if msg != nil {
				return msg, nil
			}
		default:
		}
		p.forget(id)
		// The process is usually gone too; wait a moment to say how.
		select {
		case <-p.done:
		case <-time.After(100 * time.Millisecond):
		}
		return nil, p.deadErr()
	}
}

func (p *proc) forget(id uint64) {
	p.mu.Lock()
	if p.pending != nil {
		delete(p.pending, id)
	}
	p.mu.Unlock()
}

func (p *proc) waitLoop() {
	err := p.cmd.Wait()
	// Let the reader handle what the plugin wrote last, but don't wait
	// for a child that inherited stdout and still holds it open.
	select {
	case <-p.rdone:
	case <-time.After(100 * time.Millisecond):
		p.stdout.Close()
		<-p.rdone
	}
	p.stdout.Close()
	how := exitText(err)
	p.mu.Lock()
	p.exit = how
	expected := p.stopping
	pending := p.pending
	p.pending = nil
	p.mu.Unlock()
	for _, w := range pending {
		select {
		case w.ch <- nil:
		default:
		}
	}
	p.quitWriter()
	p.logf("exited: %s", how)
	p.m.died(p, how, expected)
	close(p.done)
	<-p.wdone
	p.closeLog()
}

func exitText(err error) string {
	if err == nil {
		return "exit status 0"
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return "killed by signal " + strconv.Itoa(int(ws.Signal())) + " (" + ws.Signal().String() + ")"
		}
		return "exit status " + strconv.Itoa(ee.ExitCode())
	}
	return err.Error()
}

// stop ends the process: stdin closed and SIGTERM to its group, then
// SIGKILL to the group after grace (which also gets the children it
// left behind).
func (p *proc) stop(grace time.Duration) {
	p.quitWriter()
	if p.isDone() {
		return
	}
	p.signal(syscall.SIGTERM)
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-p.done:
	case <-t.C:
	}
	p.kill()
	select {
	case <-p.done:
	case <-time.After(time.Second):
	}
}

// kill sends SIGKILL to the plugin's process group.
func (p *proc) kill() { p.signal(syscall.SIGKILL) }

func (p *proc) signal(sig syscall.Signal) {
	p.mu.Lock()
	pid := p.pid
	p.mu.Unlock()
	if pid <= 0 {
		return
	}
	syscall.Kill(-pid, sig)
	if !p.isDone() {
		// in case it moved itself to another group
		syscall.Kill(pid, sig)
	}
}

func (p *proc) isDone() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *proc) deadErr() error {
	p.mu.Lock()
	how := p.exit
	p.mu.Unlock()
	if how == "" {
		return fmt.Errorf("plugin %s: %w", p.spec.Name, ErrDead)
	}
	return fmt.Errorf("plugin %s: %w (%s)", p.spec.Name, ErrDead, how)
}

// setHello records the hello. It reports false if the plugin is going
// away anyway.
func (p *proc) setHello(h *Hello, bits uint8) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	ok := p.pending != nil && !p.stopping
	if ok {
		p.hello, p.events = h, bits
	}
	return ok
}

// setStopping marks an exit as expected and returns the events the
// plugin listens to.
func (p *proc) setStopping() uint8 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopping = true
	return p.events
}

// live returns the hello and events of a plugin that is running and said
// hello, or nil.
func (p *proc) live() (*Hello, uint8) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil || p.stopping || p.hello == nil {
		return nil, 0
	}
	return p.hello, p.events
}

func (p *proc) takeSegments() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.segments))
	for s := range p.segments {
		out = append(out, s)
	}
	p.segments = map[string]bool{}
	return out
}

func (p *proc) info() Info {
	p.mu.Lock()
	defer p.mu.Unlock()
	in := Info{
		Name:           p.spec.Name,
		Path:           p.spec.Path,
		Args:           p.spec.Args,
		Pid:            p.pid,
		Exit:           p.exit,
		DroppedEvents:  p.dropped.Load(),
		DroppedActions: p.lost.Load(),
		Log:            p.logPath,
	}
	switch {
	case p.pending == nil:
		in.State = Dead
	case p.hello != nil:
		in.State = Running
	}
	if p.hello != nil {
		in.Events = p.hello.Events
		in.Commands = p.hello.Commands
		in.Complete = p.hello.Complete
	}
	return in
}
