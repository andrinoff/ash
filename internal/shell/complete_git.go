package shell

import (
	"bytes"
	"errors"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Completing git arguments runs git about a dozen times (branches, tags,
// remotes, aliases, recent commits...), one after another, on every Tab.
// What those commands print only changes when the repository's refs,
// HEAD or config do, so while a completion runs, the output of
// read-only git commands is kept in memory and reused until one of those
// files changes. And before each prompt, the git commands completion ran
// lately are run again in the background wherever the output is missing
// or out of date, so the first Tab doesn't wait for git either.

// gitCacheTTL bounds how long an entry is trusted, for what the
// fingerprint can't see (files a git config includes, relative dates).
const gitCacheTTL = time.Hour

// gitCacheMax is the most entries kept; when full, the cache starts over.
const gitCacheMax = 512

type gitCache struct {
	mu      sync.Mutex
	entries map[string]*gitCacheEntry
	used    []gitCommand // what completion ran lately, newest last, for warming
	warming bool
	file    string // where used is kept between shells, once loaded
	loaded  bool
}

// gitCommand is a git command line completion ran, kept so it can be run
// again ahead of time in another directory or after the repository
// changes.
type gitCommand struct {
	path string
	args []string
}

// gitWarmMax is how many recent git command lines are warmed.
const gitWarmMax = 32

type gitCacheEntry struct {
	fp      string // the repository's fingerprint when it ran
	created time.Time
	status  int
	out     []byte
	err     []byte
}

// gitCompleteCached runs git (at path) for completion code, or replays
// what an identical earlier run printed. ok is false when this command
// can't be cached and should run normally.
func (sh *Shell) gitCompleteCached(path string, args []string, io *IO) (status int, ok bool) {
	c := &sh.shared.complete.git
	if filepath.Base(path) != "git" {
		return 0, false
	}
	dir, cacheable := gitReadOnly(args)
	if !cacheable {
		return 0, false
	}
	switch {
	case dir == "":
		dir = sh.dir
	case !filepath.IsAbs(dir):
		dir = filepath.Join(sh.dir, dir)
	default:
		dir = filepath.Clean(dir)
	}
	env := sh.Environ()
	fp := sh.gitFingerprint(dir, env)
	if fp == "" {
		return 0, false // not in a repository we can watch
	}
	key := gitCacheKey(path, dir, env, args)
	now := time.Now()

	c.mu.Lock()
	e := c.entries[key]
	c.mu.Unlock()
	if e != nil && e.fp == fp && now.Sub(e.created) < gitCacheTTL && now.Sub(e.created) >= 0 {
		if len(e.out) > 0 && io.Out != nil {
			io.Out.Write(e.out)
		}
		if len(e.err) > 0 && io.Err != nil {
			io.Err.Write(e.err)
		}
		return e.status, true
	}

	out := &cacheTee{out: discardOr(io.Out), max: cachedMaxOutput}
	errOut := &cacheTee{out: discardOr(io.Err), max: 64 << 10}
	cio := io.clone()
	cio.Out = &Stream{W: out}
	cio.Err = &Stream{W: errOut}
	if io.Out != nil {
		cio.Out.broken = io.Out.broken
	}
	status = sh.runExternalAt(path, args, cio, nil, nil)
	o, okOut := out.take()
	eb, okErr := errOut.take()
	if status >= 126 || sh.interrupted() || !okOut || !okErr {
		return status, true
	}
	c.mu.Lock()
	if !c.loaded {
		c.load(filepath.Join(sh.CacheDir(), "git-complete"))
	}
	if c.entries == nil || len(c.entries) >= gitCacheMax {
		c.entries = map[string]*gitCacheEntry{}
	}
	c.entries[key] = &gitCacheEntry{
		fp: fp, created: now, status: status,
		out: slices.Clone(o), err: slices.Clone(eb),
	}
	added := c.remember(path, args)
	c.mu.Unlock()
	if added {
		c.save()
	}
	return status, true
}

// remember adds a command line to the ones warmed, and reports whether
// it is new. Called with mu held.
func (c *gitCache) remember(path string, args []string) bool {
	for i, u := range c.used {
		if u.path == path && slices.Equal(u.args, args) {
			c.used = append(append(c.used[:i:i], c.used[i+1:]...), u)
			return false
		}
	}
	c.used = append(c.used, gitCommand{path, slices.Clone(args)})
	if len(c.used) > gitWarmMax {
		c.used = c.used[len(c.used)-gitWarmMax:]
	}
	return true
}

// save writes the command lines to warm, so a new shell warms them
// before its first Tab: one per line, the program and then the words,
// each quoted.
func (c *gitCache) save() {
	c.mu.Lock()
	file := c.file
	var b strings.Builder
	for _, u := range c.used {
		b.WriteString(strconv.Quote(u.path))
		for _, a := range u.args {
			b.WriteString(" " + strconv.Quote(a))
		}
		b.WriteString("\n")
	}
	c.mu.Unlock()
	if file == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return
	}
	tmp := file + ".tmp" + strconv.Itoa(os.Getpid())
	if os.WriteFile(tmp, []byte(b.String()), 0o644) == nil {
		if os.Rename(tmp, file) != nil {
			os.Remove(tmp)
		}
	}
}

// load reads what save wrote, under the commands this shell already
// remembers. Called with mu held.
func (c *gitCache) load(file string) {
	c.file, c.loaded = file, true
	b, err := os.ReadFile(file)
	if err != nil {
		return
	}
	var read []gitCommand
	for line := range strings.Lines(string(b)) {
		words, err := unquoteWords(strings.TrimSpace(line))
		if err != nil || len(words) < 2 || filepath.Base(words[0]) != "git" {
			continue
		}
		if _, ro := gitReadOnly(words[1:]); !ro {
			continue // only ever warm read-only commands
		}
		read = append(read, gitCommand{words[0], words[1:]})
	}
	mine := c.used
	c.used = nil
	for _, u := range append(read, mine...) {
		c.remember(u.path, u.args)
	}
}

// WarmGitCompletions runs, in the background, the git commands completion
// ran lately whose output for the current directory is missing or out of
// date, so the first Tab at this prompt doesn't wait for git. Call it
// before reading a command line.
func (sh *Shell) WarmGitCompletions() {
	c := &sh.shared.complete.git
	c.mu.Lock()
	if !c.loaded {
		c.load(filepath.Join(sh.CacheDir(), "git-complete"))
	}
	if c.warming || len(c.used) == 0 {
		c.mu.Unlock()
		return
	}
	used := slices.Clone(c.used)
	c.warming = true
	c.mu.Unlock()
	dir, env := sh.dir, sh.Environ()
	go func() {
		defer func() {
			c.mu.Lock()
			c.warming = false
			c.mu.Unlock()
		}()
		fp := computeGitFingerprint(dir, env)
		if fp == "" {
			return
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for _, u := range used {
			if d, _ := gitReadOnly(u.args); d != "" {
				continue // -C: some other directory
			}
			key := gitCacheKey(u.path, dir, env, u.args)
			now := time.Now()
			c.mu.Lock()
			e := c.entries[key]
			c.mu.Unlock()
			if e != nil && e.fp == fp && now.Sub(e.created) < gitCacheTTL && now.Sub(e.created) >= 0 {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer func() { <-sem; wg.Done() }()
				c.warm(u, dir, env, key, fp, now)
			}()
		}
		wg.Wait()
	}()
}

// warm runs one git command line and keeps its output.
func (c *gitCache) warm(u gitCommand, dir string, env []string, key, fp string, now time.Time) {
	var out, errb bytes.Buffer
	cmd := exec.Command(u.path, u.args[1:]...)
	cmd.Args[0] = u.args[0]
	cmd.Dir = dir
	// no index refresh or other optional writes behind the user's back
	cmd.Env = append(slices.Clone(env), "GIT_OPTIONAL_LOCKS=0")
	cmd.Stdout, cmd.Stderr = &out, &errb
	status := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || !ee.Exited() {
			return
		}
		status = ee.ExitCode()
	}
	if out.Len() > cachedMaxOutput || errb.Len() > 64<<10 || status >= 126 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[key]; e != nil && e.fp == fp && !e.created.Before(now) {
		return // completion got there first
	}
	if c.entries == nil || len(c.entries) >= gitCacheMax {
		c.entries = map[string]*gitCacheEntry{}
	}
	c.entries[key] = &gitCacheEntry{fp: fp, created: now, status: status, out: out.Bytes(), err: errb.Bytes()}
}

// discardOr is s as a writer, or one that drops everything when s is nil.
func discardOr(s *Stream) writerFunc {
	if s == nil {
		return func(p []byte) (int, error) { return len(p), nil }
	}
	return s.Write
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// gitCacheKey names one git command: the program, where it runs, the
// environment it sees that changes what git does, and its words.
func gitCacheKey(path, dir string, env, args []string) string {
	var b strings.Builder
	field := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	}
	field(path)
	if st, err := os.Stat(path); err == nil {
		field(strconv.FormatInt(st.ModTime().UnixNano(), 10) + " " + strconv.FormatInt(st.Size(), 10))
	}
	field(dir)
	vars := []string{}
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "HOME=") ||
			strings.HasPrefix(kv, "XDG_CONFIG_HOME=") || strings.HasPrefix(kv, "LANG=") || strings.HasPrefix(kv, "LC_") {
			vars = append(vars, kv)
		}
	}
	slices.Sort(vars)
	for _, v := range vars {
		field(v)
	}
	for _, a := range args[1:] {
		field(a)
	}
	return b.String()
}

// gitReadOnly reports whether a git command line only reads what the
// fingerprint watches: refs, HEAD, config and objects. Commands
// that look at the work tree (status, diff, ls-files) or the network, or
// that change anything, are not cached. dir is the -C directory, if any.
func gitReadOnly(args []string) (dir string, ok bool) {
	i := 1
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--no-pager" || a == "-P" || a == "--no-optional-locks" || a == "--literal-pathspecs" ||
			a == "--no-replace-objects" || a == "--glob-pathspecs" || a == "--noglob-pathspecs" || a == "--icase-pathspecs":
			i++
		case a == "-c":
			i += 2
		case a == "-C":
			if i+1 >= len(args) || dir != "" {
				return "", false
			}
			dir = args[i+1]
			i += 2
		case a == "--version" || a == "version":
			return dir, i == len(args)-1
		case strings.HasPrefix(a, "--list-cmds="):
			return dir, i == len(args)-1
		case strings.HasPrefix(a, "-"):
			return "", false // --git-dir, --work-tree, --exec-path...
		default:
			return dir, gitSubReadOnly(a, args[i+1:])
		}
	}
	return "", false
}

func gitSubReadOnly(sub string, rest []string) bool {
	positional := 0
	for _, a := range rest {
		if !strings.HasPrefix(a, "-") {
			positional++
		}
	}
	has := func(flags ...string) bool {
		for _, a := range rest {
			for _, f := range flags {
				if a == f || strings.HasPrefix(f, "--") && strings.HasPrefix(a, f+"=") {
					return true
				}
			}
		}
		return false
	}
	switch sub {
	case "for-each-ref", "rev-parse", "show-ref", "describe", "rev-list", "merge-base", "log", "shortlog", "reflog", "var":
		if sub == "reflog" && positional > 0 && rest[0] != "show" {
			return false // reflog expire / delete
		}
		return !has("--edit-description")
	case "config":
		if len(rest) > 0 && (rest[0] == "get" || rest[0] == "list") {
			return true
		}
		return has("--get", "--get-all", "--get-regexp", "--get-urlmatch", "-l", "--list") &&
			!has("--unset", "--unset-all", "--add", "--replace-all", "--rename-section", "--remove-section", "-e", "--edit")
	case "tag":
		return !has("-d", "--delete", "-a", "--annotate", "-s", "--sign", "-u", "--local-user", "-f", "--force",
			"-m", "--message", "-F", "--file", "-e", "--edit", "-v", "--verify") &&
			(positional == 0 || has("-l", "--list"))
	case "branch":
		return !has("-d", "-D", "--delete", "-m", "-M", "--move", "-c", "-C", "--copy", "-f", "--force",
			"-u", "--set-upstream-to", "--unset-upstream", "--edit-description", "-t", "--track", "--no-track", "--create-reflog") &&
			(positional == 0 || has("-l", "--list"))
	case "remote":
		return len(rest) == 0 || len(rest) == 1 && (rest[0] == "-v" || rest[0] == "--verbose") ||
			len(rest) >= 1 && rest[0] == "get-url"
	case "stash":
		return len(rest) >= 1 && rest[0] == "list"
	case "symbolic-ref":
		return positional <= 1 && !has("-d", "--delete", "-m")
	case "help":
		return has("-a", "--all", "-g", "--guides", "--config")
	}
	return false
}

// gitFingerprint sums up the state of the repository dir is in: the
// mtimes and sizes of HEAD, the index, packed-refs, config, the stash and
// fetch heads, every directory under refs (a ref update renames a file
// into one), and the user's own git config. "" if dir is in no repository
// that can be found by looking for .git. Worked out once per completion.
func (sh *Shell) gitFingerprint(dir string, env []string) string {
	run := sh.shared.complete.active.Load()
	if run != nil {
		if fp, ok := run.gitFP[dir]; ok {
			return fp
		}
	}
	fp := computeGitFingerprint(dir, env)
	if run != nil {
		if run.gitFP == nil {
			run.gitFP = map[string]string{}
		}
		run.gitFP[dir] = fp
	}
	return fp
}

func computeGitFingerprint(dir string, env []string) string {
	getenv := func(name string) string {
		for i := len(env) - 1; i >= 0; i-- {
			if v, ok := strings.CutPrefix(env[i], name+"="); ok {
				return v
			}
		}
		return ""
	}
	gitDir := getenv("GIT_DIR")
	if gitDir != "" {
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dir, gitDir)
		}
	} else {
		gitDir = findGitDir(dir)
	}
	if gitDir == "" {
		return ""
	}
	common := gitDir
	if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		c := strings.TrimSpace(string(b))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitDir, c)
		}
		common = filepath.Clean(c)
	}
	if st, err := os.Stat(filepath.Join(common, "refs")); err != nil || !st.IsDir() {
		if _, err := os.Stat(filepath.Join(common, "reftable")); err != nil {
			return ""
		}
	}

	h := fnv.New64a()
	add := func(p string) {
		h.Write([]byte(p))
		if st, err := os.Stat(p); err == nil {
			h.Write([]byte{' '})
			h.Write([]byte(strconv.FormatInt(st.ModTime().UnixNano(), 10)))
			h.Write([]byte{' '})
			h.Write([]byte(strconv.FormatInt(st.Size(), 10)))
		}
		h.Write([]byte{0})
	}
	// not the mtime of gitDir itself: git status renames a new index into it
	h.Write([]byte(gitDir + "\x00" + common + "\x00"))
	for _, f := range []string{"HEAD", "FETCH_HEAD", "ORIG_HEAD", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "config.worktree", "logs/HEAD", "rebase-merge", "rebase-apply"} {
		add(filepath.Join(gitDir, f))
	}
	for _, f := range []string{"packed-refs", "config", "logs/refs/stash", "shallow", "worktrees", "reftable/tables.list"} {
		add(filepath.Join(common, f))
	}
	walkDirs := func(root string) {
		var walk func(string, int)
		walk = func(d string, depth int) {
			ents, err := os.ReadDir(d)
			add(d)
			if err != nil || depth > 16 {
				return
			}
			for _, e := range ents {
				if e.IsDir() {
					walk(filepath.Join(d, e.Name()), depth+1)
				}
			}
		}
		walk(root, 0)
	}
	walkDirs(filepath.Join(common, "refs"))
	if gitDir != common {
		walkDirs(filepath.Join(gitDir, "refs"))
	}
	// the user's git config: aliases, sorting, colors
	home := getenv("HOME")
	xdg := getenv("XDG_CONFIG_HOME")
	if xdg == "" && home != "" {
		xdg = filepath.Join(home, ".config")
	}
	for _, p := range []string{getenv("GIT_CONFIG_GLOBAL"), getenv("GIT_CONFIG_SYSTEM")} {
		if p != "" {
			add(p)
		}
	}
	if home != "" {
		add(filepath.Join(home, ".gitconfig"))
	}
	if xdg != "" {
		add(filepath.Join(xdg, "git", "config"))
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

// findGitDir finds the .git of the work tree dir is in: a directory, or a
// file that points to one (worktrees, submodules).
func findGitDir(dir string) string {
	for d := dir; ; {
		p := filepath.Join(d, ".git")
		if st, err := os.Stat(p); err == nil {
			if st.IsDir() {
				return p
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return ""
			}
			g, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
			if !ok {
				return ""
			}
			g = strings.TrimSpace(g)
			if !filepath.IsAbs(g) {
				g = filepath.Join(d, g)
			}
			return filepath.Clean(g)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}
