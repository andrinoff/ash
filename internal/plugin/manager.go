package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Limits.
const (
	defaultQueue = 256  // lines waiting to be written to one plugin
	maxPending   = 1024 // actions waiting to be applied
	stopGrace    = 250 * time.Millisecond
	cacheTTL     = 3 * time.Second // completion answers are reused this long
	cacheMax     = 64
)

// Errors.
var (
	ErrUnknown = errors.New("no such plugin")
	ErrLoaded  = errors.New("already loaded")
	ErrClosed  = errors.New("the shell is exiting")
	ErrNoHello = errors.New("no hello")
	ErrDead    = errors.New("not running")
)

// Options configure a Manager.
type Options struct {
	// LogDir holds each plugin's stderr in NAME.log. Empty discards it.
	LogDir string
	// Wake is called, from any goroutine, when actions are waiting to be
	// taken with Actions. It must not block. After one call it isn't
	// called again until Actions has been called.
	Wake func()
	// Reserved reports names plugins can't take for their commands
	// (the shell's own builtins).
	Reserved func(name string) bool
	// QueueSize is how many lines may wait to be written to one plugin.
	// When the queue is full, events are dropped and counted.
	QueueSize int
}

// Spec says how to start a plugin.
type Spec struct {
	Name string   // what the user calls it; also names its log file
	Path string   // the executable
	Args []string // argv; Args[0] is what the program sees as its name
	Dir  string   // working directory
	Env  []string // environment
}

// State is where a plugin is in its life.
type State uint8

const (
	Starting State = iota // running, but it hasn't said hello yet
	Running               // it said hello
	Dead                  // it exited
)

func (s State) String() string {
	switch s {
	case Starting:
		return "starting"
	case Running:
		return "running"
	}
	return "dead"
}

// Info describes a loaded plugin.
type Info struct {
	Name           string
	Path           string
	Args           []string
	Pid            int
	State          State
	Exit           string // how it ended, when Dead
	Events         []string
	Commands       []Command
	Complete       []string
	DroppedEvents  uint64 // events not sent because its queue was full
	DroppedActions uint64 // actions ignored because too many were waiting
	Log            string
}

// CommandInfo describes a command a plugin added.
type CommandInfo struct {
	Name, Description, Plugin string
}

// Result is what a plugin command printed, and its exit status.
type Result struct {
	Stdout, Stderr string
	Status         int
}

// Manager runs a set of plugins. Its methods are safe to call from any
// goroutine. Sending events never blocks.
type Manager struct {
	opts  Options
	queue int

	mu     sync.Mutex
	procs  map[string]*proc // by name, including dead ones
	seq    uint64
	closed bool
	once   sync.Once

	// tables is rebuilt whenever a plugin says hello or goes away, so
	// that sending an event or looking up a command takes no lock.
	tables atomic.Pointer[tables]

	nextID atomic.Uint64

	amu     sync.Mutex
	actions []Action
	woken   bool
	has     atomic.Bool

	cache compCache
}

type tables struct {
	any      uint8 // events somebody listens to
	subs     [len(eventNames)][]*proc
	commands map[string]cmdRef
	complete map[string]*proc
}

type cmdRef struct {
	p    *proc
	desc string
}

// New returns a Manager with no plugins.
func New(o Options) *Manager {
	m := &Manager{opts: o, queue: o.QueueSize, procs: map[string]*proc{}}
	if m.queue <= 0 {
		m.queue = defaultQueue
	}
	m.tables.Store(&tables{})
	return m
}

// Load starts a plugin. It returns as soon as the process runs; the
// plugin's commands appear when it has said hello (see WaitReady).
func (m *Manager) Load(s Spec) error {
	if !ValidName(s.Name) {
		return fmt.Errorf("%q: not a valid plugin name (use letters, digits, '_', '-' and '.')", s.Name)
	}
	if len(s.Args) == 0 {
		s.Args = []string{s.Path}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if old := m.procs[s.Name]; old != nil && !old.isDone() {
		return ErrLoaded
	}
	m.seq++
	p, err := m.start(s, m.seq)
	if err != nil {
		return err
	}
	m.procs[s.Name] = p
	return nil
}

// WaitReady waits until the plugin has said hello. It fails if the plugin
// exits first or stays quiet for longer than timeout. When the plugin
// exited, the error says how, and the exit isn't reported again in a
// message: the caller tells the user.
func (m *Manager) WaitReady(name string, timeout time.Duration) error {
	p := m.get(name)
	if p == nil {
		return ErrUnknown
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-p.ready:
	case <-p.done:
	case <-t.C:
		return fmt.Errorf("%w within %v", ErrNoHello, timeout)
	}
	if p.isDone() {
		m.unreport(name)
		return p.deadErr()
	}
	return nil
}

// unreport takes a plugin's crash report off the queue.
func (m *Manager) unreport(name string) {
	m.amu.Lock()
	defer m.amu.Unlock()
	keep := m.actions[:0]
	for _, a := range m.actions {
		if !a.crash || a.Plugin != name {
			keep = append(keep, a)
		}
	}
	m.actions = keep
	if len(keep) == 0 {
		m.woken = false
		m.has.Store(false)
	}
}

// Unload stops a plugin: its input is closed and its process group is
// sent SIGTERM, then SIGKILL if it is still there a moment later.
func (m *Manager) Unload(name string) error {
	m.mu.Lock()
	p := m.procs[name]
	if p == nil {
		m.mu.Unlock()
		return ErrUnknown
	}
	delete(m.procs, name)
	p.setStopping()
	m.rebuildLocked()
	m.mu.Unlock()
	p.stop(stopGrace)
	return nil
}

// Reload restarts a plugin with the same command, in dir with env.
func (m *Manager) Reload(name, dir string, env []string) error {
	p := m.get(name)
	if p == nil {
		return ErrUnknown
	}
	s := p.spec
	s.Dir, s.Env = dir, env
	if err := m.Unload(name); err != nil {
		return err
	}
	return m.Load(s)
}

// Spec returns how a plugin was started.
func (m *Manager) Spec(name string) (Spec, bool) {
	p := m.get(name)
	if p == nil {
		return Spec{}, false
	}
	return p.spec, true
}

// List describes the plugins, sorted by name.
func (m *Manager) List() []Info {
	m.mu.Lock()
	procs := make([]*proc, 0, len(m.procs))
	for _, p := range m.procs {
		procs = append(procs, p)
	}
	m.mu.Unlock()
	out := make([]Info, len(procs))
	for i, p := range procs {
		out[i] = p.info()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Close tells the plugins that want to know that the shell is exiting,
// closes their input and gives them up to grace to exit; then their
// process groups are killed.
func (m *Manager) Close(grace time.Duration) {
	m.once.Do(func() {
		m.mu.Lock()
		m.closed = true
		procs := make([]*proc, 0, len(m.procs))
		for _, p := range m.procs {
			if !p.isDone() {
				procs = append(procs, p)
			}
		}
		m.procs = map[string]*proc{}
		m.tables.Store(&tables{})
		m.mu.Unlock()
		exit := encode(&exitEvent{Event: EventExit})
		for _, p := range procs {
			if p.setStopping()&eventBit(EventExit) != 0 {
				p.post(exit)
			}
			p.quitWriter()
		}
		deadline := time.NewTimer(grace)
		defer deadline.Stop()
	wait:
		for _, p := range procs {
			select {
			case <-p.done:
			case <-deadline.C:
				break wait
			}
		}
		for _, p := range procs {
			p.kill()
		}
	})
}

// Wants reports whether any plugin listens to event.
func (m *Manager) Wants(event string) bool {
	return m.tables.Load().any&eventBit(event) != 0
}

func (m *Manager) subscribers(event string) []*proc {
	t := m.tables.Load()
	b := eventBit(event)
	if t.any&b == 0 {
		return nil
	}
	return t.subs[eventIndex(b)]
}

func broadcast(ps []*proc, line []byte) {
	for _, p := range ps {
		p.post(line)
	}
}

// Prompt sends the prompt event: the shell is about to draw a prompt.
func (m *Manager) Prompt(cwd string, status int, pipestatus []int, d time.Duration) {
	ps := m.subscribers(EventPrompt)
	if ps == nil {
		return
	}
	if len(pipestatus) == 0 {
		pipestatus = []int{status}
	}
	broadcast(ps, encode(&promptEvent{Event: EventPrompt, Cwd: cwd, Status: status, Pipestatus: pipestatus, DurationMs: d.Milliseconds()}))
}

// PreExec sends the preexec event: a command line is about to run.
func (m *Manager) PreExec(line, cwd string) {
	if ps := m.subscribers(EventPreExec); ps != nil {
		broadcast(ps, encode(&preexecEvent{Event: EventPreExec, Line: line, Cwd: cwd}))
	}
}

// PostExec sends the postexec event: a command line finished.
func (m *Manager) PostExec(line string, status int, d time.Duration, cwd string) {
	if ps := m.subscribers(EventPostExec); ps != nil {
		broadcast(ps, encode(&postexecEvent{Event: EventPostExec, Line: line, Status: status, DurationMs: d.Milliseconds(), Cwd: cwd}))
	}
}

// Cd sends the cd event: the shell changed directory.
func (m *Manager) Cd(from, to string) {
	if ps := m.subscribers(EventCd); ps != nil {
		broadcast(ps, encode(&cdEvent{Event: EventCd, From: from, To: to}))
	}
}

// Actions returns the actions plugins sent since the last call, oldest
// first, and resets the wake-up.
func (m *Manager) Actions() []Action {
	if !m.has.Load() {
		return nil
	}
	m.amu.Lock()
	a := m.actions
	m.actions = nil
	m.woken = false
	m.has.Store(false)
	m.amu.Unlock()
	return a
}

// Notify queues a message about a plugin, to show above the prompt.
func (m *Manager) Notify(plugin, text string) {
	m.push(nil, []Action{{Plugin: plugin, Kind: ActMessage, Text: text}})
}

// Logf writes a line to a plugin's log, for problems with what it sent.
func (m *Manager) Logf(name, format string, args ...any) {
	if p := m.get(name); p != nil {
		p.logf(format, args...)
	}
}

// push queues actions and wakes the shell up if it isn't already. A
// segment update replaces one for the same segment that is still waiting,
// so a chatty plugin can't fill the queue with them.
func (m *Manager) push(p *proc, acts []Action) {
	m.amu.Lock()
	for _, a := range acts {
		if a.Kind == ActSegment {
			for i, x := range m.actions {
				if x.Kind == ActSegment && x.Plugin == a.Plugin && x.Name == a.Name {
					m.actions = append(m.actions[:i], m.actions[i+1:]...)
					break
				}
			}
		}
		if len(m.actions) >= maxPending {
			if p != nil {
				p.lost.Add(1)
			}
			continue
		}
		m.actions = append(m.actions, a)
	}
	wake := len(m.actions) > 0 && !m.woken
	if wake {
		m.woken = true
	}
	m.has.Store(len(m.actions) > 0)
	m.amu.Unlock()
	if wake && m.opts.Wake != nil {
		m.opts.Wake()
	}
}

// HasCommand reports whether a plugin added a command called name.
func (m *Manager) HasCommand(name string) bool {
	_, ok := m.tables.Load().commands[name]
	return ok
}

// Commands lists the commands plugins added, sorted by name.
func (m *Manager) Commands() []CommandInfo {
	t := m.tables.Load()
	out := make([]CommandInfo, 0, len(t.commands))
	for n, c := range t.commands {
		out = append(out, CommandInfo{Name: n, Description: c.desc, Plugin: c.p.spec.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run runs a plugin command: it sends the run request and waits for the
// answer until ctx is done.
func (m *Manager) Run(ctx context.Context, name string, args []string, cwd string, stdin *string) (Result, error) {
	ref, ok := m.tables.Load().commands[name]
	if !ok {
		return Result{}, fmt.Errorf("%s: %w", name, ErrUnknown)
	}
	if args == nil {
		args = []string{}
	}
	msg, err := ref.p.request(ctx, "", func(id uint64) any {
		return &runRequest{ID: id, Request: "run", Name: name, Args: args, Cwd: cwd, Stdin: stdin}
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Stdout: msg.Stdout, Stderr: msg.Stderr, Status: msg.Status}, nil
}

// CommandPlugin returns the name of the plugin that added command name.
func (m *Manager) CommandPlugin(name string) string {
	if ref, ok := m.tables.Load().commands[name]; ok {
		return ref.p.spec.Name
	}
	return ""
}

// Completes reports whether a plugin completes cmd's arguments.
func (m *Manager) Completes(cmd string) bool {
	_, ok := m.tables.Load().complete[cmd]
	return ok
}

// Complete asks the plugin that completes cmd for candidates, waiting at
// most timeout. It reports false if no plugin completes cmd or the
// plugin didn't answer in time. Answers are kept for a few seconds, and
// one that arrives too late is kept for the next identical request.
func (m *Manager) Complete(cmd, line string, cursor int, timeout time.Duration) ([]Candidate, bool) {
	p := m.tables.Load().complete[cmd]
	if p == nil {
		return nil, false
	}
	key := strconv.FormatUint(p.seq, 10) + "\x00" + cmd + "\x00" + strconv.Itoa(cursor) + "\x00" + line
	if c, ok := m.cache.get(key); ok {
		return c, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	msg, err := p.request(ctx, key, func(id uint64) any {
		return &completeRequest{ID: id, Request: "complete", Name: cmd, Line: line, Cursor: cursor}
	})
	if err != nil {
		return nil, false
	}
	return msg.Candidates, true
}

func (m *Manager) get(name string) *proc {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.procs[name]
}

// hello records what a plugin said in its hello.
func (m *Manager) hello(p *proc, h *Hello) {
	var bits uint8
	for _, e := range h.Events {
		b := eventBit(e)
		if b == 0 {
			p.logf("hello: unknown event %q ignored", e)
		}
		bits |= b
	}
	clean := &Hello{Name: h.Name}
	for i, n := range eventNames {
		if bits&(1<<i) != 0 {
			clean.Events = append(clean.Events, n)
		}
	}
	for _, c := range h.Commands {
		switch {
		case !validCommand(c.Name):
			p.logf("hello: %q can't be a command name; ignored", c.Name)
		case m.opts.Reserved != nil && m.opts.Reserved(c.Name):
			p.logf("hello: %s is a builtin; the command is ignored", c.Name)
		default:
			clean.Commands = append(clean.Commands, c)
		}
	}
	for _, c := range h.Complete {
		if validCommand(c) {
			clean.Complete = append(clean.Complete, c)
		} else {
			p.logf("hello: can't complete %q; ignored", c)
		}
	}
	if !p.setHello(clean, bits) {
		return
	}
	m.mu.Lock()
	if m.procs[p.spec.Name] == p {
		m.rebuildLocked()
	}
	m.mu.Unlock()
	// Only now, so that whoever waits for it finds the commands in place.
	p.readyOnce.Do(func() { close(p.ready) })
}

// died is called once a plugin's process has exited. The segments it set
// are cleared, and an exit nobody asked for is reported.
func (m *Manager) died(p *proc, how string, expected bool) {
	m.mu.Lock()
	current := m.procs[p.spec.Name] == p
	if current {
		m.rebuildLocked()
	}
	m.mu.Unlock()
	var acts []Action
	for _, s := range p.takeSegments() {
		acts = append(acts, Action{Plugin: p.spec.Name, Kind: ActSegment, Name: s})
	}
	if current && !expected {
		msg := "ash: plugin " + p.spec.Name + " stopped (" + how + ")"
		if p.logPath != "" {
			msg += "; its log is " + p.logPath
		}
		acts = append(acts, Action{Plugin: p.spec.Name, Kind: ActMessage, Text: msg, crash: true})
	}
	if len(acts) > 0 {
		m.push(nil, acts)
	}
}

// rebuildLocked recomputes the lookup tables. Plugins loaded first win
// commands and completions that several declare.
func (m *Manager) rebuildLocked() {
	procs := make([]*proc, 0, len(m.procs))
	for _, p := range m.procs {
		procs = append(procs, p)
	}
	sort.Slice(procs, func(i, j int) bool { return procs[i].seq < procs[j].seq })
	t := &tables{commands: map[string]cmdRef{}, complete: map[string]*proc{}}
	for _, p := range procs {
		h, bits := p.live()
		if h == nil {
			continue
		}
		t.any |= bits
		for i := range eventNames {
			if bits&(1<<i) != 0 {
				t.subs[i] = append(t.subs[i], p)
			}
		}
		for _, c := range h.Commands {
			if prev, taken := t.commands[c.Name]; taken {
				p.logf("command %s is already added by plugin %s; ignored", c.Name, prev.p.spec.Name)
				continue
			}
			t.commands[c.Name] = cmdRef{p: p, desc: c.Description}
		}
		for _, c := range h.Complete {
			if _, taken := t.complete[c]; !taken {
				t.complete[c] = p
			}
		}
	}
	m.tables.Store(t)
}

// compCache keeps recent completion answers.
type compCache struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}

type cacheEntry struct {
	cands []Candidate
	at    time.Time
}

func (c *compCache) get(key string) ([]Candidate, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Since(e.at) > cacheTTL {
		return nil, false
	}
	return e.cands, true
}

func (c *compCache) put(key string, cands []Candidate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cacheEntry{}
	}
	if len(c.m) >= cacheMax {
		now := time.Now()
		for k, e := range c.m {
			if now.Sub(e.at) > cacheTTL {
				delete(c.m, k)
			}
		}
		for k := range c.m {
			if len(c.m) < cacheMax {
				break
			}
			delete(c.m, k)
		}
	}
	c.m[key] = cacheEntry{cands: cands, at: time.Now()}
}
