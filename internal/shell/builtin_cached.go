package shell

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cached runs a command once and prints its remembered output on later
// calls, for the slow `eval "$(tool init)"` lines of a config.
//
// An entry is one file in $CacheDir/cached named by a hash of the command
// words, $PATH and (with --dir) the directory. It holds a small text
// header and then the output. Writers write a temporary file and rename it
// into place, so shells starting at the same time never read half an
// entry.

func init() {
	builtins["cached"] = bCached
	builtinHelp["cached"] = "run a command once and reuse its output"
}

const cachedUsage = `usage: cached [--ttl DURATION] [--watch FILE]... [--dir] [--] COMMAND [ARGS...]
       cached --list [COMMAND [ARGS...]]
       cached --clear [COMMAND [ARGS...]]

Run COMMAND and keep its output and exit status. Later calls print the
kept output instead of running COMMAND again, until it is older than the
TTL, a watched file changes, or COMMAND itself changes (a new version is
installed, or the function is redefined). A different $PATH is a
different entry.

  --ttl DURATION  how long the output stays fresh: 90s, 30m, 12h, 7d
                  (default 24h)
  --watch FILE    run again when FILE changes; repeat for more files
  --dir           keep a separate entry for each directory
  --list          show what is cached
  --clear         forget everything, or only COMMAND
  -h, --help      show this help

Only standard output and the exit status are kept. Standard input and
standard error are passed through when COMMAND runs. Entries live in
~/.cache/ash/cached.
`

const (
	cachedDefaultTTL = 24 * time.Hour
	cachedMagic      = "ash-cached 1"
)

// cachedMaxOutput is the most output kept; more is passed through but not
// cached. A variable so tests can lower it.
var cachedMaxOutput = 8 << 20

type cachedOpts struct {
	ttl     time.Duration
	watch   []string
	dir     bool
	list    bool
	clear   bool
	command []string
}

func bCached(sh *Shell, io *IO, args []string) int {
	o := cachedOpts{ttl: cachedDefaultTTL}
	value := func(i *int, name string) (string, bool) {
		a := args[*i]
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
		if *i+1 >= len(args) {
			fmt.Fprintf(io.Err, "cached: %s needs a value\n", name)
			return "", false
		}
		*i++
		return args[*i], true
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			o.command = args[i+1:]
			i = len(args)
		case a == "--ttl" || strings.HasPrefix(a, "--ttl="):
			v, ok := value(&i, "--ttl")
			if !ok {
				return 2
			}
			d, err := parseTTL(v)
			if err != nil {
				fmt.Fprintf(io.Err, "cached: --ttl: %v\n", err)
				return 2
			}
			o.ttl = d
		case a == "--watch" || strings.HasPrefix(a, "--watch="):
			v, ok := value(&i, "--watch")
			if !ok {
				return 2
			}
			o.watch = append(o.watch, sh.absPath(v))
		case a == "--dir":
			o.dir = true
		case a == "--list":
			o.list = true
		case a == "--clear":
			o.clear = true
		case a == "-h" || a == "--help":
			io.Out.WriteString(cachedUsage)
			return 0
		case len(a) > 1 && a[0] == '-':
			fmt.Fprintf(io.Err, "cached: unknown option %s\n", a)
			return 2
		default:
			o.command = args[i:]
			i = len(args)
		}
	}
	dir := filepath.Join(sh.CacheDir(), "cached")
	switch {
	case o.list:
		return cachedList(io, dir, o.command)
	case o.clear:
		return cachedClear(io, dir, o.command)
	case len(o.command) == 0:
		fmt.Fprint(io.Err, "cached: no command given\n"+strings.SplitN(cachedUsage, "\n\n", 2)[0]+"\n")
		return 2
	}
	return sh.runCached(io, dir, &o)
}

// cacheEntry is one cached command.
type cacheEntry struct {
	created time.Time
	ttl     time.Duration // the TTL it was stored with, for --list
	status  int
	command []string
	dir     string // with --dir
	ident   string // what the command was, see cachedIdent
	watch   []watchStat
	out     []byte
}

type watchStat struct {
	path  string
	mtime int64 // -1 if the file doesn't exist
	size  int64
}

func (sh *Shell) runCached(io *IO, dir string, o *cachedOpts) int {
	ident, ok := sh.cachedIdent(o.command[0])
	if !ok {
		fmt.Fprintf(io.Err, "cached: %s: command not found\n", o.command[0])
		return 127
	}
	wd := ""
	if o.dir {
		wd = sh.dir
	}
	file := filepath.Join(dir, cachedKey(o.command, strings.Join(sh.GetList("PATH"), ":"), wd))
	watch := statWatches(o.watch)
	now := time.Now()
	if e := readCacheEntry(file); e != nil && e.fresh(o, wd, ident, watch, now) {
		if len(e.out) > 0 {
			io.Out.Write(e.out)
		}
		return e.status
	}

	tee := &cacheTee{out: io.Out, max: cachedMaxOutput}
	cio := io.clone()
	cio.Out = &Stream{W: tee}
	status := sh.dispatch(o.command, cio, nil, false, nil)
	// Keep only complete runs: not 126/127 (couldn't run), not killed by a
	// signal or ctrl-c, not cut short by a closed stdout, and not exit or
	// return, which a replay couldn't repeat.
	if status >= 126 || sh.interrupted() || sh.ctrl != ctrlNone || tee.over || tee.err != nil {
		return status
	}
	e := &cacheEntry{
		created: now, ttl: o.ttl, status: status, command: o.command,
		dir: wd, ident: ident, watch: watch, out: tee.buf.Bytes(),
	}
	if err := writeCacheEntry(dir, file, e); err != nil {
		fmt.Fprintf(io.Err, "cached: %s\n", errText(err))
	}
	return status
}

// fresh reports whether e can stand in for running the command now.
func (e *cacheEntry) fresh(o *cachedOpts, dir, ident string, watch []watchStat, now time.Time) bool {
	age := now.Sub(e.created)
	if age < 0 || age >= o.ttl || e.ident != ident || e.dir != dir || !equalStrings(e.command, o.command) {
		return false
	}
	if len(e.watch) != len(watch) {
		return false
	}
	for i := range watch {
		if e.watch[i] != watch[i] {
			return false
		}
	}
	return true
}

// cachedIdent describes what name runs, so an entry goes stale when that
// changes: a function's source, the ash version for a builtin, or an
// executable's path, mtime and size. ok is false if nothing can run.
func (sh *Shell) cachedIdent(name string) (string, bool) {
	if fn := sh.lookupFunc(name); fn != nil {
		h := sha256.Sum256([]byte(fn.Src))
		return "function " + hex.EncodeToString(h[:12]), true
	}
	if _, ok := builtins[name]; ok {
		return "builtin " + Version, true
	}
	p, err := sh.lookPath(name)
	if err != nil {
		return "", false
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", false
	}
	return "exe " + strconv.Quote(p) + " " + strconv.FormatInt(st.ModTime().UnixNano(), 10) + " " + strconv.FormatInt(st.Size(), 10), true
}

// statWatches stats the watched files, in a fixed order so the same files
// given in another order still match.
func statWatches(paths []string) []watchStat {
	if len(paths) == 0 {
		return nil
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	out := make([]watchStat, 0, len(sorted))
	for i, p := range sorted {
		if i > 0 && p == sorted[i-1] {
			continue
		}
		w := watchStat{path: p, mtime: -1, size: -1}
		if st, err := os.Stat(p); err == nil {
			w.mtime, w.size = st.ModTime().UnixNano(), st.Size()
		}
		out = append(out, w)
	}
	return out
}

// cachedKey names the entry for a command.
func cachedKey(command []string, path, dir string) string {
	h := sha256.New()
	field := func(s string) {
		h.Write([]byte(strconv.Itoa(len(s))))
		h.Write([]byte{':'})
		h.Write([]byte(s))
	}
	field(cachedMagic)
	field(path)
	field(dir)
	for _, w := range command {
		field(w)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// cacheTee passes a command's output through while keeping a copy.
type cacheTee struct {
	mu   sync.Mutex
	out  io.Writer
	buf  bytes.Buffer
	max  int
	over bool  // more than max: not cached
	err  error // writing through failed: the output is incomplete
}

func (t *cacheTee) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.over {
		if t.buf.Len()+len(p) > t.max {
			t.over = true
			t.buf = bytes.Buffer{}
		} else {
			t.buf.Write(p)
		}
	}
	n, err := t.out.Write(p)
	if err != nil && t.err == nil {
		t.err = err
	}
	return n, err
}

// The entry file: a header of "key value" lines, a blank line, then the
// output.
//
//	ash-cached 1
//	created 1730000000000000000
//	ttl 86400000000000
//	status 0
//	size 1234
//	command "go" "env" "GOPATH"
//	dir "/home/me/src"
//	ident exe "/usr/local/go/bin/go" 1730000000000000000 12345678
//	watch "/home/me/.config/go/env" 1730000000000000000 120

func (e *cacheEntry) encode() []byte {
	var b bytes.Buffer
	b.WriteString(cachedMagic + "\n")
	fmt.Fprintf(&b, "created %d\nttl %d\nstatus %d\nsize %d\ncommand", e.created.UnixNano(), int64(e.ttl), e.status, len(e.out))
	for _, w := range e.command {
		b.WriteString(" " + strconv.Quote(w))
	}
	b.WriteString("\n")
	if e.dir != "" {
		b.WriteString("dir " + strconv.Quote(e.dir) + "\n")
	}
	b.WriteString("ident " + e.ident + "\n")
	for _, w := range e.watch {
		fmt.Fprintf(&b, "watch %s %d %d\n", strconv.Quote(w.path), w.mtime, w.size)
	}
	b.WriteString("\n")
	b.Write(e.out)
	return b.Bytes()
}

var errBadEntry = errors.New("bad cache entry")

func decodeCacheEntry(data []byte) (*cacheEntry, error) {
	head, out, ok := bytes.Cut(data, []byte("\n\n"))
	if !ok {
		return nil, errBadEntry
	}
	lines := strings.Split(string(head), "\n")
	if lines[0] != cachedMagic {
		return nil, errBadEntry
	}
	e := &cacheEntry{out: out}
	size := -1
	for _, l := range lines[1:] {
		key, val, _ := strings.Cut(l, " ")
		var err error
		switch key {
		case "created":
			var n int64
			n, err = strconv.ParseInt(val, 10, 64)
			e.created = time.Unix(0, n)
		case "ttl":
			var n int64
			n, err = strconv.ParseInt(val, 10, 64)
			e.ttl = time.Duration(n)
		case "status":
			e.status, err = strconv.Atoi(val)
		case "size":
			size, err = strconv.Atoi(val)
		case "command":
			e.command, err = unquoteWords(val)
		case "dir":
			e.dir, err = strconv.Unquote(val)
		case "ident":
			e.ident = val
		case "watch":
			var w watchStat
			var q string
			if q, err = strconv.QuotedPrefix(val); err == nil {
				w.path, _ = strconv.Unquote(q)
				_, err = fmt.Sscanf(val[len(q):], " %d %d", &w.mtime, &w.size)
			}
			e.watch = append(e.watch, w)
		}
		if err != nil {
			return nil, errBadEntry
		}
	}
	// a short file means a write was cut off (say, by a crash)
	if size != len(out) || len(e.command) == 0 || e.created.IsZero() {
		return nil, errBadEntry
	}
	return e, nil
}

func unquoteWords(s string) ([]string, error) {
	var out []string
	for s = strings.TrimLeft(s, " "); s != ""; s = strings.TrimLeft(s, " ") {
		q, err := strconv.QuotedPrefix(s)
		if err != nil {
			return nil, err
		}
		w, _ := strconv.Unquote(q)
		out = append(out, w)
		s = s[len(q):]
	}
	return out, nil
}

// readCacheEntry returns the entry in file, or nil if there is none or it
// can't be used.
func readCacheEntry(file string) *cacheEntry {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	e, err := decodeCacheEntry(data)
	if err != nil {
		return nil
	}
	return e
}

// writeCacheEntry writes e to file through a temporary file and a rename,
// which other shells see happen all at once.
func writeCacheEntry(dir, file string, e *cacheEntry) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(e.encode())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

type cacheFile struct {
	path string
	*cacheEntry
}

// cacheEntries reads every entry in dir, newest first, keeping those for
// command if it is given.
func cacheEntries(dir string, command []string) ([]cacheFile, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		return nil, err
	}
	var out []cacheFile
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".") || !de.Type().IsRegular() {
			continue
		}
		p := filepath.Join(dir, de.Name())
		e := readCacheEntry(p)
		if e == nil || len(command) > 0 && !equalStrings(e.command, command) {
			continue
		}
		out = append(out, cacheFile{p, e})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].created.After(out[j].created) })
	return out, nil
}

func cachedList(io *IO, dir string, command []string) int {
	es, err := cacheEntries(dir, command)
	if err != nil {
		fmt.Fprintf(io.Err, "cached: %s\n", errText(err))
		return 1
	}
	now := time.Now()
	var b strings.Builder
	for _, e := range es {
		words := make([]string, len(e.command))
		for i, w := range e.command {
			words[i] = Quote(w)
		}
		fmt.Fprintf(&b, "%-4s %6s  %s", fmtAge(now.Sub(e.created)), fmtSize(len(e.out)), strings.Join(words, " "))
		var notes []string
		if e.status != 0 {
			notes = append(notes, "exit "+strconv.Itoa(e.status))
		}
		if e.dir != "" {
			notes = append(notes, "in "+e.dir)
		}
		if now.Sub(e.created) >= e.ttl {
			notes = append(notes, "expired")
		}
		if len(notes) > 0 {
			b.WriteString("  (" + strings.Join(notes, ", ") + ")")
		}
		b.WriteString("\n")
	}
	io.Out.WriteString(b.String())
	return 0
}

// cachedClear removes the entries for command, in every directory and
// $PATH, or all of them. Temporary files are left alone unless they are
// old, since another shell may be about to rename one into place.
func cachedClear(io *IO, dir string, command []string) int {
	status := 0
	remove := func(p string) {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(io.Err, "cached: %s\n", errText(err))
			status = 1
		}
	}
	if len(command) > 0 {
		es, err := cacheEntries(dir, command)
		if err != nil {
			fmt.Fprintf(io.Err, "cached: %s\n", errText(err))
			return 1
		}
		for _, e := range es {
			remove(e.path)
		}
		return status
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0
		}
		fmt.Fprintf(io.Err, "cached: %s\n", errText(err))
		return 1
	}
	for _, de := range des {
		p := filepath.Join(dir, de.Name())
		if strings.HasPrefix(de.Name(), ".tmp-") {
			if info, err := de.Info(); err != nil || time.Since(info.ModTime()) < time.Hour {
				continue
			}
		}
		remove(p)
	}
	return status
}

// parseTTL reads a Go duration ("90s", "1h30m") with days and weeks added:
// "7d", "1d12h", "2w".
func parseTTL(s string) (time.Duration, error) {
	bad := fmt.Errorf("invalid duration %q (try 30m, 12h or 7d)", s)
	var total time.Duration
	rest := s
	for _, u := range []struct {
		unit string
		d    time.Duration
	}{{"w", 7 * 24 * time.Hour}, {"d", 24 * time.Hour}} {
		i := strings.Index(rest, u.unit)
		if i < 0 {
			continue
		}
		n, err := strconv.ParseFloat(rest[:i], 64)
		if err != nil || n < 0 {
			return 0, bad
		}
		total += time.Duration(n * float64(u.d))
		rest = rest[i+1:]
	}
	if rest != "" {
		d, err := time.ParseDuration(rest)
		if err != nil || d < 0 {
			return 0, bad
		}
		total += d
	} else if rest == s {
		return 0, bad
	}
	return total, nil
}

func fmtAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

func fmtSize(n int) string {
	switch {
	case n < 1024:
		return strconv.Itoa(n) + "B"
	case n < 1024*1024:
		return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + "K"
	}
	return strconv.FormatFloat(float64(n)/(1024*1024), 'f', 1, 64) + "M"
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
