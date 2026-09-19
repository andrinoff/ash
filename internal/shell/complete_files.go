package shell

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/syntax"
)

// fileMode selects which files are offered.
type fileMode uint8

const (
	filesAll    fileMode = 0
	filesDirs   fileMode = 1 << iota // directories only
	filesExec                        // executables and directories
	filesCDPath                      // also directories in $CDPATH
)

// fileTok is the part of a token that names a file.
type fileTok struct {
	prefix     string // raw text in front of it (e.g. "--file=")
	raw        string // its raw text
	value      string // its value
	startQuote byte   // quote open where it starts
	slash      int    // index in raw just past the last '/', -1 if none
	slashVal   int    // ... and in value
	slashQuote byte   // quote open there
	expFirst   int    // index in raw of the first expansion, -1 if none
	expEnd     int    // index in raw just past the last expansion
	globLast   int    // index in raw of the last unquoted wildcard, -1 if none
	tilde      bool   // starts with an unquoted ~
}

// wholeFileTok is the file token for the whole token at the cursor.
func (r *compRun) wholeFileTok() fileTok {
	l := r.line
	t := l.tok
	ft := fileTok{raw: l.raw(), value: t.value, slash: -1, expFirst: -1, expEnd: -1, globLast: -1}
	if t.slashPos >= 0 {
		ft.slash, ft.slashVal, ft.slashQuote = t.slashPos-t.start, t.slashVal, t.slashQuote
	}
	if t.expFirst >= 0 {
		ft.expFirst, ft.expEnd = t.expFirst-t.start, t.expEnd-t.start
	}
	if t.globLast >= 0 {
		ft.globLast = t.globLast - t.start
	}
	ft.tilde = strings.HasPrefix(ft.raw, "~")
	return ft
}

// sepFileTok is the file token after the last '=' or ':' of the token.
func (r *compRun) sepFileTok() (fileTok, bool) {
	l := r.line
	t := l.tok
	if t.sepPos < 0 {
		return fileTok{}, false
	}
	whole := r.wholeFileTok()
	sep := t.sepPos - t.start
	ft := fileTok{
		prefix:     whole.raw[:sep],
		raw:        whole.raw[sep:],
		value:      t.value[t.sepVal:],
		startQuote: t.sepQuote,
		slash:      -1,
		expFirst:   -1,
		expEnd:     -1,
		globLast:   -1,
	}
	if whole.slash >= sep {
		ft.slash, ft.slashVal, ft.slashQuote = whole.slash-sep, whole.slashVal-t.sepVal, whole.slashQuote
	}
	if whole.expEnd > sep {
		ft.expFirst, ft.expEnd = max(whole.expFirst-sep, 0), whole.expEnd-sep
	}
	if whole.globLast >= sep {
		ft.globLast = whole.globLast - sep
	}
	ft.tilde = ft.startQuote == 0 && strings.HasPrefix(ft.raw, "~")
	return ft, true
}

// completeFileToken completes the token as a file name. With seps, a
// token like --file=x, VAR=x or host:x is also completed after its last
// '=' or ':'.
func (r *compRun) completeFileToken(col *collector, mode fileMode, seps bool) {
	whole := r.wholeFileTok()
	fromStart := true
	if seps {
		if ft, ok := r.sepFileTok(); ok {
			r.completeFiles(col, ft, mode)
			fromStart = !strings.HasPrefix(r.line.tok.value, "-")
		}
	}
	if fromStart {
		r.completeFiles(col, whole, mode)
	}
}

// completeFiles offers the files that ft can complete to.
func (r *compRun) completeFiles(col *collector, ft fileTok, mode fileMode) {
	l := r.line
	if ft.tilde && ft.slash < 0 {
		r.completeUsersIn(col, ft)
		return
	}
	dirRaw, baseRaw := "", ft.raw
	needle := ft.value
	baseQuote := ft.startQuote
	dirVal := ""
	if ft.slash >= 0 {
		dirRaw, baseRaw = ft.raw[:ft.slash], ft.raw[ft.slash:]
		needle = ft.value[ft.slashVal:]
		baseQuote = ft.slashQuote
		v, ok := r.dirValue(ft)
		if !ok {
			return
		}
		dirVal = v
	}
	baseStart := max(ft.slash, 0)
	if ft.globLast >= baseStart {
		r.completeGlob(col, ft)
		return
	}
	if ft.expEnd > baseStart {
		// an expansion in the name itself ($x.txt): expand it
		v, ok := r.expandRaw(opener(baseQuote)+baseRaw, l.quote)
		if !ok {
			return
		}
		needle = v
	}
	abs := dirVal
	if abs == "" {
		abs = r.sh.dir
	} else if !filepath.IsAbs(abs) {
		abs = filepath.Join(r.sh.dir, abs)
	}
	in := insertion{
		prefix:     ft.prefix + dirRaw,
		raw:        baseRaw,
		needle:     needle,
		quote:      l.quote,
		startQuote: baseQuote,
		escape:     l.escape,
		atStart:    ft.prefix == "" && dirRaw == "",
		closeQuote: r.closeQuote,
	}
	found := r.addDirEntries(col, abs, &in, mode, "")
	if !found && dirVal != "" && ft.expFirst < 0 {
		// /u/l/b: match each directory of the path loosely
		if _, err := os.Stat(abs); err != nil {
			r.completeFuzzyPath(col, ft, dirVal, needle, mode)
		}
	}
	if mode&filesCDPath != 0 && dirRaw == "" && !strings.HasPrefix(needle, "/") && !strings.HasPrefix(needle, ".") && !ft.tilde {
		for _, cp := range r.sh.GetList("CDPATH") {
			for _, d := range strings.Split(cp, ":") {
				if d == "" || d == "." {
					continue
				}
				if !filepath.IsAbs(d) {
					d = filepath.Join(r.sh.dir, d)
				}
				if d == r.sh.dir {
					continue
				}
				r.addDirEntries(col, d, &in, filesDirs, "")
			}
		}
	}
}

// addDirEntries adds the entries of dir that match in.needle. It reports
// whether the directory could be read.
func (r *compRun) addDirEntries(col *collector, dir string, in *insertion, mode fileMode, shown string) bool {
	ents, ok := r.c.dirs.list(dir)
	if !ok {
		return false
	}
	needle := in.needle
	m := newMatcher(needle, !strings.HasPrefix(needle, "-"))
	hidden := strings.HasPrefix(needle, ".")
	for _, e := range ents {
		name := e.name
		if name[0] == '.' && !hidden {
			continue
		}
		tier := m.tier(name, col.max())
		if tier == tierNone {
			continue
		}
		isDir := e.typ.IsDir()
		if !isDir && e.typ&fs.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if !isDir {
			if mode&filesDirs != 0 {
				continue
			}
			if mode&filesExec != 0 && !isExecutable(filepath.Join(dir, name)) {
				continue
			}
		}
		val, kind := name, "file"
		if isDir {
			val, kind = name+"/", "dir"
		}
		var text string
		if shown != "" {
			// a loosely matched path replaces the whole token
			text = in.prefix + escapeFor(shown+val, in.startQuote, in.prefix == "")
			if q := closer(in.startQuote); q != 0 && (!isDir || in.closeQuote) {
				text += string(q)
			}
			tier = max(tier, tierPrefixFold)
		} else {
			text = in.text(val, tier, !isDir)
		}
		col.add(tier, shown+val, editor.Candidate{Text: text, Display: shown + val, NoSpace: isDir, Kind: kind}, false)
	}
	if needle == ".." && mode&filesExec == 0 && shown == "" {
		col.add(tierPrefix, "../", editor.Candidate{Text: in.text("../", tierPrefix, false), Display: "../", NoSpace: true, Kind: "dir"}, false)
	}
	return true
}

func isExecutable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0
}

// dirValue returns the directory part of a file token, expanded.
func (r *compRun) dirValue(ft fileTok) (string, bool) {
	if (ft.expFirst < 0 || ft.expFirst >= ft.slash) && !ft.tilde {
		return ft.value[:ft.slashVal], true
	}
	src := ft.raw[:ft.slash]
	if ft.startQuote != 0 {
		src = opener(ft.startQuote) + src
	}
	return r.expandRaw(src, ft.slashQuote)
}

// opener returns the text that opens a quote.
func opener(q byte) string {
	switch q {
	case 0:
		return ""
	case '$':
		return "$'"
	}
	return string(q)
}

// expandRaw expands a piece of a word typed on the command line, closing
// the quote open at its end. Substitutions are not run: such words are not
// completed.
func (r *compRun) expandRaw(src string, quote byte) (string, bool) {
	if q := closer(quote); q != 0 {
		src += string(q)
	}
	f, err := syntax.Parse("x " + src)
	if err != nil || len(f.Stmts) != 1 {
		return "", false
	}
	call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) != 2 || !safeParts(call.Args[1].Parts) {
		return "", false
	}
	vals, err := r.sh.expandWord(call.Args[1], r.io, expNoGlob)
	if err != nil || len(vals) != 1 {
		return "", false
	}
	return vals[0], true
}

// safeParts reports whether expanding the parts runs nothing and changes
// nothing: only literals, quotes, ~ and plain $variables.
func safeParts(parts []syntax.WordPart) bool {
	for _, p := range parts {
		switch p := p.(type) {
		case *syntax.Lit, *syntax.Quoted, *syntax.Tilde:
		case *syntax.DblQuoted:
			if !safeParts(p.Parts) {
				return false
			}
		case *syntax.ParamExp:
			if p.Op != syntax.OpNone || p.Indirect || p.Index != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// completeFuzzyPath completes a path whose directories don't exist as
// typed by matching each of them loosely: /u/l/b -> /usr/local/bin/.
func (r *compRun) completeFuzzyPath(col *collector, ft fileTok, dirVal, needle string, mode fileMode) {
	type dir struct{ abs, shown string }
	var cur []dir
	segs := strings.Split(strings.TrimSuffix(dirVal, "/"), "/")
	switch {
	case strings.HasPrefix(dirVal, "/"):
		cur = []dir{{"/", "/"}}
		segs = segs[1:]
	case strings.HasPrefix(dirVal, "~/") && ft.tilde:
		return // handled by expansion; the home directory exists
	default:
		cur = []dir{{r.sh.dir, ""}}
	}
	if len(segs) > 16 {
		return
	}
	for _, seg := range segs {
		var next []dir
		for _, c := range cur {
			if seg == "" || seg == "." || seg == ".." {
				next = append(next, dir{filepath.Join(c.abs, seg), c.shown + seg + "/"})
				continue
			}
			if st, err := os.Stat(filepath.Join(c.abs, seg)); err == nil && st.IsDir() {
				next = append(next, dir{filepath.Join(c.abs, seg), c.shown + seg + "/"})
				continue
			}
			ents, ok := r.c.dirs.list(c.abs)
			if !ok {
				continue
			}
			m := newMatcher(seg, true)
			best := tierNone - 1
			var matches []string
			for _, e := range ents {
				if e.name[0] == '.' && seg[0] != '.' {
					continue
				}
				t := m.tier(e.name, best)
				if t == tierNone || t > best {
					continue
				}
				isDir := e.typ.IsDir()
				if !isDir && e.typ&fs.ModeSymlink != 0 {
					st, err := os.Stat(filepath.Join(c.abs, e.name))
					isDir = err == nil && st.IsDir()
				}
				if !isDir {
					continue
				}
				if t < best {
					best = t
					matches = matches[:0]
				}
				matches = append(matches, e.name)
			}
			for _, n := range matches {
				next = append(next, dir{filepath.Join(c.abs, n), c.shown + n + "/"})
			}
		}
		if len(next) > 64 {
			next = next[:64]
		}
		cur = next
		if len(cur) == 0 {
			return
		}
	}
	in := insertion{prefix: ft.prefix, needle: needle, startQuote: ft.startQuote, quote: r.line.quote, closeQuote: r.closeQuote}
	for _, c := range cur {
		r.addDirEntries(col, c.abs, &in, mode, c.shown)
	}
}

// completeGlob offers the files matching a wildcard pattern; they replace
// the token.
func (r *compRun) completeGlob(col *collector, ft fileTok) {
	// re-read the token as a pattern so quoted wildcards stay literal
	src := ft.raw
	if ft.startQuote != 0 {
		src = opener(ft.startQuote) + src
	}
	if q := closer(r.line.quote); q != 0 {
		src += string(q)
	}
	f, err := syntax.Parse("x " + src)
	if err != nil || len(f.Stmts) != 1 {
		return
	}
	call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) != 2 || !safeParts(call.Args[1].Parts) {
		return
	}
	pat, err := r.sh.expandPattern(call.Args[1], r.io, false)
	if err != nil || pat == "" {
		return
	}
	if !strings.HasSuffix(pat, "*") {
		pat += "*"
	}
	for _, mtc := range r.globMatches(pat) {
		val := mtc
		isDir := false
		p := mtc
		if !filepath.IsAbs(p) {
			p = filepath.Join(r.sh.dir, p)
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			isDir = true
			if !strings.HasSuffix(val, "/") {
				val += "/"
			}
		}
		text := ft.prefix + escapeFor(val, ft.startQuote, ft.prefix == "")
		if q := closer(ft.startQuote); q != 0 && (!isDir || r.closeQuote) {
			text += string(q)
		}
		kind := "file"
		if isDir {
			kind = "dir"
		}
		col.add(tierPrefixFold, val, editor.Candidate{Text: text, Display: val, NoSpace: isDir, Kind: kind}, false)
	}
}

// Limits on expanding a wildcard for completion, which the line editor
// waits for: "**" in a big tree would otherwise take minutes.
var (
	globMaxVisits  = 100000 // directory entries looked at
	globMaxMatches = 4096
	globMaxTime    = 500 * time.Millisecond
)

// globMatches expands the pattern like sh.glob, but stops at the limits
// above or when the shell is interrupted, and leaves out the empty match
// ("**" matching no directory at all).
func (r *compRun) globMatches(pat string) []string {
	sh := r.sh
	var segs []string
	for _, s := range strings.Split(strings.TrimPrefix(pat, "/"), "/") {
		if s != "**" && strings.Contains(s, "**") {
			// fish-style "**.go" means "**/*.go"
			segs = append(segs, "**", strings.ReplaceAll(s, "**", "*"))
			continue
		}
		segs = append(segs, s)
	}
	base, prefix := sh.dir, ""
	if strings.HasPrefix(pat, "/") {
		base, prefix = "/", "/"
	}
	deadline := time.Now().Add(globMaxTime)
	visits, stopped := 0, false
	var out []string
	seen := map[string]bool{}
	readDir := func(dir string) []os.DirEntry {
		if stopped || visits >= globMaxVisits || len(out) >= globMaxMatches || sh.interrupted() || time.Now().After(deadline) {
			stopped = true
			return nil
		}
		ents, _ := os.ReadDir(dir)
		visits += len(ents)
		return ents
	}
	add := func(m string) {
		if m != "" && !seen[m] && len(out) < globMaxMatches {
			seen[m] = true
			out = append(out, m)
		}
	}
	var walk func(dir, rel string, i int)
	walk = func(dir, rel string, i int) {
		if stopped {
			return
		}
		if i == len(segs) {
			add(rel)
			return
		}
		join := func(name string) (string, string) {
			if rel == "" {
				return filepath.Join(dir, name), prefix + name
			}
			return filepath.Join(dir, name), rel + "/" + name
		}
		switch seg := segs[i]; {
		case seg == "":
			// trailing slash or "//"
			if i < len(segs)-1 {
				walk(dir, rel, i+1)
			} else if rel != "" {
				add(rel + "/")
			}
		case seg == "**":
			walk(dir, rel, i+1) // no directory at all
			var rec func(d, rl string, depth int)
			rec = func(d, rl string, depth int) {
				if depth > 64 {
					return
				}
				for _, e := range readDir(d) {
					// (hidden and symlinked directories are not entered)
					if name := e.Name(); name[0] != '.' && e.IsDir() && !stopped {
						nd, nr := filepath.Join(d, name), prefix+name
						if rl != "" {
							nr = rl + "/" + name
						}
						walk(nd, nr, i+1)
						rec(nd, nr, depth+1)
					}
				}
			}
			rec(dir, rel, 0)
		case !hasMeta(seg):
			d, rl := join(unescapePattern(seg))
			if i < len(segs)-1 {
				walk(d, rl, i+1)
			} else if _, err := os.Lstat(d); err == nil {
				add(rl)
			}
		default:
			dotOK := strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, `\.`)
			for _, e := range readDir(dir) {
				name := e.Name()
				if stopped || name[0] == '.' && !dotOK || !matchPattern(seg, name) {
					continue
				}
				d, rl := join(name)
				if i < len(segs)-1 && !e.IsDir() {
					if st, err := os.Stat(d); err != nil || !st.IsDir() {
						continue
					}
				}
				walk(d, rl, i+1)
			}
		}
	}
	walk(base, "", 0)
	return out
}

// userHome is a user name and home directory.
type userHome struct{ name, home string }

func (r *compRun) userList() []userHome {
	u := &r.c.users
	u.once.Do(func() {
		seen := map[string]bool{}
		if name := os.Getenv("USER"); name != "" {
			u.list = append(u.list, userHome{name, r.sh.HomeDir()})
			seen[name] = true
		}
		f, err := os.Open("/etc/passwd")
		if err != nil {
			return
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if line == "" || line[0] == '#' {
				continue
			}
			fields := strings.Split(line, ":")
			if len(fields) < 6 || seen[fields[0]] {
				continue
			}
			seen[fields[0]] = true
			u.list = append(u.list, userHome{fields[0], fields[5]})
		}
	})
	return u.list
}

// completeUsers completes ~user at the start of a token.
func (r *compRun) completeUsers(col *collector) {
	r.completeUsersIn(col, r.wholeFileTok())
}

func (r *compRun) completeUsersIn(col *collector, ft fileTok) {
	if !strings.HasPrefix(ft.value, "~") {
		return
	}
	in := insertion{prefix: ft.prefix, raw: ft.raw, needle: ft.value, quote: r.line.quote, escape: r.line.escape,
		atStart: ft.prefix == "", closeQuote: r.closeQuote}
	typed := ft.value[1:]
	m := newMatcher(typed, true)
	for _, u := range r.userList() {
		if strings.HasPrefix(u.name, "_") && !strings.HasPrefix(typed, "_") {
			continue
		}
		tier := m.tier(u.name, col.max())
		if tier == tierNone {
			continue
		}
		val := "~" + u.name + "/"
		var text string
		if tier == tierPrefix {
			text = in.text(val, tier, false)
		} else {
			text = ft.prefix + "~" + escapeFor(u.name, 0, false) + "/"
		}
		col.add(tier, val, editor.Candidate{Text: text, Display: val, Description: u.home, NoSpace: true, Kind: "dir"}, false)
	}
}

// dirCache remembers directory listings, keyed by path and validated by
// the directory's modification time, so repeated completions in the same
// directory don't read it again.
type dirCache struct {
	mu   sync.Mutex
	m    map[string]*dirListing
	keys []string
}

type dirListing struct {
	mod  time.Time
	size int64
	read time.Time // when it was read
	ents []dirEnt
}

// dirTimeSlack is how long after its last change a directory must have
// been read for the listing to be reused while its time stays the same.
// Before that, another change could keep the same time: timestamps tick
// every second on HFS+ and every two seconds on FAT.
const dirTimeSlack = 2 * time.Second

type dirEnt struct {
	name string
	typ  fs.FileMode
}

const dirCacheSize = 32

// list returns the entries of dir and whether it could be read.
func (d *dirCache) list(dir string) ([]dirEnt, bool) {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return nil, false
	}
	d.mu.Lock()
	if l := d.m[dir]; l != nil && l.mod.Equal(st.ModTime()) && l.size == st.Size() && l.read.Sub(l.mod) > dirTimeSlack {
		d.mu.Unlock()
		return l.ents, true
	}
	d.mu.Unlock()
	read := time.Now()
	f, err := os.Open(dir)
	if err != nil {
		return nil, false
	}
	des, err := f.ReadDir(-1)
	f.Close()
	if err != nil && len(des) == 0 {
		return nil, false
	}
	ents := make([]dirEnt, 0, len(des))
	for _, e := range des {
		if e.Name() == "" {
			continue
		}
		ents = append(ents, dirEnt{name: e.Name(), typ: e.Type()})
	}
	d.mu.Lock()
	if d.m == nil {
		d.m = map[string]*dirListing{}
	}
	if _, ok := d.m[dir]; !ok {
		d.keys = append(d.keys, dir)
		if len(d.keys) > dirCacheSize {
			delete(d.m, d.keys[0])
			d.keys = d.keys[1:]
		}
	}
	d.m[dir] = &dirListing{mod: st.ModTime(), size: st.Size(), read: read, ents: ents}
	d.mu.Unlock()
	return ents, true
}
