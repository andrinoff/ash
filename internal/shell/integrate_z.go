package shell

// A native port of rupa/z and jethrokuan/z (the fish version). The data
// file is shared with them: one "path|rank|time" line per directory,
// numbers printed the way awk prints them, so ash and fish can take turns
// updating the same file.

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func init() {
	registerIntegration(integration{"z", "", "jump to frecent directories (built-in rupa/z)", setupZ})
	builtins["z"] = bZ
	builtinHelp["z"] = "jump to a frecent directory (integrate z or zoxide)"
}

// zMaxScore is the total rank above which every rank is aged by 0.99.
const zMaxScore = 9000

// zEntry is one line of the data file. The texts keep numbers exactly as
// read, so lines we don't touch are written back byte for byte.
type zEntry struct {
	path     string
	rank     float64
	rankText string
	time     float64
	timeText string
}

// zStore serializes access to the data file and batches the additions
// made on directory changes, which are written in the background.
type zStore struct {
	mu      sync.Mutex // guards pending and running
	pending []zPending
	running bool
	file    sync.Mutex // held while reading or rewriting the data file

	now func() time.Time // the clock (tests replace it)
}

type zPending struct {
	file, dir string
	now       int64
}

func (z *zStore) clock() time.Time {
	if z.now != nil {
		return z.now()
	}
	return time.Now()
}

// bZ answers `z` with whichever tool was integrated last; without one it
// behaves like any unknown command (or runs a `z` program from $PATH).
func bZ(sh *Shell, io *IO, args []string) int {
	in := sh.shared.integr
	in.mu.Lock()
	jump := in.jump
	in.mu.Unlock()
	if jump == nil {
		return sh.runExternal(args, io, nil, nil)
	}
	return jump(sh, io, args)
}

func setupZ(sh *Shell, io *IO, exe string) string {
	in := sh.shared.integr
	in.mu.Lock()
	in.jump = (*Shell).zJump
	in.mu.Unlock()
	if in.once("z") {
		sh.OnDirChanged(func(sh *Shell, dir string) { sh.zRecord(dir) })
	}
	return "z jumps to frecent directories, recorded in " + sh.zDataFile()
}

// zDataFile is $Z_DATA, or $Z_DATA_DIR/data, where Z_DATA_DIR defaults to
// $XDG_DATA_HOME/z or ~/.local/share/z (jethrokuan/z's defaults).
func (sh *Shell) zDataFile() string {
	if f := sh.Get("Z_DATA"); f != "" {
		return sh.absPath(sh.expandTildeString(f))
	}
	dir := sh.Get("Z_DATA_DIR")
	if dir == "" {
		if x := sh.Get("XDG_DATA_HOME"); x != "" {
			dir = filepath.Join(x, "z")
		} else {
			dir = filepath.Join(sh.HomeDir(), ".local", "share", "z")
		}
	}
	return filepath.Join(sh.absPath(sh.expandTildeString(dir)), "data")
}

// zRecord queues dir for the data file, unless it is $HOME, /, matches a
// regex in $Z_EXCLUDE, or the shell is in private mode.
func (sh *Shell) zRecord(dir string) {
	in := sh.shared.integr
	if _, ok := in.on("z"); !ok || sh.privateMode() {
		return
	}
	if !sh.zWanted(dir) {
		return
	}
	z := &in.z
	p := zPending{file: sh.zDataFile(), dir: dir, now: z.clock().Unix()}
	z.mu.Lock()
	z.pending = append(z.pending, p)
	start := !z.running
	z.running = true
	z.mu.Unlock()
	if start {
		in.background(z.drain)
	}
}

func (sh *Shell) zWanted(dir string) bool {
	if dir == "" || dir == "/" || dir == filepath.Clean(sh.HomeDir()) {
		return false
	}
	in := sh.shared.integr
	for _, pat := range sh.GetList("Z_EXCLUDE") {
		if re := in.regexp(pat); re != nil && re.MatchString(dir) {
			return false
		}
	}
	return true
}

// regexp compiles and caches a user pattern (Z_EXCLUDE); invalid patterns
// give nil.
func (in *integrations) regexp(pat string) *regexp.Regexp {
	in.mu.Lock()
	defer in.mu.Unlock()
	if re, ok := in.regexps[pat]; ok {
		return re
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		re = nil
	}
	if in.regexps == nil {
		in.regexps = map[string]*regexp.Regexp{}
	}
	in.regexps[pat] = re
	return re
}

// drain writes queued additions until none are left.
func (z *zStore) drain() {
	for {
		z.file.Lock()
		z.mu.Lock()
		batch := z.pending
		z.pending = nil
		if len(batch) == 0 {
			z.running = false
		}
		z.mu.Unlock()
		if len(batch) == 0 {
			z.file.Unlock()
			return
		}
		z.apply(batch)
		z.file.Unlock()
	}
}

// lock takes the data file for reading or rewriting, first writing any
// queued additions so that `cd x; z -l` sees x.
func (z *zStore) lock() {
	z.file.Lock()
	z.mu.Lock()
	batch := z.pending
	z.pending = nil
	z.mu.Unlock()
	if len(batch) > 0 {
		z.apply(batch)
	}
}

func (z *zStore) unlock() { z.file.Unlock() }

// apply adds each pending directory, one file rewrite per data file. A
// data file that can't be read is left alone.
func (z *zStore) apply(batch []zPending) {
	for len(batch) > 0 {
		file := batch[0].file
		entries, err := zRead(file)
		var rest []zPending
		for _, p := range batch {
			if p.file != file {
				rest = append(rest, p)
			} else if err == nil {
				entries = zAdd(entries, p.dir, p.now)
			}
		}
		if err == nil {
			zWrite(file, entries)
		}
		batch = rest
	}
}

// zAdd is rupa/z's update: entries with rank below 1 are dropped, the
// path gets rank+1 (or 1 when new) and time now, and when the ranks read
// add up to more than 9000 every rank is multiplied by 0.99.
func zAdd(entries []zEntry, path string, now int64) []zEntry {
	nowText := strconv.FormatInt(now, 10)
	out := make([]zEntry, 0, len(entries)+1)
	seen := make(map[string]int, len(entries)+1)
	count := 0.0
	for _, e := range entries {
		if !(e.rank >= 1) {
			continue
		}
		count += e.rank
		if e.path == path {
			e.rank++
			e.rankText = awkNumber(e.rank)
			e.time, e.timeText = float64(now), nowText
		}
		if i, ok := seen[e.path]; ok {
			out[i] = e
			continue
		}
		seen[e.path] = len(out)
		out = append(out, e)
	}
	if _, ok := seen[path]; !ok {
		out = append(out, zEntry{path: path, rank: 1, rankText: "1", time: float64(now), timeText: nowText})
	}
	if count > zMaxScore {
		for i := range out {
			out[i].rank = float64(0.99 * out[i].rank)
			out[i].rankText = awkNumber(out[i].rank)
		}
	}
	return out
}

// awkNumber formats a number the way awk prints one: integers plainly,
// anything else with %.6g.
func awkNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', 6, 64)
}

// zParse reads data file lines. The rank and time are the last two
// '|'-separated fields, so a path containing '|' survives; lines whose
// numbers don't parse are skipped.
func zParse(data []byte) []zEntry {
	entries := make([]zEntry, 0, bytes.Count(data, []byte{'\n'})+1)
	for len(data) > 0 {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			line, data = data, nil
		}
		j := bytes.LastIndexByte(line, '|')
		if j <= 0 {
			continue
		}
		i := bytes.LastIndexByte(line[:j], '|')
		if i <= 0 {
			continue
		}
		e := zEntry{path: string(line[:i]), rankText: string(line[i+1 : j]), timeText: string(line[j+1:])}
		var err1, err2 error
		e.rank, err1 = strconv.ParseFloat(strings.TrimSpace(e.rankText), 64)
		e.time, err2 = strconv.ParseFloat(strings.TrimSpace(e.timeText), 64)
		if err1 != nil || err2 != nil || !zFinite(e.rank) || !zFinite(e.time) {
			continue
		}
		entries = append(entries, e)
	}
	return entries
}

func zFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func zFormat(entries []zEntry) []byte {
	n := 0
	for _, e := range entries {
		n += len(e.path) + len(e.rankText) + len(e.timeText) + 3
	}
	b := make([]byte, 0, n)
	for _, e := range entries {
		b = append(b, e.path...)
		b = append(b, '|')
		b = append(b, e.rankText...)
		b = append(b, '|')
		b = append(b, e.timeText...)
		b = append(b, '\n')
	}
	return b
}

// zRead loads the data file; a missing file is empty.
func zRead(file string) ([]zEntry, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return zParse(data), nil
}

// zWrite replaces the data file through a temporary file and a rename, so
// a concurrent reader (or a crash) never sees half a file. Like rupa/z it
// follows a symlinked data file and leaves alone a file owned by someone
// else (as happens under sudo with $HOME kept).
func zWrite(file string, entries []zEntry) error {
	if fi, err := os.Lstat(file); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if r, err := filepath.EvalSymlinks(file); err == nil {
				file = r
				fi, err = os.Lstat(file)
				if err != nil {
					return err
				}
			}
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
			return fmt.Errorf("%s: owned by another user", file)
		}
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(file)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(zFormat(entries))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), file)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// zFrecent is rupa/z's frecency: rank weighted by how long ago the
// directory was visited. The conversions keep each step rounded like awk
// does (no fused multiply-add).
func zFrecent(rank, t, now float64) float64 {
	dx := now - t
	a := float64(0.0001 * dx)
	b := float64(a + 1)
	c := float64(b + 0.25)
	d := float64(3.75 / c)
	e := float64(10000 * rank)
	return math.Trunc(float64(e * d))
}

// zMatch is a candidate directory and its score.
type zMatch struct {
	path  string
	score float64
	text  string // the score as z prints it
}

// zSearch ranks the entries matching re (rupa/z's query): typ is "" for
// frecency, "rank" or "recent". It returns the eligible matches in data
// file order, the best one and their common root ("" if none).
func zSearch(entries []zEntry, re *regexp.Regexp, typ string, now float64, exists func(string) bool) (matches []zMatch, best, common string) {
	index := map[string]int{}
	hi := -9999999999.0
	for _, e := range entries {
		if !re.MatchString(e.path) || !exists(e.path) {
			continue
		}
		m := zMatch{path: e.path}
		switch typ {
		case "rank":
			m.score, m.text = e.rank, e.rankText
		case "recent":
			m.score = float64(e.time - now)
			m.text = awkNumber(m.score)
		default:
			m.score = zFrecent(e.rank, e.time, now)
			m.text = awkNumber(m.score)
		}
		if i, ok := index[e.path]; ok {
			matches[i] = m
		} else {
			index[e.path] = len(matches)
			matches = append(matches, m)
		}
		// A score of 0 counts as no match in z (awk truthiness).
		if m.score != 0 && m.score > hi {
			best, hi = e.path, m.score
		}
	}
	out := matches[:0]
	for _, m := range matches {
		if m.score != 0 {
			out = append(out, m)
		}
	}
	return out, best, zCommon(out)
}

// zCommon is the shortest match when every match starts with it.
func zCommon(matches []zMatch) string {
	short := ""
	for _, m := range matches {
		if short == "" || len(m.path) < len(short) {
			short = m.path
		}
	}
	if short == "" || short == "/" {
		return ""
	}
	for _, m := range matches {
		if !strings.HasPrefix(m.path, short) {
			return ""
		}
	}
	return short
}

const zUsage = `usage: z [-celrthx] [--] [regex...]

Jump to the most frecent directory whose path matches the regexes in
order. All-lowercase patterns match case-insensitively. With no regex,
list the directories.

  -c  only directories below the current one
  -e  print the match instead of going there
  -l  list matches and their scores
  -r  rank by how often directories were visited
  -t  rank by how recently directories were visited
  -x  forget the current directory
  -h  show this help

Directories are recorded in $Z_DATA (default ~/.local/share/z/data)
unless they match a regex in $Z_EXCLUDE.
`

// zJump is the z builtin of `integrate z`.
func (sh *Shell) zJump(io *IO, args []string) int {
	var (
		words                    []string
		typ                      string
		list, echo, here, forget bool
		clean, add               bool
	)
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			words = append(words, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "--") {
			switch a {
			case "--list":
				list = true
			case "--echo":
				echo = true
			case "--rank":
				typ = "rank"
			case "--recent":
				typ = "recent"
			case "--delete":
				forget = true
			case "--clean":
				clean = true
			case "--add":
				add = true
			case "--help":
				fmt.Fprint(io.Out, zUsage)
				return 0
			default:
				fmt.Fprintf(io.Err, "z: unknown option %s\n", a)
				return 2
			}
			continue
		}
		if len(a) < 2 || a[0] != '-' {
			words = append(words, a)
			continue
		}
		for _, c := range a[1:] {
			switch c {
			case 'c':
				here = true
			case 'e':
				echo = true
			case 'l':
				list = true
			case 'r':
				typ = "rank"
			case 't':
				typ = "recent"
			case 'x':
				forget = true
			case 'h':
				fmt.Fprint(io.Out, zUsage)
				return 0
			default:
				fmt.Fprintf(io.Err, "z: unknown option -%c\n", c)
				return 2
			}
		}
	}

	in := sh.shared.integr
	z := &in.z
	file := sh.zDataFile()
	switch {
	case add:
		// rupa/z's --add, for scripts and importing.
		now := z.clock().Unix()
		z.lock()
		defer z.unlock()
		entries, err := zRead(file)
		if err != nil {
			fmt.Fprintf(io.Err, "z: %s\n", errText(err))
			return 1
		}
		for _, w := range words {
			if d := filepath.Clean(sh.absPath(w)); sh.zWanted(d) {
				entries = zAdd(entries, d, now)
			}
		}
		return zSave(io, file, entries)
	case forget || clean:
		z.lock()
		entries, err := zRead(file)
		if err == nil {
			kept := make([]zEntry, 0, len(entries))
			for _, e := range entries {
				if forget && e.path == sh.dir || clean && !integIsDir(e.path) {
					continue
				}
				kept = append(kept, e)
			}
			if len(kept) != len(entries) {
				err = zWrite(file, kept)
			}
		}
		z.unlock()
		if err != nil {
			fmt.Fprintf(io.Err, "z: %s\n", errText(err))
			return 1
		}
		if clean {
			fmt.Fprintf(io.Out, "%s cleaned!\n", file)
		}
		if len(words) == 0 {
			return 0
		}
	}

	// rupa/z joins the words with spaces and turns each space into ".*".
	query := strings.ReplaceAll(strings.Join(words, " "), " ", ".*")
	if query == "" {
		list = true
	}
	// "If we hit enter on a completion just go there" (or print it: -e).
	if !list && len(words) > 0 {
		if last := words[len(words)-1]; strings.HasPrefix(last, "/") && integIsDir(last) {
			if echo {
				fmt.Fprintln(io.Out, last)
				return 0
			}
			return sh.changeDir(last, io, false)
		}
	}
	pattern := query
	if here {
		// The current directory and what is below it; /a/b must not let
		// its sibling /a/bc through.
		pattern = "^" + regexp.QuoteMeta(strings.TrimSuffix(sh.dir, "/")) + "(/|$)"
		if query != "" {
			pattern += ".*" + query
		}
	}

	z.lock()
	entries, err := zRead(file)
	z.unlock()
	if err != nil {
		fmt.Fprintf(io.Err, "z: %s\n", errText(err))
		return 1
	}
	matches, target, common, err := zFind(entries, pattern, query, typ, float64(z.clock().Unix()), integIsDir)
	if err != nil {
		fmt.Fprintf(io.Err, "z: invalid pattern '%s'\n", query)
		return 2
	}
	if list {
		if len(matches) == 0 {
			return 1
		}
		io.Out.WriteString(zListing(matches, common))
		return 0
	}
	if target == "" {
		fmt.Fprintf(io.Err, "z: '%s' did not match any results\n", strings.Join(words, " "))
		return 1
	}
	if echo {
		fmt.Fprintln(io.Out, target)
		return 0
	}
	return sh.changeDir(target, io, false)
}

// zFind answers a z query. pattern is the whole regex; query is the
// user's part of it, which decides case: all lowercase matches ignoring
// case, anything else must match case first and falls back to ignoring it
// (as rupa/z does). The target is the best match, or in frecency mode the
// matches' common root when there is one.
func zFind(entries []zEntry, pattern, query, typ string, now float64, exists func(string) bool) (matches []zMatch, target, common string, err error) {
	exact, err := regexp.Compile(pattern)
	if err != nil {
		return nil, "", "", err
	}
	fold := regexp.MustCompile("(?i)" + pattern)
	if query == strings.ToLower(query) {
		matches, target, common = zSearch(entries, fold, typ, now, exists)
	} else {
		matches, target, common = zSearch(entries, exact, typ, now, exists)
		if target == "" {
			matches, target, common = zSearch(entries, fold, typ, now, exists)
		}
	}
	if common != "" && typ == "" {
		target = common
	}
	return matches, target, common, nil
}

// zListing is the output of z -l: the common root, then the matches by
// ascending score (`sort -n`, ties by line), so the best ends up last.
func zListing(matches []zMatch, common string) string {
	var b strings.Builder
	if common != "" {
		fmt.Fprintf(&b, "%-10s %s\n", "common:", common)
	}
	lines := make([]string, len(matches))
	order := make([]int, len(matches))
	for i, m := range matches {
		lines[i] = fmt.Sprintf("%-10s %s", m.text, m.path)
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		x, y := matches[order[a]], matches[order[b]]
		if x.score != y.score {
			return x.score < y.score
		}
		return lines[order[a]] < lines[order[b]]
	})
	for _, i := range order {
		b.WriteString(lines[i])
		b.WriteByte('\n')
	}
	return b.String()
}

func zSave(io *IO, file string, entries []zEntry) int {
	if err := zWrite(file, entries); err != nil {
		fmt.Fprintf(io.Err, "z: %s\n", errText(err))
		return 1
	}
	return 0
}
