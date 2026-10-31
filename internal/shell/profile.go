package shell

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/andrinoff/ash/internal/syntax"
)

// Profiling (ash --profile) times every top-level statement of the files
// and code strings the shell runs, and every file as a whole, so the slow
// lines of a config stand out.
//
// Statements inside a top-level if, switch/case or begin/{ } block count
// as top level too, since configs often wrap everything in `if status
// is-interactive`. Loops and function bodies are not broken down; their
// time goes to the statement that ran them.
//
// While profiling is off, Shell.prof is zero and costs one nil check per
// statement in runStmts and one per file in RunFile and source.

// profHook is a shell's part of the profiler: the profiler, and the timed
// statement or file the shell is inside of. Whatever is timed while it
// runs is charged to it as nested time, which gives each entry a self
// time. Subshells copy it along with the rest of the Shell.
type profHook struct {
	p   *profiler
	cur *profEntry
}

type profiler struct {
	start   time.Time
	stopped atomic.Bool

	mu      sync.Mutex
	stmts   map[*syntax.Stmt]*profEntry // statements to time
	entries []*profEntry
	phases  []profPhase
	label   string // name for the next unnamed top-level string, see ProfileAs

	// A file run again (sourced in a loop, say) adds to the entries of
	// its first run rather than getting a second set of rows.
	srcs  map[string]string        // the code each name was first registered with
	where map[profWhere]*profEntry // statements of those first runs
	files map[string]*profEntry
}

type profWhere struct {
	name     string
	pos, end int
}

// profEntry is one timed statement or file.
type profEntry struct {
	name     string // file path, or a label such as "-c"
	src      string // the code the statement is in, for its line and text
	pos, end int
	file     bool // a whole file rather than a statement
	runs     int
	total    time.Duration
	nested   time.Duration // time of timed statements and files run inside it
}

type profPhase struct {
	name  string
	start time.Duration // since ash started
	dur   time.Duration
}

// StartProfile makes the shell time the top-level statements and the
// files it runs, until StopProfile. start is when ash started; the report
// measures from there.
func (sh *Shell) StartProfile(start time.Time) {
	sh.prof = profHook{p: &profiler{
		start: start,
		stmts: map[*syntax.Stmt]*profEntry{},
		srcs:  map[string]string{},
		where: map[profWhere]*profEntry{},
		files: map[string]*profEntry{},
	}}
}

// Profiling reports whether StartProfile is in effect.
func (sh *Shell) Profiling() bool { return sh.prof.p != nil }

// ProfileAs names the next code string that runs at the top level without
// a file name, so its statements are broken down in the profile (main
// uses "-c" for the command of ash -c). Other unnamed strings, like traps
// and the code integrations evaluate, only count as part of what ran them.
func (sh *Shell) ProfileAs(label string) {
	if p := sh.prof.p; p != nil {
		p.mu.Lock()
		p.label = label
		p.mu.Unlock()
	}
}

var noop = func() {}

// ProfilePhase marks the start of a stage of startup, such as reading the
// config, and returns the function that marks its end. The report says how
// long each stage took.
func (sh *Shell) ProfilePhase(name string) func() {
	p := sh.prof.p
	if p == nil {
		return noop
	}
	start := time.Now()
	return func() {
		d := time.Since(start)
		p.mu.Lock()
		p.phases = append(p.phases, profPhase{name: name, start: start.Sub(p.start), dur: d})
		p.mu.Unlock()
	}
}

// runTop runs the top-level statements of a file or a code string. Under
// --profile it registers them first so runStmts times each of them: always
// for files, and for strings only when they run at the top level and have
// a name.
func (sh *Shell) runTop(stmts []*syntax.Stmt, io *IO, name, src string, file bool) int {
	if p := sh.prof.p; p != nil && !p.stopped.Load() {
		p.add(stmts, sh.prof.cur, name, src, file)
	}
	return sh.runStmts(stmts, io)
}

func (p *profiler) add(stmts []*syntax.Stmt, cur *profEntry, name, src string, file bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stmts == nil {
		return // stopped since the caller looked
	}
	if !file {
		if cur != nil {
			// eval, a trap or a hook inside a timed statement: it's part
			// of that statement
			return
		}
		if name == "" {
			name, p.label = p.label, ""
			if name == "" {
				return
			}
		}
	}
	prev, seen := p.srcs[name]
	if !seen {
		p.srcs[name] = src
	}
	p.register(stmts, name, src, !seen, seen && prev == src)
}

// register sets up entries for stmts. On the first run of name they are
// recorded by where they are, and a later run of the same code (again)
// adds to those.
func (p *profiler) register(stmts []*syntax.Stmt, name, src string, first, again bool) {
	for _, st := range stmts {
		k := profWhere{name, st.Pos, st.End}
		e := p.where[k]
		if !again || e == nil {
			e = &profEntry{name: name, src: src, pos: st.Pos, end: st.End}
			p.entries = append(p.entries, e)
			if first {
				p.where[k] = e
			}
		}
		p.stmts[st] = e
		// The conditions of an if are left out: their time is the if's
		// own, on the same line.
		switch c := st.Cmd.(type) {
		case *syntax.IfClause:
			p.register(c.Then, name, src, first, again)
			for _, el := range c.Elifs {
				p.register(el.Then, name, src, first, again)
			}
			p.register(c.Else, name, src, first, again)
		case *syntax.Block:
			p.register(c.Stmts, name, src, first, again)
		case *syntax.CaseClause:
			for _, it := range c.Items {
				p.register(it.Body, name, src, first, again)
			}
		}
	}
}

// profStmt runs st for runStmts while profiling, timing it if it was
// registered.
func (sh *Shell) profStmt(st *syntax.Stmt, io *IO) {
	p := sh.prof.p
	var e *profEntry
	if !p.stopped.Load() {
		p.mu.Lock()
		e = p.stmts[st]
		p.mu.Unlock()
	}
	if e == nil {
		sh.runStmt(st, io)
		return
	}
	parent := sh.prof.cur
	sh.prof.cur = e
	start := time.Now()
	sh.runStmt(st, io)
	p.done(e, parent, time.Since(start))
	sh.prof.cur = parent
}

// profFile starts timing a whole file run by RunFile or source, and
// returns the function that stops it.
func (sh *Shell) profFile(name string) func() {
	p := sh.prof.p
	p.mu.Lock()
	if p.stmts == nil {
		p.mu.Unlock()
		return noop // stopped
	}
	e := p.files[name]
	if e == nil {
		// one row for each file, however often it runs
		e = &profEntry{name: name, file: true}
		p.files[name] = e
		p.entries = append(p.entries, e)
	}
	p.mu.Unlock()
	parent := sh.prof.cur
	sh.prof.cur = e
	start := time.Now()
	return func() {
		p.done(e, parent, time.Since(start))
		sh.prof.cur = parent
	}
}

func (p *profiler) done(e, parent *profEntry, d time.Duration) {
	p.mu.Lock()
	e.runs++
	e.total += d
	if parent != nil {
		parent.nested += d
	}
	p.mu.Unlock()
}

// Profile is what ash --profile measured.
type Profile struct {
	Start  time.Time // when ash started
	items  []profItem
	phases []profPhase
	home   string // for shortening paths
	config string
}

type profItem struct {
	name        string
	src         string
	pos, end    int
	file        bool
	runs        int // shown when more than one
	total, self time.Duration
}

// StopProfile ends profiling and returns the measurements, or nil if the
// shell wasn't profiling.
func (sh *Shell) StopProfile() *Profile {
	p := sh.prof.p
	if p == nil {
		return nil
	}
	sh.prof = profHook{}
	p.stopped.Store(true)
	p.mu.Lock()
	defer p.mu.Unlock()
	prof := &Profile{
		Start:  p.start,
		phases: append([]profPhase(nil), p.phases...),
		home:   sh.HomeDir(),
		config: sh.ConfigDir(),
	}
	for _, e := range p.entries {
		if e.runs == 0 {
			continue
		}
		self := e.total - e.nested
		if self < 0 {
			self = 0
		}
		prof.items = append(prof.items, profItem{
			name: e.name, src: e.src, pos: e.pos, end: e.end, file: e.file,
			runs: e.runs, total: e.total, self: self,
		})
	}
	p.stmts, p.srcs, p.where, p.files = nil, nil, nil, nil
	return prof
}

// How much of the profile the report shows.
const (
	profShowStmts = 10
	profShowFiles = 10
	profTextWidth = 50
	profWhereMax  = 32
)

// WriteReport prints the profile: the statements that took the longest
// themselves, then each file with what it took in all, then a summary
// that ends with end ("first prompt", "exit") at the given time since ash
// started.
func (p *Profile) WriteReport(w io.Writer, end string, at time.Duration) {
	var stmts, files []profItem
	for _, it := range p.items {
		if it.file {
			files = append(files, it)
		} else {
			stmts = append(stmts, it)
		}
	}
	sort.SliceStable(stmts, func(i, j int) bool {
		if stmts[i].self != stmts[j].self {
			return stmts[i].self > stmts[j].self
		}
		return stmts[i].total > stmts[j].total
	})
	sort.SliceStable(files, func(i, j int) bool { return files[i].total > files[j].total })

	var rest time.Duration
	more := 0
	if len(stmts) > profShowStmts {
		for _, it := range stmts[profShowStmts:] {
			rest += it.self
		}
		more = len(stmts) - profShowStmts
		stmts = stmts[:profShowStmts]
	}
	if len(files) > profShowFiles {
		files = files[:profShowFiles]
	}

	type row struct{ where, self, total, text string }
	var top, bottom []row
	width := len("where")
	add := func(rows *[]row, r row) {
		if n := len([]rune(r.where)); n > width {
			width = n
		}
		*rows = append(*rows, r)
	}
	for _, it := range stmts {
		add(&top, row{p.where(it), fmtMs(it.self), fmtMs(it.total), profText(it.src, it.pos, it.end) + runsNote(it.runs, " (", ")")})
	}
	for _, it := range files {
		add(&bottom, row{p.where(it), fmtMs(it.self), fmtMs(it.total), "(whole file" + runsNote(it.runs, ", ", "") + ")"})
	}

	var b strings.Builder
	line := func(r row) {
		pad := strings.Repeat(" ", width-len([]rune(r.where)))
		l := fmt.Sprintf("  %s%s %9s %9s  %s", r.where, pad, r.self, r.total, r.text)
		b.WriteString(strings.TrimRight(l, " ") + "\n")
	}
	if len(top)+len(bottom) > 0 {
		b.WriteString("ash: profile, slowest first\n")
		line(row{"where", "self", "total", "statement"})
		for _, r := range top {
			line(r)
		}
		if more > 0 {
			fmt.Fprintf(&b, "  ... and %d more statements, %s in all\n", more, fmtMs(rest))
		}
		for _, r := range bottom {
			line(r)
		}
	}
	var sum []string
	for _, ph := range p.phases {
		sum = append(sum, ph.name+" took "+fmtMs(ph.dur))
	}
	if end != "" {
		sum = append(sum, end+" at "+fmtMs(at))
	}
	if len(sum) > 0 {
		b.WriteString("ash: " + strings.Join(sum, ", ") + "\n")
	}
	io.WriteString(w, b.String())
}

// runsNote says how often a row's statement or file ran, when it ran more
// than once (a file sourced in a loop): the times are the sums.
func runsNote(runs int, before, after string) string {
	if runs < 2 {
		return ""
	}
	return before + strconv.Itoa(runs) + " runs" + after
}

// where is the file:line column: paths in the config directory are shown
// relative to it, others under $HOME with ~.
func (p *Profile) where(it profItem) string {
	name := it.name
	if name == "-" {
		name = "stdin" // cmd | source
	}
	if filepath.IsAbs(name) {
		if rel, ok := relUnder(name, p.config); ok {
			name = rel
		} else if rel, ok := relUnder(name, p.home); ok {
			name = "~/" + rel
		}
	}
	if r := []rune(name); len(r) > profWhereMax {
		name = "…" + string(r[len(r)-profWhereMax+1:])
	}
	if it.file {
		return name
	}
	line, _ := syntax.LineCol(it.src, it.pos)
	return name + ":" + strconv.Itoa(line)
}

func relUnder(path, dir string) (string, bool) {
	if dir == "" || dir == "/" {
		return "", false
	}
	dir = strings.TrimSuffix(dir, "/")
	if strings.HasPrefix(path, dir+"/") {
		return path[len(dir)+1:], true
	}
	return "", false
}

// profText is the start of a statement's source: its first line without
// control characters, cut to fit, with … when there is more.
func profText(src string, pos, end int) string {
	if pos < 0 || end > len(src) || pos >= end {
		return ""
	}
	s := strings.TrimSpace(src[pos:end])
	more := false
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
		more = true
	}
	s = strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > profTextWidth {
		return string(r[:profTextWidth-1]) + "…"
	}
	if more {
		s += " …"
	}
	return s
}

func fmtMs(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 2, 64) + "ms"
}
