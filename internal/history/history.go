// Package history is ash's command history: a file shared by every running
// shell, and an in-memory copy for recalling, searching and suggesting
// commands.
//
// The file starts with the line "# ash history v1", followed by one entry
// per line:
//
//	unix-seconds TAB status TAB duration-ms TAB dir TAB cmd
//
// Backslashes, tabs and newlines in dir and cmd are escaped as \\, \t and
// \n, so a multi-line command takes one line. Entries are appended with a
// single write to the file opened with O_APPEND, so lines from concurrent
// shells never interleave, and lines that don't parse are skipped. Deleting
// entries rewrites the file through a temporary file and a rename; where
// flock(2) exists the file is locked meanwhile, so that a rewrite never
// loses another shell's append.
//
// In memory only the newest occurrence of each command is kept.
package history

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Entry is a command in the history.
type Entry struct {
	Cmd      string        // the command line; it may span several lines
	When     time.Time     // when it ran, to the second
	Dir      string        // the directory it ran in, if known
	Status   int           // its exit status
	Duration time.Duration // how long it ran, to the millisecond
}

// item is an entry as kept in memory. A dead slot has an empty cmd.
type item struct {
	cmd    string
	dir    string
	when   int64 // unix seconds
	dur    int64 // milliseconds
	status int32
}

func (it *item) entry() Entry {
	return Entry{
		Cmd:      it.cmd,
		When:     time.Unix(it.when, 0),
		Dir:      it.dir,
		Status:   int(it.status),
		Duration: time.Duration(it.dur) * time.Millisecond,
	}
}

// Store is a command history kept in a file. Its methods are safe for
// concurrent use.
type Store struct {
	path    string
	loaded  chan struct{} // closed once the file has been read
	loadErr error

	wmu sync.Mutex // serializes the changes this process makes to the file

	mu    sync.RWMutex
	items []item           // oldest first; dead slots have an empty cmd
	heads []uint64         // the first bytes of each command, for Suggest
	index map[string]int32 // command -> its slot in items
	dead  int              // the number of dead slots
	fresh bool             // the file didn't exist when the store was opened
	fi    os.FileInfo      // the file as last read, to tell when it's replaced
	off   int64            // how much of that file is reflected in memory
}

// Open returns the history kept in the file at path. It returns at once:
// the file is read in the background, and the methods that need its
// contents wait for that. A missing file is an empty history, created when
// the first command is added. With an empty path the history is kept in
// memory only.
func Open(path string) *Store {
	s := &Store{path: path, loaded: make(chan struct{}), index: map[string]int32{}}
	if path == "" {
		close(s.loaded)
		return s
	}
	go s.load()
	return s
}

func (s *Store) load() {
	defer close(s.loaded)
	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.fresh = true
		} else {
			s.loadErr = err
		}
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.loadErr = err
		return
	}
	data, err := readAll(f, fi.Size())
	if err != nil {
		s.loadErr = err
		return
	}
	recs, n := parseRecords(data)
	st := buildState(recs)
	s.mu.Lock()
	s.setState(st)
	s.fi, s.off = fi, int64(n)
	s.mu.Unlock()
}

// readAll reads the rest of f, which is about size bytes long, into a
// string without copying it again.
func readAll(f *os.File, size int64) (string, error) {
	var b strings.Builder
	if size > 0 && size < 1<<40 {
		b.Grow(int(size))
	}
	_, err := io.Copy(&b, f)
	return b.String(), err
}

// Path is the file the history is kept in.
func (s *Store) Path() string { return s.path }

// Wait waits until the history file has been read.
func (s *Store) Wait() { <-s.loaded }

// Loaded reports whether the history file has been read, without waiting.
func (s *Store) Loaded() bool {
	select {
	case <-s.loaded:
		return true
	default:
		return false
	}
}

// Err waits until the history file has been read and returns the error
// that prevented it, if any. A missing file is not an error.
func (s *Store) Err() error {
	<-s.loaded
	return s.loadErr
}

// Add adds a command to the history and appends it to the file. Empty
// commands and commands that start with a space are ignored, and so is
// unescaped whitespace at the end. An earlier occurrence of the command is
// forgotten, though it stays in the file. A zero When means now.
func (s *Store) Add(e Entry) error {
	cmd := trimCommand(e.Cmd)
	if cmd == "" || cmd[0] == ' ' {
		return nil
	}
	when := e.When
	if when.IsZero() {
		when = time.Now()
	}
	it := item{cmd: cmd, dir: e.Dir, when: when.Unix(), dur: max(e.Duration.Milliseconds(), 0), status: int32(e.Status)}
	<-s.loaded
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	s.insert(it)
	s.mu.Unlock()
	if s.path == "" {
		return nil
	}
	start, end, fi, err := appendFile(s.path, appendRecord(nil, &it))
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.noteAppend(start, end, fi)
	s.mu.Unlock()
	return nil
}

// noteAppend records that this store appended the bytes start:end to the
// file fi. If they directly follow what it has read, Merge needn't read
// them; otherwise Merge reads them back after the other shells' entries
// before them, which puts them in file order.
func (s *Store) noteAppend(start, end int64, fi os.FileInfo) {
	switch {
	case s.fi == nil:
		// The file didn't exist when it was read; if this append created
		// it, it is the file from now on.
		if start != 0 || s.off != 0 {
			return
		}
		s.fi = fi
	case !os.SameFile(s.fi, fi):
		return // replaced: Merge reads the new one from scratch
	}
	if start == s.off {
		s.off = end
	}
}

// state is the in-memory history.
type state struct {
	items []item
	heads []uint64
	index map[string]int32
}

// buildState makes the in-memory history from the records of a file,
// oldest first, keeping the newest occurrence of each command. It reuses
// recs.
func buildState(recs []item) state {
	index := make(map[string]int32, min(len(recs), 1<<16))
	w := len(recs)
	for r := len(recs) - 1; r >= 0; r-- {
		if _, dup := index[recs[r].cmd]; dup {
			continue
		}
		w--
		recs[w] = recs[r]
		index[recs[w].cmd] = int32(w)
	}
	items := recs[w:]
	if w > 0 {
		if w > len(recs)/4 {
			items = append(make([]item, 0, len(items)+len(items)/8), items...)
		} else {
			clear(recs[:w])
		}
		for cmd, i := range index {
			index[cmd] = i - int32(w)
		}
	}
	heads := make([]uint64, len(items), cap(items))
	for i := range items {
		heads[i] = headOf(items[i].cmd)
	}
	return state{items, heads, index}
}

func (s *Store) setState(st state) {
	s.items, s.heads, s.index, s.dead = st.items, st.heads, st.index, 0
}

// insert adds it as the newest entry. The caller holds s.mu.
func (s *Store) insert(it item) {
	if i, ok := s.index[it.cmd]; ok {
		s.kill(int(i))
	}
	s.index[it.cmd] = int32(len(s.items))
	s.items = append(s.items, it)
	s.heads = append(s.heads, headOf(it.cmd))
	if s.dead > 64 && s.dead > len(s.items)/4 {
		s.compact()
	}
}

// kill empties slot i. The caller holds s.mu and updates s.index.
func (s *Store) kill(i int) {
	s.items[i] = item{}
	s.heads[i] = 0
	s.dead++
}

// compact removes the dead slots. The caller holds s.mu.
func (s *Store) compact() {
	j := 0
	for i := range s.items {
		if s.items[i].cmd == "" {
			continue
		}
		if i != j {
			s.items[j], s.heads[j] = s.items[i], s.heads[i]
			s.index[s.items[j].cmd] = int32(j)
		}
		j++
	}
	clear(s.items[j:])
	s.items, s.heads, s.dead = s.items[:j], s.heads[:j], 0
}

// live returns the entries, oldest first. The caller holds s.mu.
func (s *Store) live() []item {
	out := make([]item, 0, len(s.items)-s.dead)
	for _, it := range s.items {
		if it.cmd != "" {
			out = append(out, it)
		}
	}
	return out
}

// Len is the number of distinct commands in the history.
func (s *Store) Len() int {
	<-s.loaded
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items) - s.dead
}

// Recent returns the n most recent distinct commands, newest first, or all
// of them if n <= 0.
func (s *Store) Recent(n int) []string {
	<-s.loaded
	s.mu.RLock()
	defer s.mu.RUnlock()
	if live := len(s.items) - s.dead; n <= 0 || n > live {
		n = live
	}
	out := make([]string, 0, n)
	for i := len(s.items) - 1; i >= 0 && len(out) < n; i-- {
		if cmd := s.items[i].cmd; cmd != "" {
			out = append(out, cmd)
		}
	}
	return out
}

// Entries returns the history, newest first, one entry per command.
func (s *Store) Entries() []Entry {
	<-s.loaded
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.items)-s.dead)
	for i := len(s.items) - 1; i >= 0; i-- {
		if s.items[i].cmd != "" {
			out = append(out, s.items[i].entry())
		}
	}
	return out
}

// Search returns the commands that contain q, or that start with it if
// prefix is set, newest first. The search ignores case unless q has
// upper-case letters.
func (s *Store) Search(q string, prefix bool) []string {
	m := newSearcher(q, prefix)
	<-s.loaded
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for i := len(s.items) - 1; i >= 0; i-- {
		if cmd := s.items[i].cmd; cmd != "" && m.match(cmd) {
			out = append(out, cmd)
		}
	}
	return out
}

// searcher matches commands for Search.
type searcher struct {
	q      string
	prefix bool
	fold   bool // ignore case; q is lower-case
	ascii  bool // q is ASCII
}

func newSearcher(q string, prefix bool) searcher {
	m := searcher{q: q, prefix: prefix, fold: !hasUpper(q)}
	if m.fold {
		m.q = strings.ToLower(q)
	}
	m.ascii = isASCII(m.q)
	return m
}

func (m *searcher) match(s string) bool {
	if m.fold {
		if m.ascii && isASCII(s) {
			if m.prefix {
				return len(s) >= len(m.q) && equalFoldASCII(s[:len(m.q)], m.q)
			}
			return indexFoldASCII(s, m.q) >= 0
		}
		s = strings.ToLower(s)
	}
	if m.prefix {
		return strings.HasPrefix(s, m.q)
	}
	return strings.Contains(s, m.q)
}

// Suggest returns a command to suggest to someone who has typed prefix in
// dir: the newest one that starts with prefix and is longer than it,
// preferring commands that succeeded in dir, then commands that succeeded
// anywhere. It returns "" if there is none, and also while the history is
// still being read, so that typing never waits for it.
func (s *Store) Suggest(prefix, dir string) string {
	if prefix == "" || !s.Loaded() {
		return ""
	}
	h, mask := headOf(prefix), headMask(len(prefix))
	s.mu.RLock()
	defer s.mu.RUnlock()
	ok, failed := -1, -1
	for i := len(s.heads) - 1; i >= 0; i-- {
		if s.heads[i]&mask != h {
			continue
		}
		it := &s.items[i]
		if len(it.cmd) <= len(prefix) || it.cmd[:len(prefix)] != prefix {
			continue
		}
		if it.status != 0 {
			if failed < 0 {
				failed = i
			}
			continue
		}
		if dir == "" || it.dir == dir {
			return it.cmd
		}
		if ok < 0 {
			ok = i
		}
	}
	switch {
	case ok >= 0:
		return s.items[ok].cmd
	case failed >= 0:
		return s.items[failed].cmd
	}
	return ""
}

// headOf packs the first 8 bytes of s into an integer, first byte highest,
// so that comparing heads under headMask(len(prefix)) quickly rules out
// most commands that don't start with prefix.
func headOf(s string) uint64 {
	var h uint64
	for i := 0; i < len(s) && i < 8; i++ {
		h |= uint64(s[i]) << (56 - 8*i)
	}
	return h
}

// headMask selects the first n bytes of a head.
func headMask(n int) uint64 {
	if n >= 8 {
		return ^uint64(0)
	}
	return ^(^uint64(0) >> (8 * n))
}

func hasUpper(s string) bool {
	for _, r := range s {
		if r < utf8.RuneSelf {
			if 'A' <= r && r <= 'Z' {
				return true
			}
		} else if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// equalFoldASCII reports whether s equals lower ignoring ASCII case. lower
// is lower-case and as long as s.
func equalFoldASCII(s, lower string) bool {
	for i := 0; i < len(lower); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}

// indexFoldASCII is strings.Index ignoring ASCII case; lower is lower-case.
func indexFoldASCII(s, lower string) int {
	n := len(lower)
	if n == 0 {
		return 0
	}
	c0, u0 := lower[0], lower[0]
	if 'a' <= c0 && c0 <= 'z' {
		u0 -= 'a' - 'A'
	}
	for i := 0; i+n <= len(s); i++ {
		if c := s[i]; (c == c0 || c == u0) && equalFoldASCII(s[i+1:i+n], lower[1:]) {
			return i
		}
	}
	return -1
}
