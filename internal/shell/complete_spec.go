package shell

import (
	"io"
	"strings"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/syntax"
)

// compOptKind is the kind of a completion spec.
type compOptKind uint8

const (
	optArgs  compOptKind = iota // no option: the command's plain arguments
	optShort                    // -x
	optOld                      // -name (old style)
	optLong                     // --name
)

// compOpt is one completion spec defined with `complete`: an option of a
// command, or the command's plain arguments, and what may follow it.
type compOpt struct {
	kind       compOptKind
	name       string
	args       string // -a, as given
	desc       string
	conds      []string
	reqParam   bool // -r: the next word is the option's argument
	noFiles    bool // -f
	forceFiles bool // -F
	keepOrder  bool // -k

	argWords  []*syntax.Word   // args, parsed
	condStmts [][]*syntax.Stmt // conds, parsed

	file string // the completion file that defined it, if any
}

// compEntry holds the specs of one command (or command pattern).
type compEntry struct {
	cmd    string
	isPath bool // complete -p: cmd is a path pattern
	opts   []*compOpt
}

// dashes returns the option as typed: -x, -name or --name.
func (o *compOpt) dashes() string {
	switch o.kind {
	case optShort, optOld:
		return "-" + o.name
	case optLong:
		return "--" + o.name
	}
	return ""
}

// parseCompCode parses the code of a -n condition. fish says whether the
// spec comes from fish code; when the code doesn't parse with those
// quoting rules, the other ones are tried (complete is mostly used with
// fish's rules, where \' works inside single quotes).
func parseCompCode(src string, fish bool) (*syntax.File, error) {
	f, err := syntax.ParseOpts(src, syntax.Options{Fish: fish, Lenient: true})
	if err != nil {
		if f2, err2 := syntax.ParseOpts(src, syntax.Options{Fish: !fish, Lenient: true}); err2 == nil {
			return f2, nil
		}
	}
	return f, err
}

// parseCompArgs parses the -a string of a spec into words. Like fish, the
// string is a list of arguments: newlines and semicolons separate them
// like spaces, and keywords are plain words.
func parseCompArgs(src string, fish bool) ([]*syntax.Word, error) {
	words := splitArgList(src)
	if len(words) == 0 {
		return nil, nil
	}
	f, err := parseCompCode("x "+strings.Join(words, " "), fish)
	if err != nil {
		return nil, err
	}
	if len(f.Stmts) != 1 {
		return nil, errorf("unexpected separator")
	}
	call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) < 1 || len(call.Redirs) > 0 {
		return nil, errorf("not a list of arguments")
	}
	return call.Args[1:], nil
}

// splitArgList splits src into the source text of its words, treating
// unquoted newlines and semicolons as spaces. Quotes and parentheses are
// matched loosely: the parser has the last word.
func splitArgList(src string) []string {
	var out []string
	i := 0
	for i < len(src) {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ';' {
			i++
			continue
		}
		start := i
		depth := 0
		for i < len(src) {
			c := src[i]
			if depth == 0 && (c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ';') {
				break
			}
			switch c {
			case '\\':
				i += 2
				continue
			case '\'':
				j := i + 1
				for j < len(src) && src[j] != '\'' {
					if src[j] == '\\' && j+1 < len(src) && (src[j+1] == '\'' || src[j+1] == '\\') {
						j++
					}
					j++
				}
				i = j + 1
				continue
			case '"', '`':
				j := i + 1
				for j < len(src) && src[j] != c {
					if src[j] == '\\' {
						j++
					}
					j++
				}
				i = j + 1
				continue
			case '(':
				depth++
			case ')':
				if depth > 0 {
					depth--
				}
			}
			i++
		}
		if i > len(src) {
			i = len(src)
		}
		out = append(out, src[start:i])
	}
	return out
}

// entry returns the entry for cmd, creating it if asked. Call with c.mu
// held.
func (c *completions) entry(cmd string, isPath, create bool) *compEntry {
	if !isPath && !hasMeta(cmd) {
		e := c.entries[cmd]
		if e == nil && create {
			e = &compEntry{cmd: cmd}
			c.entries[cmd] = e
			c.order = append(c.order, e)
		}
		return e
	}
	for _, e := range c.patterns {
		if e.cmd == cmd && e.isPath == isPath {
			return e
		}
	}
	if !create {
		return nil
	}
	e := &compEntry{cmd: cmd, isPath: isPath}
	c.patterns = append(c.patterns, e)
	c.order = append(c.order, e)
	return e
}

// addOpt adds a spec for cmd.
func (c *completions) addOpt(cmd string, isPath bool, o *compOpt) {
	c.mu.Lock()
	if n := len(c.loading); n > 0 {
		o.file = c.loading[n-1]
	}
	e := c.entry(cmd, isPath, true)
	// Readers keep slices of e.opts; appending never changes what they see.
	e.opts = append(e.opts, o)
	c.mu.Unlock()
}

// dropSpecs erases the specs that a completion file defined.
func (c *completions) dropSpecs(file string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.order {
		var keep []*compOpt
		for i, o := range e.opts {
			if o.file == file {
				if keep == nil {
					keep = append(make([]*compOpt, 0, len(e.opts)), e.opts[:i]...)
				}
				continue
			}
			if keep != nil {
				keep = append(keep, o)
			}
		}
		if keep != nil {
			e.opts = keep
		}
	}
}

// removeOpts erases specs of cmd: all of them, or the options named.
func (c *completions) removeOpts(cmd string, isPath bool, match func(*compOpt) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entry(cmd, isPath, false)
	if e == nil {
		return
	}
	var keep []*compOpt
	if match != nil {
		for _, o := range e.opts {
			if !match(o) {
				keep = append(keep, o)
			}
		}
	}
	e.opts = keep
}

func (c *completions) addWrap(cmd, target string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.wraps[cmd]
	if !ok {
		c.wrapOrder = append(c.wrapOrder, cmd)
	}
	for _, t := range l {
		if t == target {
			return
		}
	}
	c.wraps[cmd] = append(l, target)
}

func (c *completions) removeWrap(cmd, target string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var keep []string
	for _, t := range c.wraps[cmd] {
		if t != target {
			keep = append(keep, t)
		}
	}
	c.wraps[cmd] = keep
}

// optsFor returns the specs that apply to cmd.
func (c *completions) optsFor(r *compRun, cmd string) []*compOpt {
	c.mu.RLock()
	var opts []*compOpt
	if e := c.entries[cmd]; e != nil {
		opts = e.opts
	}
	pats := c.patterns
	c.mu.RUnlock()
	if len(pats) == 0 {
		return opts
	}
	opts = append([]*compOpt(nil), opts...)
	path, havePath := "", false
	for _, e := range pats {
		target := cmd
		if e.isPath {
			if !havePath {
				path, havePath = r.commandPath(cmd), true
			}
			target = path
		}
		if target != "" && matchPattern(e.cmd, target) {
			c.mu.RLock()
			opts = append(opts, e.opts...)
			c.mu.RUnlock()
		}
	}
	return opts
}

// commandPath resolves a command name to its path, for complete -p.
func (r *compRun) commandPath(cmd string) string {
	if strings.Contains(cmd, "/") {
		return r.sh.absPath(cmd)
	}
	p, _ := r.sh.LookPath(cmd)
	return p
}

// print writes specs in a form complete can read back. cmds restricts
// the output to some commands.
func (c *completions) print(w io.Writer, cmds []string, paths []string) {
	want := func(e *compEntry) bool {
		if len(cmds) == 0 && len(paths) == 0 {
			return true
		}
		l := cmds
		if e.isPath {
			l = paths
		}
		for _, x := range l {
			if x == e.cmd {
				return true
			}
		}
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	var b strings.Builder
	for _, e := range c.order {
		if !want(e) {
			continue
		}
		for i := len(e.opts) - 1; i >= 0; i-- {
			b.WriteString(specString(e, e.opts[i]))
		}
	}
	for _, cmd := range c.wrapOrder {
		if len(cmds) > 0 || len(paths) > 0 {
			found := false
			for _, x := range cmds {
				found = found || x == cmd
			}
			if !found {
				continue
			}
		}
		for _, t := range c.wraps[cmd] {
			b.WriteString("complete " + fishEscape(cmd) + " -w " + fishEscape(t) + "\n")
		}
	}
	io.WriteString(w, b.String())
}

// specString renders one spec as a complete command, the way fish does.
func specString(e *compEntry, o *compOpt) string {
	var b strings.Builder
	b.WriteString("complete")
	if o.keepOrder {
		b.WriteString(" -k")
	}
	switch {
	case o.noFiles && o.reqParam:
		b.WriteString(" --exclusive")
	case o.noFiles:
		b.WriteString(" --no-files")
	case o.forceFiles:
		b.WriteString(" --force-files")
	case o.reqParam:
		b.WriteString(" --require-parameter")
	}
	if e.isPath {
		b.WriteString(" -p " + fishEscape(e.cmd))
	} else {
		b.WriteString(" " + fishEscape(e.cmd))
	}
	switch o.kind {
	case optShort:
		b.WriteString(" -s " + fishEscape(o.name))
	case optOld:
		b.WriteString(" -o " + fishEscape(o.name))
	case optLong:
		b.WriteString(" -l " + fishEscape(o.name))
	}
	if o.desc != "" {
		b.WriteString(" -d " + fishEscape(o.desc))
	}
	if o.args != "" {
		b.WriteString(" -a " + fishEscape(o.args))
	}
	for _, cond := range o.conds {
		b.WriteString(" -n " + fishEscape(cond))
	}
	b.WriteByte('\n')
	return b.String()
}

// specComplete adds the completions that specs give for an argument of
// cmd, whose words so far are argv (argv[0] is the command). It returns
// whether files may be completed too and whether a spec forces them.
func (r *compRun) specComplete(col *collector, cmd string, argv []string) (useFiles, force bool) {
	r.load(cmd)
	opts := r.c.optsFor(r, cmd)
	if len(opts) == 0 {
		return true, false
	}
	cur := r.line.tok.value
	in := r.tokenInsertion()
	useFiles = true
	useSwitches := true
	for _, a := range argv[1:] {
		if a == "--" {
			useSwitches = false
			break
		}
	}
	prev := ""
	if len(argv) > 1 {
		prev = argv[len(argv)-1]
	}
	useCommon := true
	apply := func(o *compOpt) {
		if o.reqParam {
			useCommon = false
		}
		if o.noFiles {
			useFiles = false
		}
		if o.forceFiles {
			force = true
		}
	}
	if useSwitches {
		if strings.HasPrefix(cur, "-") {
			// an option and its argument in one word: --color=auto,
			// -name=value, -wVALUE
			shortPos := shortOptionPos(cur, opts)
			for i := len(opts) - 1; i >= 0; i-- {
				o := opts[i]
				off := 0
				switch o.kind {
				case optShort:
					if shortPos < 0 || cur[shortPos] != o.name[0] {
						continue
					}
					off = shortPos + 1
				case optLong, optOld:
					p := o.dashes() + "="
					if !strings.HasPrefix(cur, p) {
						continue
					}
					off = len(p)
				default:
					continue
				}
				if !r.condOK(o) {
					continue
				}
				apply(o)
				if sub, ok := in.split(off); ok {
					r.addArgs(col, o, sub)
				}
			}
		} else if len(prev) > 1 && prev[0] == '-' {
			// the previous word is an option that takes this one
			oldMatch := false
			for i := len(opts) - 1; i >= 0; i-- {
				o := opts[i]
				if o.kind == optOld && prev == "-"+o.name && r.condOK(o) {
					oldMatch = true
					apply(o)
					r.addArgs(col, o, in)
				}
			}
			if !oldMatch {
				prevShort := shortOptionPos(prev, opts)
				for i := len(opts) - 1; i >= 0; i-- {
					o := opts[i]
					if !o.reqParam {
						continue
					}
					match := false
					switch o.kind {
					case optShort:
						match = prevShort == len(prev)-1 && prev[prevShort] == o.name[0]
					case optLong:
						match = prev == "--"+o.name
					}
					if match && r.condOK(o) {
						apply(o)
						r.addArgs(col, o, in)
					}
				}
			}
		}
	}
	if !useCommon {
		return useFiles, force
	}
	isOpt := useSwitches && strings.HasPrefix(cur, "-")
	m := newMatcher(cur, true)
	for i := len(opts) - 1; i >= 0; i-- {
		o := opts[i]
		if o.kind != optArgs && !isOpt {
			continue
		}
		if !r.condOK(o) {
			continue
		}
		switch o.kind {
		case optArgs:
			if o.noFiles {
				useFiles = false
			}
			if o.forceFiles {
				force = true
			}
			r.addArgs(col, o, in)
		case optShort:
			if shortOK(cur, o, opts) {
				val := cur + o.name
				col.add(tierPrefix, val, editor.Candidate{Text: in.text(val, tierPrefix, !o.reqParam), Display: val,
					Description: o.desc, NoSpace: o.reqParam, Kind: "option"}, false)
			}
		case optLong, optOld:
			whole := o.dashes()
			tier := m.tier(whole, col.max())
			if tier == tierNone {
				continue
			}
			if o.kind == optLong && o.args != "" && !o.reqParam {
				// an optional argument can only be given after '='
				val := whole + "="
				col.add(tier, val, editor.Candidate{Text: in.text(val, tier, false), Display: val,
					Description: o.desc, NoSpace: true, Kind: "option"}, false)
			}
			col.add(tier, whole, editor.Candidate{Text: in.text(whole, tier, true), Display: whole,
				Description: o.desc, Kind: "option"}, false)
		}
	}
	return useFiles, force
}

// split returns the insertion for the part of the token after its first
// off bytes, which must have been typed without quotes or escapes.
func (in *insertion) split(off int) (*insertion, bool) {
	if off > len(in.raw) || off > len(in.needle) || in.raw[:off] != in.needle[:off] {
		return nil, false
	}
	return &insertion{
		prefix:     in.prefix + in.raw[:off],
		raw:        in.raw[off:],
		needle:     in.needle[off:],
		quote:      in.quote,
		escape:     in.escape,
		closeQuote: in.closeQuote,
	}, true
}

// shortOptionPos returns the index in arg of the first character of a
// group of short options (-abc) that isn't a known short option or that
// takes an argument, or -1.
func shortOptionPos(arg string, opts []*compOpt) int {
	if len(arg) <= 1 || arg[0] != '-' || arg[1] == '-' {
		return -1
	}
	for i := 1; i < len(arg); i++ {
		o := findShort(opts, arg[i])
		if o == nil || o.reqParam {
			return i
		}
	}
	return -1
}

func findShort(opts []*compOpt, c byte) *compOpt {
	for _, o := range opts {
		if o.kind == optShort && o.name != "" && o.name[0] == c {
			return o
		}
	}
	return nil
}

// shortOK reports whether the short option o can be added to arg, which
// is "-" or a group of short options that take no argument.
func shortOK(arg string, o *compOpt, opts []*compOpt) bool {
	if o.name == "" || len(arg) < 1 || arg[0] != '-' || len(arg) > 1 && arg[1] == '-' {
		return false
	}
	if strings.Contains(arg[1:], o.name) {
		return false
	}
	for i := 1; i < len(arg); i++ {
		x := findShort(opts, arg[i])
		if x == nil || x.reqParam {
			return false
		}
	}
	return true
}

// condOK runs o's conditions (-n); each result is remembered for the
// rest of the completion of this command line.
func (r *compRun) condOK(o *compOpt) bool {
	for i, src := range o.conds {
		ok, cached := r.conds[src]
		if !cached {
			ok = r.runCode(o.condStmts[i]) == 0
			r.conds[src] = ok
		}
		if !ok {
			return false
		}
	}
	return true
}

// runCode runs completion code in the completion shell, quietly, and
// returns its status.
func (r *compRun) runCode(stmts []*syntax.Stmt) int {
	sh := r.sh
	if sh.interrupted() {
		return 1
	}
	r.preload(stmts)
	ctrl, exit := sh.ctrl, sh.exitStatus
	sh.noErrexit++
	st := sh.inBlockScope(func() int { return sh.runStmts(stmts, r.io) })
	sh.noErrexit--
	switch sh.ctrl {
	case ctrlExit:
		st = sh.exitStatus
	case ctrlReturn:
		st = sh.status
	}
	sh.ctrl, sh.exitStatus = ctrl, exit
	return st
}

// argItem is one candidate from a -a list.
type argItem struct{ value, desc string }

// evalArgs expands the -a list of o: words, variables and command
// substitutions (one candidate per line, "value<TAB>description").
func (r *compRun) evalArgs(o *compOpt) []argItem {
	if len(o.argWords) == 0 || r.sh.interrupted() {
		return nil
	}
	for _, w := range o.argWords {
		walkWord(w, r.preloadFunc)
	}
	var items []argItem
	for _, w := range o.argWords {
		vals, err := r.sh.expandWord(w, r.io, expNoGlob)
		if err != nil {
			if err == errInterrupted {
				break
			}
			continue
		}
		for _, v := range vals {
			val, desc, _ := strings.Cut(v, "\t")
			if val == "" {
				continue
			}
			items = append(items, argItem{val, desc})
		}
	}
	return items
}

// addArgs adds the -a candidates of o that match the insertion's needle.
func (r *compRun) addArgs(col *collector, o *compOpt, in *insertion) {
	if len(o.argWords) == 0 {
		return
	}
	m := newMatcher(in.needle, true)
	for _, it := range r.evalArgs(o) {
		tier := m.tier(it.value, col.max())
		if tier == tierNone {
			continue
		}
		desc := it.desc
		if desc == "" {
			desc = o.desc
		}
		// like fish, no space after a value that is likely continued
		noSpace := strings.IndexByte("/=@:.,-", it.value[len(it.value)-1]) >= 0
		col.add(tier, it.value, editor.Candidate{Text: in.text(it.value, tier, !noSpace), Display: it.value,
			Description: desc, NoSpace: noSpace, Kind: "arg"}, o.keepOrder)
	}
}
