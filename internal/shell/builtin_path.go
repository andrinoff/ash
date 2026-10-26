package shell

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"golang.org/x/sys/unix"
)

// path follows fish 4.8, including its exit statuses: the string
// subcommands (basename, dirname, normalize) succeed only when some path
// changed, filter/is/mtime/resolve when something passed, change-extension
// when it got any path, and sort always.

func init() {
	builtins["path"] = bPath
	builtinHelp["path"] = "manipulate and check paths"
}

const pathUsage = `path - manipulate and check paths

usage: path SUBCOMMAND [OPTIONS] [PATH...]

  basename [-E]          the last component (-E, --no-extension: without extension)
  dirname                everything before the last component
  extension              the extension, with its dot (an empty line if none)
  change-extension EXT   replace the extension ('' removes it)
  filter                 keep paths that exist and pass the checks:
      -t, --type TYPE      file, dir, link, block, char, fifo, socket (any of)
      -p, --perm PERM      read, write, exec, suid, sgid, user, group (all of)
      -f -d -l             --type file, dir, link
      -r -w -x             --perm read, write, exec
      -v, --invert         keep the paths that fail instead
      --all                print nothing; succeed if every path passes
  is                     filter -q
  mtime [-R]             modification time in epoch seconds (-R: seconds ago)
  normalize              squash //, . and .. without looking at the disk
  resolve                absolute physical path, resolving symlinks
  sort [-r] [-u] [--key=basename|dirname|path]

Every subcommand takes:
  -z, --null-in    read NUL-separated paths from stdin
  -Z, --null-out   separate output with NUL instead of newline
  -q, --quiet      print nothing, only set the exit status

Paths come from the arguments, or from stdin (one per line) when none are
given and stdin is not a terminal.
`

// Option indexes shared by the subcommand option tables.
const (
	pathOptNullIn = iota
	pathOptNullOut
	pathOptQuiet
	pathOptHelp
	pathOptNoExt
	pathOptInvert
	pathOptType
	pathOptPerm
	pathOptDir
	pathOptFile
	pathOptLink
	pathOptRead
	pathOptWrite
	pathOptExec
	pathOptAll
	pathOptRelative
	pathOptReverse
	pathOptUnique
	pathOptKey
)

var pathOptDefs = map[int]foptDef{
	pathOptNullIn:   {'z', "null-in", foptNone},
	pathOptNullOut:  {'Z', "null-out", foptNone},
	pathOptQuiet:    {'q', "quiet", foptNone},
	pathOptHelp:     {'h', "help", foptNone},
	pathOptNoExt:    {'E', "no-extension", foptNone},
	pathOptInvert:   {'v', "invert", foptNone},
	pathOptType:     {'t', "type", foptRequired},
	pathOptPerm:     {'p', "perm", foptRequired},
	pathOptDir:      {'d', "", foptNone},
	pathOptFile:     {'f', "", foptNone},
	pathOptLink:     {'l', "", foptNone},
	pathOptRead:     {'r', "", foptNone},
	pathOptWrite:    {'w', "", foptNone},
	pathOptExec:     {'x', "", foptNone},
	pathOptAll:      {0, "all", foptNone},
	pathOptRelative: {'R', "relative", foptNone},
	pathOptReverse:  {'r', "reverse", foptNone},
	pathOptUnique:   {'u', "unique", foptNone},
	pathOptKey:      {0, "key", foptRequired},
}

var pathSubOpts = map[string][]int{
	"basename":         {pathOptNoExt},
	"dirname":          nil,
	"extension":        nil,
	"change-extension": nil,
	"filter":           {pathOptInvert, pathOptType, pathOptPerm, pathOptDir, pathOptFile, pathOptLink, pathOptRead, pathOptWrite, pathOptExec, pathOptAll},
	"is":               {pathOptInvert, pathOptType, pathOptPerm, pathOptDir, pathOptFile, pathOptLink, pathOptRead, pathOptWrite, pathOptExec, pathOptAll},
	"mtime":            {pathOptRelative},
	"normalize":        nil,
	"resolve":          nil,
	"sort":             {pathOptReverse, pathOptUnique, pathOptKey},
}

// Type and permission bits for filter.
const (
	pathTypeFile = 1 << iota
	pathTypeDir
	pathTypeLink
	pathTypeBlock
	pathTypeChar
	pathTypeFifo
	pathTypeSocket
)

const (
	pathPermRead = 1 << iota
	pathPermWrite
	pathPermExec
	pathPermSuid
	pathPermSgid
	pathPermUser
	pathPermGroup
)

var pathTypeNames = map[string]int{"file": pathTypeFile, "dir": pathTypeDir, "link": pathTypeLink, "block": pathTypeBlock, "char": pathTypeChar, "fifo": pathTypeFifo, "socket": pathTypeSocket}
var pathPermNames = map[string]int{"read": pathPermRead, "write": pathPermWrite, "exec": pathPermExec, "suid": pathPermSuid, "sgid": pathPermSgid, "user": pathPermUser, "group": pathPermGroup}

type pathCmd struct {
	sh  *Shell
	io  *IO
	sub string
	out *bufio.Writer

	nullIn, nullOut, quiet bool
	noExt                  bool
	invert, all            bool
	types, perms           int
	typesSet, permsSet     bool
	relative               bool
	reverse, unique        bool
	key                    string
}

func (c *pathCmd) fail(format string, args ...any) int {
	fmt.Fprintf(c.io.Err, "path %s: %s\n", c.sub, fmt.Sprintf(format, args...))
	return 2
}

func bPath(sh *Shell, io *IO, args []string) int {
	if len(args) < 2 {
		io.Err.WriteString("path: missing subcommand\n")
		return 2
	}
	c := &pathCmd{sh: sh, io: io, sub: args[1]}
	if c.sub == "-h" || c.sub == "--help" {
		io.Out.WriteString(pathUsage)
		return 0
	}
	extra, ok := pathSubOpts[c.sub]
	if !ok {
		fmt.Fprintf(io.Err, "path %s: invalid subcommand\n", c.sub)
		return 2
	}
	ids := append([]int{pathOptNullIn, pathOptNullOut, pathOptQuiet, pathOptHelp}, extra...)
	op := &foptParser{permute: true}
	for _, id := range ids {
		op.defs = append(op.defs, pathOptDefs[id])
	}
	help, status := false, 0
	rest, _, msg, ok := op.parse(args[2:], func(h foptHit) bool {
		switch ids[h.def] {
		case pathOptNullIn:
			c.nullIn = true
		case pathOptNullOut:
			c.nullOut = true
		case pathOptQuiet:
			c.quiet = true
		case pathOptHelp:
			help = true
		case pathOptNoExt:
			c.noExt = true
		case pathOptInvert:
			c.invert = true
		case pathOptAll:
			c.all = true
		case pathOptRelative:
			c.relative = true
		case pathOptReverse:
			c.reverse = true
		case pathOptUnique:
			c.unique = true
		case pathOptDir:
			c.types, c.typesSet = c.types|pathTypeDir, true
		case pathOptFile:
			c.types, c.typesSet = c.types|pathTypeFile, true
		case pathOptLink:
			c.types, c.typesSet = c.types|pathTypeLink, true
		case pathOptRead:
			c.perms, c.permsSet = c.perms|pathPermRead, true
		case pathOptWrite:
			c.perms, c.permsSet = c.perms|pathPermWrite, true
		case pathOptExec:
			c.perms, c.permsSet = c.perms|pathPermExec, true
		case pathOptType, pathOptPerm:
			names, what, bits, set := pathTypeNames, "type", &c.types, &c.typesSet
			if ids[h.def] == pathOptPerm {
				names, what, bits, set = pathPermNames, "permission", &c.perms, &c.permsSet
			}
			*set = true
			for _, n := range strings.Split(h.value, ",") {
				if n == "" {
					continue
				}
				b, ok := names[n]
				if !ok {
					status = c.fail("Invalid %s '%s'", what, n)
					return false
				}
				*bits |= b
			}
		case pathOptKey:
			if h.value != "basename" && h.value != "dirname" && h.value != "path" {
				status = c.fail("Invalid sort key '%s'", h.value)
				return false
			}
			c.key = h.value
		}
		return true
	})
	if !ok {
		if msg != "" {
			return c.fail("%s", msg)
		}
		return status
	}
	if help {
		io.Out.WriteString(pathUsage)
		return 0
	}
	if c.sub == "is" {
		c.quiet = true
	}
	var ext string
	if c.sub == "change-extension" {
		if len(rest) == 0 {
			return c.fail("missing argument")
		}
		ext, rest = rest[0], rest[1:]
	}
	next := c.inputs(rest)
	c.out = bufio.NewWriter(io.Out)
	defer c.out.Flush()
	switch c.sub {
	case "basename":
		return c.transform(next, func(p string) string {
			b := pathBasename(p)
			if c.noExt {
				if i := pathExtIndex(b); i >= 0 {
					b = b[:i]
				}
			}
			return b
		})
	case "dirname":
		return c.transform(next, pathDirname)
	case "normalize":
		return c.transform(next, func(p string) string {
			n := pathNormalize(p)
			if strings.HasPrefix(n, "-") {
				n = "./" + n
			}
			return n
		})
	case "extension":
		return c.extension(next)
	case "change-extension":
		return c.changeExtension(next, ext)
	case "filter", "is":
		return c.filter(next)
	case "mtime":
		return c.mtime(next)
	case "resolve":
		return c.resolve(next)
	case "sort":
		return c.sort(next)
	}
	return 2
}

// inputs returns an iterator over the paths: the arguments if there are
// any, else stdin when it was piped or redirected (like fish).
func (c *pathCmd) inputs(args []string) func() (string, bool) {
	if len(args) > 0 || !c.io.In.ReadsInput() {
		i := 0
		return func() (string, bool) {
			if i >= len(args) {
				return "", false
			}
			i++
			return args[i-1], true
		}
	}
	r := bufio.NewReaderSize(c.io.In, 4096)
	sep, decided := byte('\n'), false
	if c.nullIn {
		sep, decided = 0, true
	}
	return func() (string, bool) {
		if !decided {
			// Like fish: split on NUL if one shows up in the first read.
			decided = true
			if _, err := r.Peek(1); err == nil {
				if b, _ := r.Peek(r.Buffered()); bytes.IndexByte(b, 0) >= 0 {
					sep = 0
				}
			}
		}
		s, err := r.ReadString(sep)
		if s == "" && err != nil {
			return "", false
		}
		return strings.TrimSuffix(s, string(sep)), true
	}
}

func (c *pathCmd) emit(s string) {
	if c.quiet {
		return
	}
	c.out.WriteString(s)
	if c.nullOut {
		c.out.WriteByte(0)
	} else {
		c.out.WriteByte('\n')
	}
}

// transform prints f of each non-empty path and succeeds if any changed.
func (c *pathCmd) transform(next func() (string, bool), f func(string) string) int {
	changed := false
	for p, ok := next(); ok; p, ok = next() {
		if p == "" {
			continue
		}
		t := f(p)
		if t != p {
			changed = true
			if c.quiet {
				return 0
			}
		}
		c.emit(t)
	}
	if changed {
		return 0
	}
	return 1
}

func (c *pathCmd) extension(next func() (string, bool)) int {
	found := false
	for p, ok := next(); ok; p, ok = next() {
		i := pathExtIndex(p)
		if i < 0 {
			c.emit("")
			continue
		}
		if c.quiet {
			return 0
		}
		c.emit(p[i:])
		found = true
	}
	if found {
		return 0
	}
	return 1
}

func (c *pathCmd) changeExtension(next func() (string, bool), ext string) int {
	n := 0
	for p, ok := next(); ok; p, ok = next() {
		if i := pathExtIndex(p); i >= 0 {
			p = p[:i]
		}
		if ext != "" {
			if ext[0] != '.' {
				p += "."
			}
			p += ext
		}
		if c.quiet {
			return 0
		}
		c.emit(p)
		n++
	}
	if n > 0 {
		return 0
	}
	return 1
}

// onDisk turns a path into one to hand to the system: relative paths are
// taken from the shell's directory. It does not clean the result, so
// "link/.." still means the parent of the link's target.
func (c *pathCmd) onDisk(p string) string {
	if p == "" || p[0] == '/' {
		return p
	}
	d := c.sh.dir
	if !strings.HasSuffix(d, "/") {
		d += "/"
	}
	return d + p
}

// passes applies filter's checks to p (before --invert).
func (c *pathCmd) passes(p string) bool {
	if p == "" {
		return false
	}
	f := c.onDisk(p)
	if !c.typesSet && !c.permsSet {
		_, err := os.Stat(f)
		return err == nil
	}
	if c.typesSet {
		ok := false
		if c.types&pathTypeLink != 0 {
			fi, err := os.Lstat(f)
			ok = err == nil && fi.Mode()&os.ModeSymlink != 0
		}
		fi, err := os.Stat(f)
		if err != nil {
			return false
		}
		m := fi.Mode()
		ok = ok ||
			c.types&pathTypeFile != 0 && m.IsRegular() ||
			c.types&pathTypeDir != 0 && m.IsDir() ||
			c.types&pathTypeBlock != 0 && m&os.ModeDevice != 0 && m&os.ModeCharDevice == 0 ||
			c.types&pathTypeChar != 0 && m&os.ModeCharDevice != 0 ||
			c.types&pathTypeFifo != 0 && m&os.ModeNamedPipe != 0 ||
			c.types&pathTypeSocket != 0 && m&os.ModeSocket != 0
		if !ok {
			return false
		}
	}
	if c.permsSet {
		var mode uint32
		if c.perms&pathPermRead != 0 {
			mode |= unix.R_OK
		}
		if c.perms&pathPermWrite != 0 {
			mode |= unix.W_OK
		}
		if c.perms&pathPermExec != 0 {
			mode |= unix.X_OK
		}
		if mode != 0 && unix.Access(f, mode) != nil {
			return false
		}
		if c.perms&(pathPermSuid|pathPermSgid|pathPermUser|pathPermGroup) != 0 {
			fi, err := os.Stat(f)
			if err != nil {
				return false
			}
			if c.perms&pathPermSuid != 0 && fi.Mode()&os.ModeSetuid == 0 ||
				c.perms&pathPermSgid != 0 && fi.Mode()&os.ModeSetgid == 0 {
				return false
			}
			st, ok := fi.Sys().(*syscall.Stat_t)
			if c.perms&pathPermUser != 0 && (!ok || int(st.Uid) != os.Geteuid()) ||
				c.perms&pathPermGroup != 0 && (!ok || int(st.Gid) != os.Getegid()) {
				return false
			}
		}
	}
	return true
}

func (c *pathCmd) filter(next func() (string, bool)) int {
	n := 0
	for p, ok := next(); ok; p, ok = next() {
		if c.passes(p) == c.invert {
			if c.all {
				return 1
			}
			continue
		}
		n++
		if c.all {
			continue
		}
		if c.quiet {
			return 0
		}
		if strings.HasPrefix(p, "-") {
			p = "./" + p
		}
		c.emit(p)
	}
	if n > 0 {
		return 0
	}
	return 1
}

func (c *pathCmd) mtime(next func() (string, bool)) int {
	now := time.Now().Unix()
	n := 0
	for p, ok := next(); ok; p, ok = next() {
		if p == "" {
			continue
		}
		fi, err := os.Stat(c.onDisk(p))
		if err != nil {
			continue
		}
		if c.quiet {
			return 0
		}
		t := fi.ModTime().Unix()
		if c.relative {
			t = now - t
		}
		c.emit(strconv.FormatInt(t, 10))
		n++
	}
	if n > 0 {
		return 0
	}
	return 1
}

// pathRealpath is fish's wrealpath: realpath, except that the last
// component need not exist. p must be absolute.
func pathRealpath(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r, true
	}
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return p, true
	}
	r, err := filepath.EvalSymlinks(p[:i])
	if err != nil {
		return "", false
	}
	if len(r) > 1 {
		r += "/"
	}
	return r + p[i+1:], true
}

func (c *pathCmd) resolve(next func() (string, bool)) int {
	n := 0
	for p, ok := next(); ok; p, ok = next() {
		if p == "" {
			continue
		}
		abs := c.onDisk(p)
		real, ok := pathRealpath(abs)
		if !ok {
			// Resolve as much of the front as exists and append the rest.
			dir, rest := abs, pathBasename(abs)
			for dir != "" && dir != "/" {
				dir = pathDirname(dir)
				if r, ok2 := pathRealpath(dir); ok2 {
					real, ok = r+"/"+rest, true
					break
				}
				rest = pathBasename(dir) + "/" + rest
			}
			if !ok {
				continue
			}
		}
		if c.quiet {
			return 0
		}
		c.emit(pathNormalize(real))
		n++
	}
	if n > 0 {
		return 0
	}
	return 1
}

func (c *pathCmd) sort(next func() (string, bool)) int {
	var list []string
	for p, ok := next(); ok; p, ok = next() {
		list = append(list, p)
	}
	key := func(s string) string { return s }
	switch c.key {
	case "basename":
		key = pathBasename
	case "dirname":
		key = pathDirname
	}
	sort.SliceStable(list, func(i, j int) bool {
		if c.reverse {
			return pathFileCmp(key(list[j]), key(list[i])) < 0
		}
		return pathFileCmp(key(list[i]), key(list[j])) < 0
	})
	if c.unique {
		var keep []string
		for i, p := range list {
			if i == 0 || key(p) != key(keep[len(keep)-1]) {
				keep = append(keep, p)
			}
		}
		list = keep
	}
	for _, p := range list {
		c.emit(p)
	}
	return 0
}

// pathBasename is fish's wbasename (POSIX basename).
func pathBasename(p string) string {
	if p == "" {
		return "."
	}
	if strings.Trim(p, "/") == "" {
		return "/"
	}
	p = strings.TrimRight(p, "/")
	return p[strings.LastIndexByte(p, '/')+1:]
}

// pathDirname is fish's wdirname: POSIX dirname, keeping a lone "//".
func pathDirname(p string) string {
	if p == "//" {
		return p
	}
	if p != "" && strings.Trim(p, "/") == "" {
		return "/"
	}
	p = strings.TrimRight(p, "/")
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return "."
	}
	if p = strings.TrimRight(p[:i], "/"); p == "" {
		return "/"
	}
	return p
}

// pathExtIndex returns where p's extension starts, or -1. Like fish it
// looks for the dot in the basename but measures from the end of p, so
// trailing slashes shift the result ("a/b.c/" gives "c/").
func pathExtIndex(p string) int {
	base := pathBasename(p)
	if base == "." || base == ".." {
		return -1
	}
	i := strings.LastIndexByte(base, '.')
	if i <= 0 {
		return -1
	}
	return i + len(p) - len(base)
}

// pathNormalize is fish's normalize_path: squash slashes, drop "." and
// fold ".." into earlier components, without touching the disk.
func pathNormalize(p string) string {
	abs := strings.HasPrefix(p, "/")
	var comps []string
	for _, c := range strings.Split(p, "/") {
		switch {
		case c == "" || c == ".":
		case c != "..":
			comps = append(comps, c)
		case len(comps) > 0 && comps[len(comps)-1] != "..":
			comps = comps[:len(comps)-1]
		case !abs:
			comps = append(comps, "..")
		}
	}
	res := strings.Join(comps, "/")
	if abs {
		res = "/" + res
	}
	if res == "" {
		res = "."
	}
	return res
}

// pathFileCmp orders strings like fish's glob sort (wcsfilecmp_glob):
// case-insensitive, runs of digits compared as numbers, ties broken by
// plain comparison.
func pathFileCmp(a, b string) int {
	ar, br := []rune(a), []rune(b)
	ai, bi, ret := 0, 0, 0
	for ai < len(ar) && bi < len(br) {
		ac, bc := ar[ai], br[bi]
		if pathIsDigit(ac) && pathIsDigit(bc) {
			var ad, bd int
			ret, ad, bd = pathDigitsCmp(ar[ai:], br[bi:])
			ai, bi = ai+ad, bi+bd
			if ret != 0 || ai == len(ar) || bi == len(br) {
				break
			}
			continue
		}
		if ac != bc {
			al, bl := unicode.ToLower(ac), unicode.ToLower(bc)
			if al != bl {
				if al < bl {
					return -1
				}
				return 1
			}
		}
		ai, bi = ai+1, bi+1
	}
	switch {
	case ret != 0:
		return ret
	case ai == len(ar) && bi == len(br):
		return strings.Compare(a, b)
	case ai == len(ar):
		return -1
	}
	return 1
}

func pathIsDigit(r rune) bool { return r >= '0' && r <= '9' }

// pathDigitsCmp compares the numbers a and b start with, returning the
// result and how much of each it used (plus trailing whitespace, as fish
// skips it).
func pathDigitsCmp(a, b []rune) (int, int, int) {
	at := func(s []rune, i int) rune {
		if i < len(s) {
			return s[i]
		}
		return 0
	}
	ai, bi := 0, 0
	for ai < len(a) && a[ai] == '0' {
		ai++
	}
	for bi < len(b) && b[bi] == '0' {
		bi++
	}
	ret := 0
	for {
		ac, bc := at(a, ai), at(b, bi)
		if !pathIsDigit(ac) || !pathIsDigit(bc) {
			if pathIsDigit(ac) {
				ret = 1
			}
			if pathIsDigit(bc) {
				ret = -1
			}
			break
		}
		if ret == 0 && ac != bc {
			ret = 1
			if ac < bc {
				ret = -1
			}
		}
		ai, bi = ai+1, bi+1
	}
	for ai < len(a) && unicode.IsSpace(a[ai]) {
		ai++
	}
	for bi < len(b) && unicode.IsSpace(b[bi]) {
		bi++
	}
	return ret, ai, bi
}
