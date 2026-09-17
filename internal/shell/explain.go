package shell

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/andrinoff/ash/internal/syntax"
)

// explain says what each word of a command line does, from what the
// shell already knows: the descriptions of builtins and functions, the
// one-line summary in a program's man page, and the descriptions in
// completion definitions (fish's completion files, cobra programs, your
// own). Nothing in the line is run.

// ExplainRow is one word of an explained command line.
type ExplainRow struct {
	Word   string
	Desc   string
	Kind   string // command, option, value, arg, assign, missing, redirect, operator
	Indent int    // 0 for a command, 1 for its arguments
}

func init() {
	builtins["explain"] = bExplain
	builtinHelp["explain"] = "say what each part of a command line does"
}

const explainUsage = `usage: explain [COMMAND LINE]

Say what the command and each of its options and arguments does, from
man pages and completion definitions. Nothing is run. Without arguments,
in a key binding, explain the command line being edited; otherwise read
the command line from standard input.
`

func bExplain(sh *Shell, io *IO, args []string) int {
	var line string
	switch {
	case len(args) > 1 && (args[1] == "-h" || args[1] == "--help"):
		fmt.Fprint(io.Out, explainUsage)
		return 0
	case len(args) > 1:
		a := args[1:]
		if a[0] == "--" {
			a = a[1:]
		}
		line = strings.Join(a, " ")
	case sh.Editor != nil && sh.Editor.Reading():
		line, _ = sh.Editor.Buffer()
	case !io.In.TTY:
		b, _ := readAllLimited(io.In, 1<<16)
		line = string(b)
	}
	rows := sh.Explain(line)
	if len(rows) == 0 {
		return 1
	}
	fmt.Fprint(io.Out, sh.FormatExplain(rows, io.Out.TTY))
	return 0
}

func readAllLimited(r io.Reader, n int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, n))
}

// Explain describes each word of line.
func (sh *Shell) Explain(line string) []ExplainRow {
	f, err := syntax.ParseOpts(line, syntax.Options{Lenient: true})
	if f == nil {
		if err != nil {
			// an unfinished line ("git commit -m 'wip"): explain what's there
			f, _ = syntax.ParseOpts(line+"'", syntax.Options{Lenient: true})
		}
		if f == nil {
			return nil
		}
	}
	ex := &explainer{sh: sh, src: line}
	ex.stmts(f.Stmts)
	return ex.rows
}

type explainer struct {
	sh   *Shell
	src  string
	rows []ExplainRow
}

// exWord is a word of a simple command: its source text and, if it has no
// expansions, its value.
type exWord struct {
	raw, value string
	literal    bool
}

func (ex *explainer) add(word, desc, kind string, indent int) {
	ex.rows = append(ex.rows, ExplainRow{Word: word, Desc: desc, Kind: kind, Indent: indent})
}

func (ex *explainer) stmts(stmts []*syntax.Stmt) {
	for _, st := range stmts {
		if st != nil {
			ex.command(st.Cmd)
		}
	}
}

func (ex *explainer) command(c syntax.Command) {
	switch c := c.(type) {
	case *syntax.CallExpr:
		ex.call(c.Assigns, c.Args)
		ex.redirects(c.Redirs)
	case *syntax.EnvCmd:
		if call, ok := c.Cmd.(*syntax.CallExpr); ok {
			ex.call(append(append([]*syntax.Assign(nil), c.Assigns...), call.Assigns...), call.Args)
			ex.redirects(call.Redirs)
		} else {
			ex.command(c.Cmd)
		}
	case *syntax.BinaryCmd:
		ex.command(c.X)
		if c.Op == syntax.AndOp {
			ex.add("&&", "and if that worked:", "operator", 0)
		} else {
			ex.add("||", "or if that failed:", "operator", 0)
		}
		ex.command(c.Y)
	case *syntax.Pipeline:
		for i, x := range c.Cmds {
			if i > 0 {
				what := "its output goes to"
				if i-1 < len(c.PipeErr) && c.PipeErr[i-1] {
					what = "its output and errors go to"
				}
				ex.add("|", what, "operator", 0)
			}
			ex.command(x)
		}
	case *syntax.Block:
		ex.stmts(c.Stmts)
	case *syntax.Subshell:
		ex.stmts(c.Stmts)
	case *syntax.IfClause:
		ex.stmts(c.Cond)
		ex.stmts(c.Then)
		for _, e := range c.Elifs {
			ex.stmts(e.Cond)
			ex.stmts(e.Then)
		}
		ex.stmts(c.Else)
	case *syntax.WhileClause:
		ex.stmts(c.Cond)
		ex.stmts(c.Body)
	case *syntax.ForClause:
		ex.stmts(c.Body)
	case *syntax.CaseClause:
		for _, it := range c.Items {
			ex.stmts(it.Body)
		}
	}
}

func (ex *explainer) word(w *syntax.Word) exWord {
	// (the end can be past the line when a closing quote was made up)
	raw := ""
	if end := min(w.End, len(ex.src)); w.Pos >= 0 && w.Pos <= end {
		raw = ex.src[w.Pos:end]
	}
	v, ok := w.LitString()
	if !ok {
		v = raw
	}
	return exWord{raw: raw, value: v, literal: ok}
}

func (ex *explainer) call(assigns []*syntax.Assign, args []*syntax.Word) {
	for _, a := range assigns {
		val := ""
		if a.Value != nil {
			val = ex.word(a.Value).raw
		}
		desc := "set " + a.Name
		if len(args) > 0 {
			desc += " for this command"
		}
		ex.add(a.Name+"="+val, desc, "assign", 0)
	}
	words := make([]exWord, len(args))
	for i, w := range args {
		words[i] = ex.word(w)
	}
	for len(words) > 0 {
		words = ex.simple(words)
	}
}

// redirects explains redirections: what goes where.
func (ex *explainer) redirects(rs []*syntax.Redirect) {
	for _, r := range rs {
		word, target := "", ""
		if r.Target != nil {
			t := ex.word(r.Target)
			target = t.value
			if end := min(r.Target.End, len(ex.src)); r.Pos >= 0 && r.Pos <= end {
				word = ex.src[r.Pos:end]
			}
		}
		if word == "" {
			word = r.Op.String()
		}
		ex.add(word, redirectDesc(r.Op, r.Fd, target), "redirect", 1)
	}
}

func redirectDesc(op syntax.RedirOp, fd int, target string) string {
	stream := func(fd, def int) string {
		if fd < 0 {
			fd = def
		}
		switch fd {
		case 0:
			return "standard input"
		case 1:
			return "the output"
		case 2:
			return "errors"
		}
		return "descriptor " + strconv.Itoa(fd)
	}
	switch op {
	case syntax.RedirIn:
		return "read " + stream(fd, 0) + " from " + target
	case syntax.RedirOut, syntax.RedirClobber:
		return "write " + stream(fd, 1) + " to " + target + ", replacing it"
	case syntax.RedirNoClobber:
		return "write " + stream(fd, 1) + " to " + target + " unless it exists"
	case syntax.RedirAppend:
		return "add " + stream(fd, 1) + " to the end of " + target
	case syntax.RedirInOut:
		return "open " + target + " for reading and writing"
	case syntax.RedirDupIn, syntax.RedirDupOut:
		def := 1
		if op == syntax.RedirDupIn {
			def = 0
		}
		switch n, err := strconv.Atoi(target); {
		case target == "-":
			return "close " + stream(fd, def)
		case err == nil:
			return "send " + stream(fd, def) + " where " + stream(n, n) + " goes"
		}
		return "write the output and errors to " + target
	case syntax.RedirAll:
		return "write the output and errors to " + target + ", replacing it"
	case syntax.RedirAllAppend:
		return "add the output and errors to the end of " + target
	case syntax.RedirHeredoc, syntax.RedirHeredocTabs:
		return "the lines that follow are " + stream(fd, 0)
	case syntax.RedirHerestring:
		return "this text is " + stream(fd, 0)
	}
	return ""
}

// precommands run the command after their own options.
var precommands = map[string]string{ // command -> its options that take a value
	"sudo":       "ugCDhprtUR",
	"doas":       "uC",
	"command":    "",
	"builtin":    "",
	"exec":       "a",
	"time":       "",
	"nice":       "n",
	"nohup":      "",
	"env":        "uCSP",
	"caffeinate": "tw",
	"xargs":      "EIJLnPRSs",
	"watch":      "nd",
}

// simple explains one simple command. When the command runs another one
// (sudo git push), it returns the words of that one.
func (ex *explainer) simple(words []exWord) []exWord {
	sh := ex.sh
	cmd := words[0]
	if !cmd.literal {
		ex.add(cmd.raw, "", "arg", 0)
		for _, w := range words[1:] {
			ex.add(w.raw, "", "arg", 1)
		}
		return nil
	}
	desc, kind := sh.describeCommand(cmd.value)
	ex.add(cmd.raw, desc, kind, 0)
	if valued, ok := precommands[cmd.value]; ok {
		for i := 1; i < len(words); i++ {
			w := words[i]
			switch {
			case cmd.value == "env" && strings.Contains(w.value, "=") && !strings.HasPrefix(w.value, "-"):
				name, _, _ := strings.Cut(w.value, "=")
				ex.add(w.raw, "set "+name+" for the command", "assign", 1)
			case len(w.value) > 1 && w.value[0] == '-' && w.value != "--":
				rows, next := ex.option(words[:i], cmd.value, w.value)
				ex.rows = append(ex.rows, rows...)
				if next == "" && len(w.value) == 2 && strings.IndexByte(valued, w.value[1]) >= 0 {
					next = w.value
				}
				if next != "" && i+1 < len(words) {
					i++
					ex.add(words[i].raw, "argument of "+next, "value", 1)
				}
			case w.value == "--":
				ex.add(w.raw, "end of options", "option", 1)
			default:
				return words[i:]
			}
		}
		return nil
	}
	ex.args(words)
	return nil
}

func (ex *explainer) args(words []exWord) {
	cmd := words[0].value
	valueFor := ""
	switches := true
	for i := 1; i < len(words); i++ {
		w := words[i]
		switch {
		case valueFor != "":
			ex.add(w.raw, "argument of "+valueFor, "value", 1)
			valueFor = ""
		case switches && w.value == "--":
			ex.add(w.raw, "end of options", "option", 1)
			switches = false
		case switches && w.literal && len(w.value) > 1 && w.value[0] == '-':
			var rows []ExplainRow
			rows, valueFor = ex.option(words[:i], cmd, w.value)
			ex.rows = append(ex.rows, rows...)
		default:
			ex.add(w.raw, ex.argDesc(words[:i], w), "arg", 1)
		}
	}
}

// completionAt starts a completion run with the cursor on a new word
// after before, as if the user were about to type it.
func (ex *explainer) completionAt(before []exWord) *compRun {
	var b strings.Builder
	for _, w := range before {
		b.WriteString(w.raw)
		b.WriteByte(' ')
	}
	buf := b.String()
	return ex.sh.beginComplete(scanCompLine(buf, len(buf)))
}

// option explains an option word (-x, -xvf, --name, --name=value, -name)
// of cmd. If the option's argument is the next word, it returns the
// option's name.
func (ex *explainer) option(before []exWord, cmd, opt string) (rows []ExplainRow, valueFor string) {
	r := ex.completionAt(before)
	if r == nil {
		return []ExplainRow{{Word: opt, Kind: "option", Indent: 1}}, ""
	}
	defer r.finish()
	defer func() {
		if p := recover(); p != nil {
			rows, valueFor = []ExplainRow{{Word: opt, Kind: "option", Indent: 1}}, ""
		}
	}()
	r.load(cmd)
	opts := r.c.optsFor(r, cmd)
	find := func(kind compOptKind, name string) *compOpt {
		for i := len(opts) - 1; i >= 0; i-- {
			if o := opts[i]; o.kind == kind && o.name == name && r.condOK(o) {
				return o
			}
		}
		return nil
	}
	named := func(o *compOpt, word, inline string, hasInline bool) ([]ExplainRow, string) {
		desc := ""
		if o != nil {
			desc = o.desc
		} else {
			// cobra programs and -a lists describe their options as
			// candidates rather than specs
			desc = ex.candidateDesc(before, word)
		}
		rows := []ExplainRow{{Word: word, Desc: desc, Kind: "option", Indent: 1}}
		switch {
		case hasInline:
			rows = append(rows, ExplainRow{Word: inline, Desc: "argument of " + word, Kind: "value", Indent: 1})
		case o != nil && o.reqParam:
			return rows, word
		}
		return rows, ""
	}
	if strings.HasPrefix(opt, "--") {
		name, val, eq := strings.Cut(opt[2:], "=")
		return named(find(optLong, name), "--"+name, val, eq)
	}
	name, val, eq := strings.Cut(opt[1:], "=")
	if o := find(optOld, name); o != nil {
		return named(o, "-"+name, val, eq)
	}
	// a group of short options; one that takes an argument takes the
	// rest of the word, or the next word
	for j := 1; j < len(opt); j++ {
		o := find(optShort, opt[j:j+1])
		if o == nil {
			if j == 1 {
				return []ExplainRow{{Word: opt, Desc: ex.candidateDesc(before, opt), Kind: "option", Indent: 1}}, ""
			}
			if rest := opt[j:]; strings.Trim(rest, "0123456789") == "" {
				// -j8, -n5: a number stuck to the option before
				prev := rows[len(rows)-1].Word
				return append(rows, ExplainRow{Word: rest, Desc: "argument of " + prev, Kind: "value", Indent: 1}), ""
			}
			rows = append(rows, ExplainRow{Word: "-" + opt[j:j+1], Kind: "option", Indent: 1})
			continue
		}
		rows = append(rows, ExplainRow{Word: "-" + o.name, Desc: o.desc, Kind: "option", Indent: 1})
		if o.reqParam {
			if j+1 < len(opt) {
				rows = append(rows, ExplainRow{Word: opt[j+1:], Desc: "argument of -" + o.name, Kind: "value", Indent: 1})
				return rows, ""
			}
			return rows, "-" + o.name
		}
	}
	return rows, ""
}

// argDesc describes a plain argument: what completion says about it (a
// subcommand, a branch, ...) or what it is on disk.
func (ex *explainer) argDesc(before []exWord, w exWord) (desc string) {
	if !w.literal {
		return ""
	}
	if d := ex.candidateDesc(before, w.raw); d != "" {
		return d
	}
	if st, err := os.Stat(ex.sh.absPath(ex.sh.expandTildeString(w.value))); err == nil {
		if st.IsDir() {
			return "directory"
		}
		return "file"
	}
	return ""
}

// candidateDesc completes word after before and returns the description
// of the candidate that is exactly word.
func (ex *explainer) candidateDesc(before []exWord, word string) string {
	var b strings.Builder
	for _, x := range before {
		b.WriteString(x.raw)
		b.WriteByte(' ')
	}
	b.WriteString(word)
	buf := b.String()
	res, _ := ex.sh.completeLine(buf, len(buf))
	want := word
	if v, ok := literalValue(word); ok {
		want = v
	}
	for _, it := range res.Items {
		if it.Label() == want && it.Description != "" {
			return it.Description
		}
	}
	return ""
}

// literalValue unquotes a word that needs no expansion.
func literalValue(raw string) (string, bool) {
	f, err := syntax.Parse("x " + raw)
	if err != nil || len(f.Stmts) != 1 {
		return "", false
	}
	call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) != 2 {
		return "", false
	}
	return call.Args[1].LitString()
}

// describeCommand says what a command name runs.
func (sh *Shell) describeCommand(name string) (desc, kind string) {
	if fn := sh.lookupFunc(name); fn != nil {
		if fn.Description != "" {
			return fn.Description, "command"
		}
		return "function", "command"
	}
	if _, ok := builtins[name]; ok {
		return builtinHelp[name], "command"
	}
	if v, ok := sh.aliases[name]; ok {
		return "alias for " + v, "command"
	}
	for _, a := range sh.Abbrs() {
		if a.Name == name && a.Regex == nil && a.Function == "" {
			return "abbreviation for " + a.Expansion, "command"
		}
	}
	if p, ok := sh.LookPath(name); ok {
		if d := sh.manSummary(name, p); d != "" {
			return d, "command"
		}
		return p, "command"
	}
	msg := "not found"
	if s := sh.suggestCommand(name); s != "" {
		msg += " (did you mean " + s + "?)"
	}
	return msg, "missing"
}

// manSummaries caches the one-line summaries of man pages.
var manSummaries sync.Map // name -> string

// manSummary returns the summary from the NAME section of a program's man
// page. It looks next to the program, the way man does, and reads the
// page itself: whatis and apropos can take seconds.
func (sh *Shell) manSummary(name, path string) string {
	if v, ok := manSummaries.Load(name); ok {
		return v.(string)
	}
	var dirs []string
	bin := filepath.Dir(path)
	dirs = append(dirs, filepath.Join(bin, "..", "share", "man"), filepath.Join(bin, "..", "man"))
	for _, d := range strings.Split(sh.Get("MANPATH"), ":") {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	dirs = append(dirs, systemManDirs()...)
	desc := findManSummary(name, dirs)
	if desc == "" && runtime.GOOS == "darwin" {
		// /usr/bin/make and friends are Xcode's, and so are their pages
		desc = findManSummary(name, xcodeManDirs())
	}
	manSummaries.Store(name, desc)
	return desc
}

func findManSummary(name string, dirs []string) string {
	for _, d := range dirs {
		for _, sec := range [...]string{"1", "8", "6"} {
			for _, ext := range [...]string{"", ".gz"} {
				if desc := readManSummary(filepath.Join(d, "man"+sec, name+"."+sec+ext), 0); desc != "" {
					return desc
				}
			}
		}
	}
	return ""
}

// systemManDirs returns the standard directories and the ones listed in
// /etc/manpaths and /etc/manpaths.d (macOS).
var systemManDirs = sync.OnceValue(func() []string {
	dirs := []string{"/usr/share/man", "/usr/local/share/man", "/opt/homebrew/share/man"}
	files := []string{"/etc/manpaths"}
	if more, err := filepath.Glob("/etc/manpaths.d/*"); err == nil {
		files = append(files, more...)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				dirs = append(dirs, l)
			}
		}
	}
	return dirs
})

// xcodeManDirs returns the man directories of the selected Xcode or
// command line tools.
var xcodeManDirs = sync.OnceValue(func() []string {
	out, err := exec.Command("/usr/bin/xcode-select", "-p").Output()
	if err != nil {
		return nil
	}
	dev := strings.TrimSpace(string(out))
	return []string{filepath.Join(dev, "usr", "share", "man"), filepath.Join(dev, "Toolchains", "XcodeDefault.xctoolchain", "usr", "share", "man")}
})

// readManSummary reads the NAME section of a man page in mdoc (.Nd) or
// man (.SH NAME) format.
func readManSummary(path string, depth int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		z, err := gzip.NewReader(f)
		if err != nil {
			return ""
		}
		defer z.Close()
		r = z
	}
	sc := bufio.NewScanner(io.LimitReader(r, 64<<10))
	inName := false
	var text []string
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, ".so ") && depth == 0:
			// the page is another page
			target := strings.TrimSpace(l[4:])
			root := filepath.Dir(filepath.Dir(path))
			return readManSummary(filepath.Join(root, target), 1)
		case strings.HasPrefix(l, ".Nd "):
			return troffText(l[4:])
		case strings.HasPrefix(l, ".SH") || strings.HasPrefix(l, ".Sh"):
			if inName {
				return nameSummary(strings.Join(text, " "))
			}
			h := strings.Trim(strings.TrimSpace(l[3:]), `"`)
			inName = strings.EqualFold(h, "NAME")
		case inName && !strings.HasPrefix(l, "."):
			text = append(text, l)
		}
	}
	if inName {
		return nameSummary(strings.Join(text, " "))
	}
	return ""
}

// nameSummary takes "git \- the stupid content tracker" apart.
func nameSummary(s string) string {
	s = troffText(s)
	for _, sep := range [...]string{" - ", " — ", " -- "} {
		if _, after, ok := strings.Cut(s, sep); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// troffText removes the common troff escapes from s.
func troffText(s string) string {
	r := strings.NewReplacer(`\-`, "-", `\(em`, "—", `\(en`, "–", `\&`, "", `\e`, `\`, `\ `, " ",
		`\fB`, "", `\fI`, "", `\fR`, "", `\fP`, "", `\f(CW`, "", `\(aq`, "'", `\*(lq`, `"`, `\*(rq`, `"`)
	s = r.Replace(s)
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(s), `"`))
}

// FormatExplain lays out rows in two columns, with colors if color is set.
func (sh *Shell) FormatExplain(rows []ExplainRow, color bool) string {
	width := 0
	for _, r := range rows {
		if w := 2*r.Indent + textWidth(r.Word); w > width {
			width = w
		}
	}
	if width > 24 {
		width = 24
	}
	paint := func(role, s string) string {
		if !color || s == "" {
			return s
		}
		st := sh.Style(role)
		if !st.Set {
			return s
		}
		return st.SGR() + s + "\x1b[0m"
	}
	var b strings.Builder
	for _, r := range rows {
		indent := strings.Repeat("  ", r.Indent)
		word := r.Word
		if strings.ContainsAny(word, "\n") {
			word = strings.ReplaceAll(word, "\n", "⏎")
		}
		pad := width - 2*r.Indent - textWidth(word)
		if pad < 0 {
			pad = 0
		}
		role := map[string]string{"command": "command", "option": "option", "value": "quote", "arg": "param",
			"assign": "param", "missing": "error", "redirect": "redirection", "operator": "operator"}[r.Kind]
		b.WriteString(indent + paint(role, word))
		if r.Desc != "" {
			desc := strings.Join(strings.Fields(r.Desc), " ")
			if r.Kind == "value" || r.Kind == "missing" || desc == "file" || desc == "directory" {
				desc = paint("autosuggestion", desc)
			}
			b.WriteString(strings.Repeat(" ", pad) + "  " + desc)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
