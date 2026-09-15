package shell

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// Syntax highlighting and autosuggestion validation for the line editor.
//
// The highlighter is a small, forgiving scanner rather than the parser:
// it must cope with every half-typed state of a command line (open
// quotes, unfinished blocks, a trailing pipe) and still color the
// complete parts. It understands the same blend of sh and fish syntax as
// the parser and never runs code. The file system is only consulted to
// validate command names, paths and redirection targets; every check is
// cached for the duration of a call, and once a call has spent hlBudget
// on such checks the rest are skipped (giving the benefit of the doubt).

// hlRole is the highlight role of one byte of the input.
type hlRole uint8

const (
	hlNormal hlRole = iota
	hlCommand
	hlError
	hlKeyword
	hlParam
	hlOption
	hlQuote
	hlEscape
	hlVariable
	hlOperator
	hlEnd
	hlRedirection
	hlComment
	hlNumRoles

	// hlPlain marks a word's plain text until the word is classified as a
	// command, parameter, option or redirection target.
	hlPlain hlRole = 0x3f
	// hlValidPath is or'ed into the roles of an argument naming a file.
	hlValidPath hlRole = 0x80
	hlRoleMask  hlRole = 0x7f
)

var hlRoleNames = [hlNumRoles]string{
	hlNormal:      "normal",
	hlCommand:     "command",
	hlError:       "error",
	hlKeyword:     "keyword",
	hlParam:       "param",
	hlOption:      "option",
	hlQuote:       "quote",
	hlEscape:      "escape",
	hlVariable:    "variable",
	hlOperator:    "operator",
	hlEnd:         "end",
	hlRedirection: "redirection",
	hlComment:     "comment",
}

// hlBudget caps the time one call spends before file system checks are
// skipped, so a slow disk never makes typing lag.
const hlBudget = 10 * time.Millisecond

// hlFoldCase: file names compare case-insensitively (macOS default).
var hlFoldCase = runtime.GOOS == "darwin"

// Highlight returns the style of each rune of buf as an SGR escape
// sequence ("" for the default style), for the line editor to draw as the
// user types. Each entry is a complete style meant to be applied after a
// reset. Any input is accepted, complete or not.
//
// It never runs code or spawns processes. Like the rest of the
// interpreter it must not be called while a command is running.
func (sh *Shell) Highlight(buf string) []string {
	return sh.highlight(buf, false)
}

// highlight is Highlight with a choice of quoting rules (fish: \' inside
// single quotes, literal backquotes), as used by the parser for .fish
// files.
func (sh *Shell) highlight(buf string, fish bool) (out []string) {
	if buf == "" {
		return nil
	}
	h := sh.newHighlighter(buf)
	h.fish = fish
	defer func() {
		if recover() != nil {
			// A bug here must not take the shell down: draw plain text.
			out = make([]string, utf8.RuneCountInString(buf))
		}
	}()
	h.list(0, 0)
	out = h.render()
	h.release()
	return out
}

// SuggestionOK reports whether a history entry should still be offered as
// an autosuggestion: its first command must still exist, and "cd DIR" must
// name a directory (relative to the current one, or found in CDPATH) other
// than the current directory. It never runs code.
func (sh *Shell) SuggestionOK(line string) (ok bool) {
	h := sh.newHighlighter(line)
	h.probe = true
	defer func() {
		if recover() != nil {
			ok = true
		}
	}()
	h.list(0, 0)
	ok = !h.bad
	h.release()
	return ok
}

type highlighter struct {
	sh    *Shell
	src   string
	lim   int // end of the text being scanned (shorter inside `...`)
	col   []hlRole
	fish  bool // fish quoting rules
	probe bool // SuggestionOK: look at the first command only
	done  bool // probe finished
	bad   bool // probe verdict: hide the suggestion
	depth int  // command substitution nesting

	stack    []hlFrame
	heredocs []hlHeredoc
	lit      []byte  // value of the word being scanned
	w        hlWord  // what the last word scan found
	plainAt  []int32 // positions marked hlPlain and not yet classified

	// Scanning stays linear on hostile input (a pasted blob of braces,
	// say): look-ahead scans for a closing bracket are charged to ahead,
	// and once it runs out the constructs are taken as plain text;
	// nesting is capped like the parser does.
	ahead     int
	classFail int // a [ before this can't start a glob class
	nest      int

	cmds  []hlCmdCheck
	stats []hlStat
	start time.Time
	fsOff bool // over budget: file system checks are skipped

	sgr   [2 * hlNumRoles]string
	sgrOK [2 * hlNumRoles]bool
}

var hlPool = sync.Pool{New: func() any { return new(highlighter) }}

func (sh *Shell) newHighlighter(src string) *highlighter {
	h := hlPool.Get().(*highlighter)
	h.sh, h.src, h.lim = sh, src, len(src)
	if cap(h.col) < len(src) {
		h.col = make([]hlRole, len(src))
	} else {
		h.col = h.col[:len(src)]
		clear(h.col)
	}
	h.fish, h.probe, h.done, h.bad, h.fsOff = false, false, false, false, false
	h.depth, h.nest, h.classFail = 0, 0, 0
	h.ahead = 8*len(src) + 4096
	h.stack = h.stack[:0]
	h.heredocs = h.heredocs[:0]
	h.lit = h.lit[:0]
	h.plainAt = h.plainAt[:0]
	h.cmds = h.cmds[:0]
	h.stats = h.stats[:0]
	h.start = time.Now()
	h.sgrOK = [2 * hlNumRoles]bool{}
	return h
}

func (h *highlighter) release() {
	h.sh, h.src = nil, ""
	clear(h.cmds[:cap(h.cmds)])
	clear(h.stats[:cap(h.stats)])
	clear(h.heredocs[:cap(h.heredocs)])
	clear(h.sgr[:])
	if cap(h.col) > 1<<16 {
		h.col = nil
	}
	if cap(h.lit) > 1<<16 {
		h.lit = nil
	}
	if cap(h.plainAt) > 1<<14 {
		h.plainAt = nil
	}
	if cap(h.stack) > 1<<10 {
		h.stack = nil
	}
	hlPool.Put(h)
}

// ---------------------------------------------------------------------
// Blocks and command state

type hlFrameKind uint8

const (
	frIf hlFrameKind = iota + 1
	frLoop
	frCase   // case ... esac
	frSwitch // switch ... end
	frBegin
	frFunc
	frBrace
	frParen
	frSubst // a command substitution: blocks outside it can't close inside
)

type hlFrame struct {
	kind  hlFrameKind
	posix bool  // if: saw then (ends with fi); loop: saw do (ends with done)
	phase uint8 // if: 0 condition, 1 then, 2 else; case: 0 patterns, 1 body; func: 1 = a { body may follow
}

func (h *highlighter) top() *hlFrame {
	if n := len(h.stack); n > 0 {
		return &h.stack[n-1]
	}
	return nil
}

func (h *highlighter) push(k hlFrameKind) { h.stack = append(h.stack, hlFrame{kind: k}) }
func (h *highlighter) pop()               { h.stack = h.stack[:len(h.stack)-1] }

type hlState uint8

const (
	stCmd        hlState = iota // command position
	stArgs                      // arguments
	stDeco                      // after sudo, env, command...: their options
	stDecoArg                   // the value of such an option
	stClosed                    // after a compound command: redirections only
	stForVar                    // for _
	stForIn                     // for x _
	stCaseWord                  // case _
	stCaseIn                    // case x _
	stCasePat                   // case x in _)
	stSwitchWord                // switch _
	stPatterns                  // fish: case _ _
	stFuncName                  // function _
	stFuncArgs                  // function name _ _
)

// Where a command starts, which limits the keywords allowed there.
const (
	atStmt uint8 = iota // start of a statement
	atPipe              // after && or ||: no and/or
	atNeg               // after ! not time and or: no and/or
	atCmd               // after |, a redirection or an assignment
)

// hlKind says which commands are acceptable in command position.
type hlKind uint8

const (
	kindAny      hlKind = iota // alias, function, builtin, abbreviation, program, directory
	kindExternal               // programs only: command, exec, sudo, env...
	kindBuiltin                // builtins only
)

type hlCmd struct {
	state    hlState
	at       uint8
	kind     hlKind // how to validate the next command word
	kwOK     bool   // keywords are recognized in command position
	assignOK bool   // NAME=value is an assignment in command position
	needCmd  bool   // a command must come before ; or &
	args     bool   // check whether arguments name files
	cd       bool   // arguments are cd targets
	dashdash bool   // "--" seen: no more options
	patStart bool   // case: at the start of a pattern list
	elseIf   bool   // fish: "else" may be followed by "if"
	funcOpts bool   // the fish function header has options
	funcSh   bool   // function name() { ... }
	deco     *hlDeco
}

func (c *hlCmd) begin(at uint8) { *c = hlCmd{state: stCmd, at: at, kwOK: true, assignOK: true} }
func (c *hlCmd) closed()        { *c = hlCmd{state: stClosed} }

// hlDeco describes a command that runs the command named after it.
type hlDeco struct {
	kind    hlKind   // what the decorated command may be
	argOpts string   // short options that take a value
	longArg []string // long options that take a value
	assigns bool     // NAME=value words may come first
	query   string   // options that turn the names into lookups (command -v)
	stop    string   // options after which no command follows (sudo -e)
}

var hlDecorators = map[string]*hlDeco{
	"command": {kind: kindExternal, query: "aqsvV"},
	"builtin": {kind: kindBuiltin},
	"exec":    {kind: kindExternal, argOpts: "a"},
	"sudo": {kind: kindExternal, argOpts: "CDgpRrTtUu", assigns: true, stop: "eKlVv",
		longArg: []string{"--chdir", "--close-from", "--command-timeout", "--group", "--host", "--other-user", "--prompt", "--role", "--type", "--user"}},
	"doas":  {kind: kindExternal, argOpts: "Cu"},
	"env":   {kind: kindExternal, argOpts: "CLPSUu", assigns: true, longArg: []string{"--chdir", "--split-string", "--unset"}},
	"nohup": {kind: kindExternal},
	"nice":  {kind: kindExternal, argOpts: "n", longArg: []string{"--adjustment"}},
}

// hlTime is the time keyword, which takes -p and then any command.
var hlTime = &hlDeco{kind: kindAny}

var hlKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"for": true, "in": true, "do": true, "done": true, "while": true, "until": true,
	"case": true, "esac": true, "switch": true, "function": true, "end": true, "begin": true,
	"and": true, "or": true, "not": true, "time": true, "!": true, "{": true, "}": true,
	"[[": true, "]]": true, "break": true, "continue": true, "return": true,
}

type hlHeredoc struct {
	delim  string
	strip  bool // <<-
	quoted bool // no expansions in the body
}

// ---------------------------------------------------------------------
// Statements

// list highlights statements from pos until the end of the input or, when
// closer is ')', an unmatched ')', whose position it returns.
func (h *highlighter) list(pos int, closer byte) int {
	var c hlCmd
	c.begin(atStmt)
	src := h.src
	for !h.done {
		pos = h.blanks(pos)
		if pos >= h.lim {
			break
		}
		if c.state == stCasePat {
			pos = h.casePattern(pos, &c)
			continue
		}
		switch src[pos] {
		case '\n':
			h.newline(&c)
			pos = h.heredocBodies(pos + 1)
			continue
		case '#':
			pos = h.comment(pos)
			continue
		case ';':
			pos = h.semicolon(pos, &c)
			continue
		case '|':
			n, at := 1, atCmd
			if pos+1 < h.lim {
				switch src[pos+1] {
				case '|':
					n, at = 2, atPipe
				case '&':
					n = 2
				}
			}
			pos = h.join(pos, n, at, &c)
			continue
		case '&':
			next := byte(0)
			if pos+1 < h.lim {
				next = src[pos+1]
			}
			switch next {
			case '&':
				pos = h.join(pos, 2, atPipe, &c)
				continue
			case '|':
				pos = h.join(pos, 2, atCmd, &c)
				continue
			case '>':
				// &> is a redirection
			case '!':
				pos = h.background(pos, 2, &c)
				continue
			default:
				pos = h.background(pos, 1, &c)
				continue
			}
		case ')':
			if f := h.top(); f != nil && f.kind == frParen {
				h.col[pos] = hlOperator
				h.pop()
				h.endCommand(&c)
				c.closed()
				pos++
				continue
			}
			if closer == ')' {
				h.endCommand(&c)
				return pos
			}
			h.col[pos] = hlError
			c.begin(atStmt)
			pos++
			continue
		}
		if h.fish {
			if n := h.fishPipe(pos); n > 0 {
				pos = h.join(pos, n, atCmd, &c)
				continue
			}
		}
		if end := h.redirect(pos, &c); end >= 0 {
			pos = end
			continue
		}
		end := h.token(pos, &c)
		if end <= pos {
			h.col[pos] = hlError
			end = pos + 1
		}
		pos = end
	}
	return pos
}

// join handles | && || |& &|.
func (h *highlighter) join(pos, n int, at uint8, c *hlCmd) int {
	role := hlEnd
	if c.state == stCmd && (c.needCmd || c.at == atStmt) {
		role = hlError // nothing to connect
	}
	h.fill(pos, pos+n, role)
	h.endCommand(c)
	c.begin(at)
	c.needCmd = true
	return pos + n
}

// background handles & and &!.
func (h *highlighter) background(pos, n int, c *hlCmd) int {
	role := hlEnd
	if c.state == stCmd && (c.needCmd || c.at == atStmt) {
		role = hlError
	}
	h.fill(pos, pos+n, role)
	h.endCommand(c)
	c.begin(atStmt)
	return pos + n
}

// semicolon handles ; and the case terminators ;; ;& ;;&.
func (h *highlighter) semicolon(pos int, c *hlCmd) int {
	src := h.src
	n := 0
	if pos+1 < h.lim && src[pos+1] == ';' {
		n = 2
		if pos+2 < h.lim && src[pos+2] == '&' {
			n = 3
		}
	} else if pos+1 < h.lim && src[pos+1] == '&' {
		n = 2
	}
	if n > 0 {
		h.endCommand(c)
		if f := h.top(); f != nil && f.kind == frCase && f.phase == 1 {
			f.phase = 0
			h.fill(pos, pos+n, hlEnd)
			c.begin(atStmt)
			c.state, c.patStart = stCasePat, true
			return pos + n
		}
		h.fill(pos, pos+n, hlError)
		c.begin(atStmt)
		return pos + n
	}
	role := hlEnd
	if c.state == stCmd && c.needCmd {
		role = hlError
	}
	h.col[pos] = role
	h.endCommand(c)
	c.begin(atStmt)
	return pos + 1
}

func (h *highlighter) newline(c *hlCmd) {
	switch c.state {
	case stCaseIn, stCasePat:
		return // "in" and patterns may come on the next line
	case stCmd:
		if c.needCmd {
			return // after | && || if ...: the command may come on the next line
		}
	}
	h.endCommand(c)
	c.begin(atStmt)
}

// endCommand finishes the current command at a terminator.
func (h *highlighter) endCommand(c *hlCmd) {
	switch c.state {
	case stFuncName, stFuncArgs:
		if !c.funcSh {
			// a fish function body follows, up to "end"
			h.push(frFunc)
			if !c.funcOpts {
				h.top().phase = 1
			}
		}
	}
	if h.probe && h.depth == 0 && c.cd {
		h.done = true // cd without arguments
	}
}

func (h *highlighter) token(pos int, c *hlCmd) int {
	src := h.src
	switch c.state {
	case stCmd:
		return h.command(pos, c)
	case stDeco:
		return h.decoWord(pos, c)
	case stDecoArg:
		c.state = stDeco
		return h.plainWord(pos, hlParam)
	case stClosed:
		end := h.scan(pos, wcNormal)
		h.errorWord(pos, end)
		return end
	case stForVar:
		if src[pos] == '(' && pos+1 < h.lim && src[pos+1] == '(' {
			c.begin(atStmt)
			if end, ok := h.arithEnd(pos+2, true); ok {
				return h.arith(pos, end)
			}
			h.fill(pos, pos+2, hlOperator)
			return pos + 2
		}
		c.state = stForIn
		return h.plainWord(pos, hlParam)
	case stForIn:
		if h.wordAt(pos, "in") {
			h.fill(pos, pos+2, hlKeyword)
			c.state, c.args = stArgs, !h.probe
			return pos + 2
		}
		if h.wordAt(pos, "do") {
			c.begin(atStmt)
			return h.command(pos, c)
		}
		end := h.scan(pos, wcNormal)
		h.errorWord(pos, end)
		c.state = stArgs
		return end
	case stCaseWord:
		c.state = stCaseIn
		return h.plainWord(pos, hlParam)
	case stCaseIn:
		c.state, c.patStart = stCasePat, true
		if h.wordAt(pos, "in") {
			h.fill(pos, pos+2, hlKeyword)
			return pos + 2
		}
		end := h.scan(pos, wcNormal)
		h.errorWord(pos, end)
		return end
	case stSwitchWord:
		c.state = stArgs
		return h.plainWord(pos, hlParam)
	case stPatterns:
		return h.plainWord(pos, hlParam)
	case stFuncName:
		c.state = stFuncArgs
		return h.plainWord(pos, hlParam)
	case stFuncArgs:
		return h.funcArg(pos, c)
	}
	// stArgs
	if h.fish && h.wordAt(pos, "}") {
		// fish closes { } blocks in argument position too
		if f := h.top(); f != nil && f.kind == frBrace {
			h.col[pos] = hlKeyword
			h.pop()
			h.endCommand(c)
			c.closed()
			return pos + 1
		}
	}
	return h.arg(pos, c)
}

// command handles a word in command position: a keyword, an assignment,
// a subshell, a function definition or a command name.
func (h *highlighter) command(pos int, c *hlCmd) int {
	src := h.src
	fresh := false // first command of a fish function without options
	if f := h.top(); f != nil && f.kind == frFunc && f.phase == 1 {
		f.phase = 0
		fresh = true
	}
	if c.kwOK && src[pos] == '(' {
		c.needCmd = false
		if pos+1 < h.lim && src[pos+1] == '(' {
			if end, ok := h.arithEnd(pos+2, false); ok {
				end = h.arith(pos, end)
				h.endCommand(c)
				c.closed()
				return end
			}
		}
		h.col[pos] = hlOperator
		h.push(frParen)
		c.begin(atStmt)
		return pos + 1
	}
	if c.assignOK {
		if end := h.assignment(pos, true); end >= 0 {
			c.at, c.needCmd = atCmd, false
			return end
		}
	}
	if c.kwOK && c.kind == kindAny {
		if end := h.funcDef(pos, c); end >= 0 {
			return end
		}
	}
	end := h.scan(pos, wcNormal)
	if end == pos {
		return pos
	}
	if c.kwOK && h.w.static && !h.w.quoted && hlKeywords[src[pos:end]] {
		return h.keyword(src[pos:end], pos, end, c, fresh)
	}
	name, known := h.value(true)
	role := hlCommand
	if known && !h.commandOK(name, c.kind) {
		role = hlError
	}
	if role == hlError {
		h.fill(pos, end, hlError)
	} else {
		h.classify(pos, end, hlCommand)
	}
	c.needCmd = false
	if h.probe && h.depth == 0 && known && role == hlError {
		h.bad, h.done = true, true
		return end
	}
	if d := hlDecorators[name]; d != nil && known {
		c.state, c.deco, c.kind, c.at = stDeco, d, d.kind, atCmd
		c.kwOK, c.assignOK = false, false
		return end
	}
	c.state, c.deco = stArgs, nil
	c.args = !h.probe
	c.cd = known && name == "cd"
	if h.probe && h.depth == 0 && !c.cd {
		h.done = true
	}
	return end
}

// decoWord handles the words after a decorator like sudo or env: its
// options, NAME=value for env, and then the command it runs.
func (h *highlighter) decoWord(pos int, c *hlCmd) int {
	src := h.src
	d := c.deco
	if d.assigns {
		if end := h.assignment(pos, false); end >= 0 {
			return end
		}
	}
	if src[pos] == '-' && pos+1 < h.lim && !hlIsMeta(src[pos+1]) {
		end := h.plainWord(pos, hlOption)
		opt := src[pos:end]
		switch {
		case opt == "--":
			c.state = stCmd
		case strings.HasPrefix(opt, "--"):
			if strings.IndexByte(opt, '=') < 0 && hlContains(d.longArg, opt) {
				c.state = stDecoArg
			}
		default:
			for i := 1; i < len(opt); i++ {
				ch := opt[i]
				if strings.IndexByte(d.stop, ch) >= 0 {
					c.state, c.args, c.deco = stArgs, !h.probe, nil
					if h.probe && h.depth == 0 {
						h.done = true
					}
					return end
				}
				if strings.IndexByte(d.query, ch) >= 0 {
					c.kind = kindAny
				}
				if strings.IndexByte(d.argOpts, ch) >= 0 {
					if i == len(opt)-1 {
						c.state = stDecoArg
					}
					break
				}
			}
		}
		return end
	}
	c.state = stCmd
	return h.command(pos, c)
}

func hlContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// keyword handles a reserved word in command position.
func (h *highlighter) keyword(kw string, pos, end int, c *hlCmd, fresh bool) int {
	ok := true
	f := h.top()
	is := func(k hlFrameKind) bool { return f != nil && f.kind == k }
	switch kw {
	case "and", "or":
		ok = c.at == atStmt
		c.begin(atNeg)
		c.needCmd = true
	case "!", "not":
		ok = c.at != atCmd
		c.begin(atNeg)
		c.needCmd = true
	case "time":
		ok = c.at != atCmd
		c.begin(atNeg)
		c.state, c.deco, c.kind = stDeco, hlTime, kindAny
	case "if":
		if c.elseIf && is(frIf) {
			f.phase = 0 // fish: else if
		} else {
			h.push(frIf)
		}
		c.begin(atStmt)
		c.needCmd = true
	case "then":
		ok = c.at == atStmt && is(frIf) && f.phase == 0
		if ok {
			f.posix, f.phase = true, 1
		}
		c.begin(atStmt)
	case "elif":
		ok = c.at == atStmt && is(frIf) && f.posix && f.phase == 1
		if ok {
			f.phase = 0
		}
		c.begin(atStmt)
		c.needCmd = true
	case "else":
		ok = c.at == atStmt && is(frIf) && (f.phase == 1 || !f.posix && f.phase == 0)
		if ok {
			f.phase = 2
		}
		c.begin(atStmt)
		c.elseIf = ok && !f.posix
	case "fi":
		ok = c.at == atStmt && is(frIf) && f.posix && f.phase != 0
		return h.closer(ok, pos, end, c)
	case "while", "until":
		h.push(frLoop)
		c.begin(atStmt)
		c.needCmd = true
	case "for":
		h.push(frLoop)
		c.begin(atStmt)
		c.state = stForVar
	case "do":
		ok = c.at == atStmt && is(frLoop) && !f.posix
		if ok {
			f.posix = true
		}
		c.begin(atStmt)
	case "done":
		ok = c.at == atStmt && is(frLoop) && f.posix
		return h.closer(ok, pos, end, c)
	case "case":
		if is(frSwitch) {
			ok = c.at == atStmt
			c.begin(atStmt)
			c.state = stPatterns
		} else {
			h.push(frCase)
			c.begin(atStmt)
			c.state = stCaseWord
		}
	case "esac":
		ok = c.at == atStmt && is(frCase)
		return h.closer(ok, pos, end, c)
	case "switch":
		h.push(frSwitch)
		c.begin(atStmt)
		c.state = stSwitchWord
	case "begin":
		h.push(frBegin)
		c.begin(atStmt)
	case "end":
		ok = c.at == atStmt && f != nil && (f.kind == frIf && !f.posix || f.kind == frLoop && !f.posix ||
			f.kind == frSwitch || f.kind == frBegin || f.kind == frFunc)
		return h.closer(ok, pos, end, c)
	case "function":
		c.begin(atStmt)
		c.state = stFuncName
		if h.probe && h.depth == 0 {
			h.done = true // defining a function runs nothing
		}
	case "{":
		if fresh && is(frFunc) {
			h.pop() // function name; { ... } is an sh body after all
		}
		h.push(frBrace)
		c.begin(atStmt)
	case "}":
		ok = c.at == atStmt && is(frBrace)
		return h.closer(ok, pos, end, c)
	case "[[":
		h.fill(pos, end, hlKeyword)
		c.needCmd = false
		return h.testClause(end, c)
	case "break", "continue", "return":
		c.state, c.needCmd, c.deco = stArgs, false, nil
		if h.probe && h.depth == 0 {
			h.done = true
		}
	default: // "in", "]]"
		ok = false
		c.state, c.needCmd = stArgs, false
	}
	role := hlKeyword
	if !ok {
		role = hlError
	}
	h.fill(pos, end, role)
	return end
}

// closer handles fi, done, esac, end and }.
func (h *highlighter) closer(ok bool, pos, end int, c *hlCmd) int {
	if !ok {
		h.fill(pos, end, hlError)
		c.state, c.needCmd = stArgs, false
		return end
	}
	h.fill(pos, end, hlKeyword)
	h.pop()
	c.closed()
	return end
}

// funcDef recognizes an sh function definition: name() body.
func (h *highlighter) funcDef(pos int, c *hlCmd) int {
	src := h.src
	i := pos
	for i < h.lim {
		ch := src[i]
		if hlIsMeta(ch) || ch == '"' || ch == '\'' || ch == '$' || ch == '\\' || ch == '`' || ch == '=' {
			break
		}
		i++
	}
	if i == pos || hlKeywords[src[pos:i]] {
		return -1 // keywords come first: "if ()" runs a subshell
	}
	j := i
	for j < h.lim && (src[j] == ' ' || src[j] == '\t') {
		j++
	}
	if j >= h.lim || src[j] != '(' {
		return -1
	}
	k := j + 1
	for k < h.lim && (src[k] == ' ' || src[k] == '\t') {
		k++
	}
	if k >= h.lim || src[k] != ')' {
		return -1
	}
	h.fill(pos, i, hlCommand)
	h.col[j], h.col[k] = hlOperator, hlOperator
	c.begin(atCmd)
	c.needCmd = true // the body
	if h.probe && h.depth == 0 {
		h.done = true
	}
	return k + 1
}

// funcArg handles the words after "function name".
func (h *highlighter) funcArg(pos int, c *hlCmd) int {
	src := h.src
	if src[pos] == '(' {
		k := pos + 1
		for k < h.lim && (src[k] == ' ' || src[k] == '\t') {
			k++
		}
		if k >= h.lim || src[k] == ')' {
			h.col[pos] = hlOperator
			c.funcSh = true
			if k >= h.lim {
				return k
			}
			h.col[k] = hlOperator
			return k + 1
		}
	}
	if !c.funcOpts && h.wordAt(pos, "{") {
		h.col[pos] = hlKeyword
		h.push(frBrace)
		c.begin(atStmt)
		return pos + 1
	}
	end := h.scan(pos, wcNormal)
	role := hlParam
	if src[pos] == '-' && end-pos > 1 {
		role = hlOption
	}
	h.classify(pos, end, role)
	c.funcOpts = true
	return end
}

// arg handles an argument of a simple command.
func (h *highlighter) arg(pos int, c *hlCmd) int {
	end := h.scan(pos, wcNormal)
	if end == pos {
		return pos
	}
	src := h.src
	role := hlParam
	if !c.dashdash && src[pos] == '-' && end-pos > 1 {
		role = hlOption
		if end-pos == 2 && src[pos+1] == '-' {
			c.dashdash = true
		}
	}
	h.classify(pos, end, role)
	switch {
	case c.cd:
		h.cdArg(pos, end, role)
	case c.args && role == hlParam && h.w.static:
		if p, ok := h.value(false); ok && h.isPath(p, end == len(src), false) {
			h.markPath(pos, end)
		}
	}
	return end
}

// cdArg checks an argument of cd: a directory, or an error.
func (h *highlighter) cdArg(pos, end int, role hlRole) {
	if role == hlOption {
		return
	}
	v, ok := h.value(false)
	if h.probe {
		if h.depth == 0 {
			if ok && !h.cdProbe(v) {
				h.bad = true
			}
			h.done = true
		}
		return
	}
	if !ok || v == "-" {
		return
	}
	if h.cdTarget(v, end == len(h.src)) {
		h.markPath(pos, end)
	} else if !h.fsOff {
		h.fill(pos, end, hlError)
	}
}

// assignment handles NAME=value (and, for the shell itself, NAME+=value,
// NAME[i]=value and NAME=(list)). It returns -1 if there is none at pos.
func (h *highlighter) assignment(pos int, shell bool) int {
	src := h.src
	i := pos
	if i >= h.lim || !isNameStartByte(src[i]) {
		return -1
	}
	for i < h.lim && isNameByte(src[i]) {
		i++
	}
	nameEnd := i
	idx, idxEnd := -1, -1
	if shell && i < h.lim && src[i] == '[' {
		if !h.lookAhead() {
			return -1
		}
		depth := 0
		j := i
		for ; j < h.lim; j++ {
			if src[j] == '[' {
				depth++
			} else if src[j] == ']' {
				depth--
				if depth == 0 {
					break
				}
			} else if src[j] == '\n' {
				break
			}
		}
		h.charge(j - i)
		if j >= h.lim || src[j] != ']' {
			return -1
		}
		idx, idxEnd = i, j
		i = j + 1
	}
	op := 0
	if shell && i+1 < h.lim && src[i] == '+' && src[i+1] == '=' {
		op = 2
	} else if i < h.lim && src[i] == '=' {
		op = 1
	} else {
		return -1
	}
	h.fill(pos, nameEnd, hlVariable)
	if idx >= 0 {
		h.col[idx], h.col[idxEnd] = hlOperator, hlOperator
		h.arithBody(idx+1, idxEnd)
	}
	h.fill(i, i+op, hlOperator)
	p := i + op
	if shell && p < h.lim && src[p] == '(' {
		h.col[p] = hlOperator
		p++
		for p < h.lim {
			p = h.blanks(p)
			if p >= h.lim {
				break
			}
			switch src[p] {
			case '\n':
				p++
				continue
			case ')':
				h.col[p] = hlOperator
				return p + 1
			case '#':
				p = h.comment(p)
				continue
			}
			e := h.scan(p, wcNormal)
			if e == p {
				h.col[p] = hlError
				e = p + 1
			}
			h.classify(p, e, hlParam)
			p = e
		}
		return p
	}
	if p >= h.lim || hlIsMeta(src[p]) {
		return p
	}
	e := h.scan(p, wcAssign)
	h.classify(p, e, hlParam)
	return e
}

// casePattern handles one token of an sh case pattern list:
// [(] pattern [| pattern]... ) or esac.
func (h *highlighter) casePattern(pos int, c *hlCmd) int {
	src := h.src
	switch src[pos] {
	case '\n':
		return pos + 1
	case '#':
		return h.comment(pos)
	case '(', '|':
		h.col[pos] = hlOperator
		return pos + 1
	case ')':
		h.col[pos] = hlOperator
		if f := h.top(); f != nil && f.kind == frCase {
			f.phase = 1
		}
		c.begin(atStmt)
		return pos + 1
	case ';', '&', '<', '>':
		h.col[pos] = hlError
		return pos + 1
	}
	if c.patStart && h.wordAt(pos, "esac") {
		ok := false
		if f := h.top(); f != nil && f.kind == frCase {
			ok = true
		}
		return h.closer(ok, pos, pos+4, c)
	}
	c.patStart = false
	return h.plainWord(pos, hlParam)
}

// testClause highlights the inside of [[ ... ]].
func (h *highlighter) testClause(pos int, c *hlCmd) int {
	src := h.src
	depth := 0
	c.state, c.args = stArgs, false
	for pos < h.lim {
		pos = h.blanks(pos)
		if pos >= h.lim {
			break
		}
		if h.wordAt(pos, "]]") {
			h.fill(pos, pos+2, hlKeyword)
			c.closed()
			return pos + 2
		}
		ch := src[pos]
		switch ch {
		case '\n':
			pos++
			continue
		case ';':
			return pos // unterminated: the statement ends here
		case '(':
			depth++
			h.col[pos] = hlOperator
			pos++
			continue
		case ')':
			if depth == 0 {
				return pos
			}
			depth--
			h.col[pos] = hlOperator
			pos++
			continue
		case '&', '|', '<', '>':
			n := 1
			if pos+1 < h.lim && src[pos+1] == ch {
				n = 2
			}
			h.fill(pos, pos+n, hlOperator)
			pos += n
			continue
		}
		end := h.scan(pos, wcTest)
		if end == pos {
			h.col[pos] = hlError
			pos++
			continue
		}
		role := hlParam
		switch w := src[pos:end]; {
		case w == "!" || w == "=" || w == "==" || w == "!=" || w == "=~":
			role = hlOperator
		case w[0] == '-' && len(w) > 1:
			role = hlOption
		}
		h.classify(pos, end, role)
		if role == hlParam && !h.probe && h.w.static {
			if p, ok := h.value(false); ok && h.isPath(p, end == len(src), false) {
				h.markPath(pos, end)
			}
		}
		pos = end
	}
	return pos
}

// arithEnd finds the "))" that closes arithmetic starting at i. It
// returns the index of the first ')' (or of a lone final ')', or the end
// of the input), and false if the text can't be arithmetic.
func (h *highlighter) arithEnd(i int, forLoop bool) (int, bool) {
	if !h.lookAhead() {
		return -1, false
	}
	src := h.src
	start := i
	depth := 0
	for ; i < h.lim; i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
				continue
			}
			h.charge(i - start)
			if i+1 >= h.lim || src[i+1] == ')' {
				return i, true
			}
			return -1, false
		case '\n', ';':
			if depth == 0 && !forLoop {
				h.charge(i - start)
				return -1, false
			}
		}
	}
	h.charge(i - start)
	return h.lim, true
}

// arith highlights (( ... )) at pos, given where arithEnd found it ends.
func (h *highlighter) arith(pos, end int) int {
	h.fill(pos, pos+2, hlOperator)
	h.arithBody(pos+2, end)
	if end >= h.lim {
		return end
	}
	n := 1
	if end+1 < h.lim && h.src[end+1] == ')' {
		n = 2
	}
	h.fill(end, end+n, hlOperator)
	return end + n
}

// arithBody colors arithmetic text: names are variables, the rest plain
// parameters.
func (h *highlighter) arithBody(p, end int) {
	src := h.src
	saved := h.lim
	h.lim = end
	var f hlWord
	start := p
	for p < end {
		c := src[p]
		switch {
		case c == '$':
			p = h.dollar(p, true, &f)
			continue
		case c == '`' && !h.fish:
			p = h.backquote(p)
			continue
		case isNameStartByte(c):
			e := p + 1
			for e < end && isNameByte(src[e]) {
				e++
			}
			h.fill(p, e, hlVariable)
			p = e
			continue
		case c >= '0' && c <= '9':
			e := p + 1
			for e < end && isNameByte(src[e]) {
				e++
			}
			h.fill(p, e, hlParam)
			p = e
			continue
		case c == ';':
			h.col[p] = hlOperator
		case c == ' ' || c == '\t' || c == '\n':
		default:
			h.col[p] = hlParam
		}
		p++
	}
	h.lim = saved
	h.classify(start, end, hlParam) // plain text of a ${x:-...} inside
}

// ---------------------------------------------------------------------
// Redirections and heredocs

type hlRedir uint8

const (
	rIn hlRedir = iota + 1
	rOut
	rAppend
	rClobber
	rNoClobber
	rInOut
	rDupIn
	rDupOut
	rAll
	rAllAppend
	rHeredoc
	rHeredocTabs
	rHerestring
)

// redirect handles a redirection at pos, returning -1 if there is none.
func (h *highlighter) redirect(pos int, c *hlCmd) int {
	src := h.src
	i := pos
	for i < h.lim && src[i] >= '0' && src[i] <= '9' {
		i++
	}
	fd := i > pos
	if fd && (i >= h.lim || src[i] != '<' && src[i] != '>') {
		return -1
	}
	rest := src[i:h.lim]
	var op hlRedir
	n := 0
	switch {
	case strings.HasPrefix(rest, "&>>"):
		op, n = rAllAppend, 3
	case strings.HasPrefix(rest, "&>"):
		op, n = rAll, 2
	case strings.HasPrefix(rest, "<<<"):
		op, n = rHerestring, 3
	case strings.HasPrefix(rest, "<<-"):
		op, n = rHeredocTabs, 3
	case strings.HasPrefix(rest, "<<"):
		op, n = rHeredoc, 2
	case strings.HasPrefix(rest, "<&"):
		op, n = rDupIn, 2
	case strings.HasPrefix(rest, "<>"):
		op, n = rInOut, 2
	case strings.HasPrefix(rest, "<("), strings.HasPrefix(rest, ">("):
		return -1 // process substitution
	case strings.HasPrefix(rest, "<"):
		op, n = rIn, 1
	case strings.HasPrefix(rest, ">>"):
		op, n = rAppend, 2
	case strings.HasPrefix(rest, ">&"):
		op, n = rDupOut, 2
	case strings.HasPrefix(rest, ">|"):
		op, n = rClobber, 2
	case strings.HasPrefix(rest, ">?"):
		op, n = rNoClobber, 2
	case strings.HasPrefix(rest, ">"):
		op, n = rOut, 1
	default:
		return -1
	}
	if c.state == stCmd {
		// "> file" alone is a command; a command word may still follow,
		// but keywords are no longer recognized.
		c.needCmd, c.kwOK, c.at = false, false, atCmd
	}
	opEnd := i + n
	h.fill(pos, opEnd, hlRedirection)
	p := h.blanks(opEnd)
	if p >= h.lim {
		return p // the target is still to come
	}
	switch src[p] {
	case '\n', ';', '&', '|', ')', '<', '>', '#':
		h.fill(pos, opEnd, hlError) // no target
		return p
	}
	end := h.scan(p, wcNormal)
	if end == p {
		h.fill(pos, opEnd, hlError)
		return p
	}
	ok := true
	switch op {
	case rHeredoc, rHeredocTabs:
		delim := src[p:end]
		if h.w.static {
			delim = string(h.lit)
		}
		h.heredocs = append(h.heredocs, hlHeredoc{delim: delim, strip: op == rHeredocTabs, quoted: h.w.quoted})
	case rHerestring:
	default:
		if !h.probe {
			ok = h.redirOK(op, fd, end == len(src))
		}
	}
	switch {
	case !ok:
		h.fill(p, end, hlError)
	case h.w.subst:
		h.classify(p, end, hlRedirection) // keep the substituted command visible
	default:
		h.fill(p, end, hlRedirection)
	}
	return end
}

// redirOK checks a redirection target the way fish does: input must be a
// readable file, output must go somewhere writable, and fd duplication
// needs a number.
func (h *highlighter) redirOK(op hlRedir, fd, typing bool) bool {
	v, ok := h.value(false)
	if !ok {
		return true
	}
	switch op {
	case rDupIn, rDupOut:
		if v == "-" || hlIsFd(v) {
			return true
		}
		if op == rDupOut && !fd {
			return h.writable(v) // bash: >& file is &> file
		}
		return false
	case rIn:
		switch v {
		case "/dev/stdin", "/dev/stdout", "/dev/stderr":
			return true
		}
		abs := h.abs(v)
		if st, ok := h.stat(abs); ok {
			return !st.mode.IsDir() && h.access(abs, unix.R_OK)
		}
		return h.fsOff || typing && h.isPath(v, true, false)
	case rNoClobber:
		if _, ok := h.stat(h.abs(v)); ok {
			return false
		}
	}
	return h.writable(v)
}

func hlIsFd(s string) bool {
	s = strings.TrimSuffix(s, "-")
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// writable reports whether output can be redirected to path v.
func (h *highlighter) writable(v string) bool {
	if v == "" || strings.HasSuffix(v, "/") {
		return false
	}
	abs := h.abs(v)
	if st, ok := h.stat(abs); ok {
		return !st.mode.IsDir() && h.access(abs, unix.W_OK)
	}
	if h.fsOff {
		return true
	}
	dir := hlDirname(abs)
	st, ok := h.stat(dir)
	if !ok {
		return h.fsOff
	}
	return st.mode.IsDir() && h.access(dir, unix.W_OK)
}

// heredocBodies highlights the bodies of heredocs started on the line that
// just ended.
func (h *highlighter) heredocBodies(pos int) int {
	n := len(h.heredocs)
	if n == 0 {
		return pos
	}
	for i := 0; i < n && pos < h.lim; i++ {
		pos = h.heredocBody(pos, h.heredocs[i])
	}
	h.heredocs = append(h.heredocs[:0], h.heredocs[n:]...)
	return pos
}

func (h *highlighter) heredocBody(pos int, d hlHeredoc) int {
	src := h.src
	for pos < h.lim {
		lineEnd := h.lim
		if i := strings.IndexByte(src[pos:h.lim], '\n'); i >= 0 {
			lineEnd = pos + i
		}
		line := src[pos:lineEnd]
		if d.strip {
			line = strings.TrimLeft(line, "\t")
		}
		if line == d.delim {
			h.fill(lineEnd-len(line), lineEnd, hlRedirection)
			if lineEnd < h.lim {
				lineEnd++
			}
			return lineEnd
		}
		if d.quoted {
			h.fill(pos, lineEnd, hlQuote)
		} else {
			saved := h.lim
			h.lim = lineEnd
			var f hlWord
			h.quotedText(pos, 0, &f)
			h.lim = saved
			h.classify(pos, lineEnd, hlQuote)
		}
		pos = lineEnd
		if pos < h.lim {
			pos++
		}
	}
	return pos
}

// fishPipe recognizes fish's [N]>| pipe.
func (h *highlighter) fishPipe(pos int) int {
	i := pos
	for i < h.lim && h.src[i] >= '0' && h.src[i] <= '9' {
		i++
	}
	if strings.HasPrefix(h.src[i:h.lim], ">|") {
		return i + 2 - pos
	}
	return 0
}

// ---------------------------------------------------------------------
// Words

type hlWordCtx uint8

const (
	wcNormal   hlWordCtx = iota
	wcAssign             // assignment value: ~ after ':' too
	wcBraceAlt           // inside {a,b}: ends at , or }
	wcIndex              // inside $x[...]: ends at ] or a blank
	wcParamArg           // inside ${x:-...}: ends at }
	wcParamPat           // inside ${x/.../...}: ends at } or /
	wcTest               // inside [[ ]]: ( is not a substitution
)

// hlWord describes the word just scanned. Its value is in h.lit when the
// word is static or evaluable.
type hlWord struct {
	static    bool // no expansions, substitutions or globs
	evaluable bool // at most simple variables, whose values are in h.lit
	multi     bool // a variable has several values
	quoted    bool // quotes or escapes were used
	subst     bool // has a command or process substitution
	tilde     bool // starts with ~ or ~user, which is h.lit[:tildeEnd]
	tildeEnd  int
}

// markSubst records a command or process substitution in the word.
func (f *hlWord) markSubst() { f.static, f.evaluable, f.subst = false, false, true }

func hlIsMeta(c byte) bool {
	switch c {
	case ' ', '\t', '\n', ';', '&', '|', '<', '>', '(', ')':
		return true
	}
	return false
}

// scan scans a word, leaving its value in h.lit.
func (h *highlighter) scan(pos int, ctx hlWordCtx) int {
	h.lit = h.lit[:0]
	return h.word(pos, ctx)
}

// plainWord scans a word whose plain text has the given role.
func (h *highlighter) plainWord(pos int, role hlRole) int {
	end := h.scan(pos, wcNormal)
	h.classify(pos, end, role)
	return end
}

// wordAt reports whether the word at pos is exactly kw.
func (h *highlighter) wordAt(pos int, kw string) bool {
	end := pos + len(kw)
	return end <= h.lim && h.src[pos:end] == kw && (end == h.lim || hlIsMeta(h.src[end]))
}

// word scans one word, coloring quotes, escapes, expansions and globs and
// marking plain text hlPlain. It sets h.w and returns the end.
func (h *highlighter) word(pos int, ctx hlWordCtx) int {
	src := h.src
	start := pos
	f := hlWord{static: true, evaluable: true}
	if h.tooDeep(pos) {
		h.w = hlWord{}
		return h.lim
	}
scan:
	for pos < h.lim {
		c := src[pos]
		switch ctx {
		case wcBraceAlt:
			if c == ',' || c == '}' {
				break scan
			}
		case wcIndex:
			if c == ']' || c == ' ' || c == '\t' {
				break scan
			}
		case wcParamArg:
			if c == '}' {
				break scan
			}
		case wcParamPat:
			if c == '}' || c == '/' {
				break scan
			}
		case wcTest:
			if c == ']' && pos+1 < h.lim && src[pos+1] == ']' && (pos+2 >= h.lim || hlIsMeta(src[pos+2])) {
				break scan
			}
		}
		if ctx != wcParamArg && ctx != wcParamPat {
			if ctx != wcTest {
				if (c == '<' || c == '>') && pos == start && pos+1 < h.lim && src[pos+1] == '(' {
					h.fill(pos, pos+2, hlOperator)
					pos = h.substBody(pos + 2)
					f.markSubst()
					continue
				}
				if c == '(' {
					h.col[pos] = hlOperator
					pos = h.substBody(pos + 1)
					if pos < h.lim && src[pos] == '[' {
						pos = h.fishIndex(pos)
					}
					f.markSubst()
					continue
				}
			}
			if hlIsMeta(c) {
				break scan
			}
		}
		switch c {
		case '\\':
			pos = h.escape(pos, &f)
		case '\'':
			f.quoted = true
			pos = h.singleQuote(pos)
		case '"':
			f.quoted = true
			pos = h.doubleQuote(pos, &f)
		case '`':
			if h.fish {
				h.plainByte(pos)
				pos++
				continue
			}
			f.markSubst()
			pos = h.backquote(pos)
		case '$':
			pos = h.dollar(pos, false, &f)
		case '{':
			if ctx != wcParamArg && ctx != wcParamPat {
				if e := h.braceExp(pos); e > pos {
					f.static, f.evaluable = false, false
					pos = e
					continue
				}
			}
			h.plainByte(pos)
			pos++
		case '~':
			if ctx != wcParamPat && (pos == start || ctx == wcAssign && src[pos-1] == ':') {
				if e := h.tilde(pos, ctx); e > pos {
					if pos == start {
						f.tilde, f.tildeEnd = true, len(h.lit)
					} else {
						f.static, f.evaluable = false, false
					}
					pos = e
					continue
				}
			}
			h.plainByte(pos)
			pos++
		case '*', '?':
			h.col[pos] = hlOperator
			h.lit = append(h.lit, c)
			f.static, f.evaluable = false, false
			pos++
		case '[':
			if e := h.globClass(pos); e > pos {
				f.static, f.evaluable = false, false
				pos = e
				continue
			}
			h.plainByte(pos)
			pos++
		default:
			h.plainByte(pos)
			pos++
		}
	}
	h.nest--
	h.w = f
	return pos
}

// markPlain marks the byte at i as plain text of the current word.
func (h *highlighter) markPlain(i int) {
	h.col[i] = hlPlain
	h.plainAt = append(h.plainAt, int32(i))
}

// plainByte marks the byte at i as plain text and adds it to the value.
func (h *highlighter) plainByte(i int) {
	h.markPlain(i)
	h.lit = append(h.lit, h.src[i])
}

// escape handles a backslash outside quotes.
func (h *highlighter) escape(pos int, f *hlWord) int {
	src := h.src
	f.quoted = true
	h.col[pos] = hlEscape
	if pos+1 >= h.lim {
		return pos + 1 // a line continuation still being typed
	}
	if src[pos+1] == '\n' {
		return pos + 2 // line continuation: the word goes on
	}
	val, n := hlUnescape(src[pos:h.lim])
	h.fill(pos, pos+n, hlEscape)
	h.lit = append(h.lit, val...)
	return pos + n
}

// hlUnescape decodes a backslash escape outside quotes the way the parser
// does (fish-style C escapes; anything else stands for itself).
func hlUnescape(s string) (string, int) {
	switch s[1] {
	case 'n':
		return "\n", 2
	case 't':
		return "\t", 2
	case 'r':
		return "\r", 2
	case 'e':
		return "\x1b", 2
	case 'x', 'X':
		v, n := hlHex(s[2:], 2)
		if n == 0 {
			return s[1:2], 2
		}
		return string([]byte{byte(v)}), 2 + n
	case 'u', 'U':
		max := 4
		if s[1] == 'U' {
			max = 8
		}
		v, n := hlHex(s[2:], max)
		if n == 0 {
			return s[1:2], 2
		}
		return string(rune(v)), 2 + n
	case 'c':
		if len(s) > 2 && s[2] >= '@' && s[2] <= '~' {
			return string([]byte{s[2] & 0x1f}), 3
		}
		if len(s) > 2 && s[2] == '?' {
			return "\x7f", 3
		}
		return "c", 2
	}
	_, size := utf8.DecodeRuneInString(s[1:])
	return s[1 : 1+size], 1 + size
}

func hlHex(s string, max int) (int, int) {
	v, n := 0, 0
	for n < len(s) && n < max {
		c := s[n]
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return v, n
		}
		v = v*16 + d
		n++
	}
	return v, n
}

// singleQuote handles '...' (fish: \' and \\ are escapes inside).
func (h *highlighter) singleQuote(pos int) int {
	src := h.src
	start := pos
	h.col[pos] = hlQuote
	pos++
	for pos < h.lim {
		c := src[pos]
		if c == '\'' {
			h.col[pos] = hlQuote
			return pos + 1
		}
		if h.fish && c == '\\' && pos+1 < h.lim && (src[pos+1] == '\'' || src[pos+1] == '\\') {
			h.col[pos], h.col[pos+1] = hlEscape, hlEscape
			h.lit = append(h.lit, src[pos+1])
			pos += 2
			continue
		}
		h.col[pos] = hlQuote
		h.lit = append(h.lit, c)
		pos++
	}
	h.col[start] = hlError // unterminated, like fish shows it
	return pos
}

// ansiQuote handles $'...'.
func (h *highlighter) ansiQuote(pos int) int {
	src := h.src
	start := pos
	h.fill(pos, pos+2, hlQuote)
	pos += 2
	for pos < h.lim {
		c := src[pos]
		if c == '\'' {
			h.col[pos] = hlQuote
			return pos + 1
		}
		if c == '\\' && pos+1 < h.lim {
			n := 2
			switch src[pos+1] {
			case 'x':
				_, k := hlHex(src[pos+2:h.lim], 2)
				n += k
			case 'u':
				_, k := hlHex(src[pos+2:h.lim], 4)
				n += k
			case 'U':
				_, k := hlHex(src[pos+2:h.lim], 8)
				n += k
			case 'c':
				if pos+2 < h.lim {
					n++
				}
			case '0', '1', '2', '3', '4', '5', '6', '7':
				for k := 0; k < 2 && pos+n < h.lim && src[pos+n] >= '0' && src[pos+n] <= '7'; k++ {
					n++
				}
			}
			h.fill(pos, pos+n, hlEscape)
			pos += n
			continue
		}
		h.col[pos] = hlQuote
		pos++
	}
	h.col[start] = hlError
	return pos
}

// doubleQuote handles "...".
func (h *highlighter) doubleQuote(pos int, f *hlWord) int {
	start := pos
	h.col[pos] = hlQuote
	pos = h.quotedText(pos+1, '"', f)
	if pos < h.lim && h.src[pos] == '"' {
		h.col[pos] = hlQuote
		return pos + 1
	}
	h.col[start] = hlError // unterminated
	return pos
}

// quotedText colors double-quoted text (or a heredoc body line) up to the
// closing quote, which it leaves for the caller.
func (h *highlighter) quotedText(pos int, quote byte, f *hlWord) int {
	src := h.src
	for pos < h.lim {
		c := src[pos]
		switch {
		case c == quote:
			return pos
		case c == '\\':
			if pos+1 < h.lim {
				n := src[pos+1]
				if n == '$' || n == '`' || n == '\\' || n == '\n' || n == '"' && quote == '"' {
					h.col[pos], h.col[pos+1] = hlEscape, hlEscape
					if n != '\n' {
						h.lit = append(h.lit, n)
					}
					pos += 2
					continue
				}
			}
			h.col[pos] = hlQuote
			h.lit = append(h.lit, c)
			pos++
		case c == '$':
			pos = h.dollar(pos, true, f)
		case c == '`' && !h.fish:
			f.markSubst()
			pos = h.backquote(pos)
		default:
			h.col[pos] = hlQuote
			h.lit = append(h.lit, c)
			pos++
		}
	}
	return pos
}

// dollar handles an expansion starting with '$'.
func (h *highlighter) dollar(pos int, inDQ bool, f *hlWord) int {
	if h.tooDeep(pos) {
		f.static, f.evaluable = false, false
		return h.lim
	}
	end := h.expansion(pos, inDQ, f)
	h.nest--
	return end
}

func (h *highlighter) expansion(pos int, inDQ bool, f *hlWord) int {
	src := h.src
	literal := func() int {
		if inDQ {
			h.col[pos] = hlQuote
			h.lit = append(h.lit, '$')
		} else {
			h.plainByte(pos)
		}
		return pos + 1
	}
	if pos+1 >= h.lim {
		if pos+1 == len(src) {
			// The user is about to type a name.
			f.static, f.evaluable = false, false
			h.col[pos] = hlVariable
			return pos + 1
		}
		return literal()
	}
	n := src[pos+1]
	switch {
	case n == '(':
		f.static, f.evaluable = false, false
		if pos+2 < h.lim && src[pos+2] == '(' {
			if end, ok := h.arithEnd(pos+3, false); ok {
				h.col[pos] = hlOperator
				return h.arith(pos+1, end)
			}
		}
		f.markSubst()
		h.fill(pos, pos+2, hlOperator)
		return h.substBody(pos + 2)
	case n == '{':
		return h.bracedParam(pos, inDQ, f)
	case n == '\'' && !inDQ:
		f.static, f.evaluable = false, false
		return h.ansiQuote(pos)
	case n == '"' && !inDQ:
		f.quoted = true
		h.col[pos] = hlQuote
		return h.doubleQuote(pos+1, f)
	case isNameStartByte(n):
		end := pos + 2
		for end < h.lim && isNameByte(src[end]) {
			end++
		}
		h.fill(pos, end, hlVariable)
		f.static = false
		if end < h.lim && src[end] == '[' {
			if e := h.fishIndex(end); e > end {
				f.evaluable = false
				return e
			}
		}
		if f.evaluable {
			h.appendVar(src[pos+1:end], inDQ, f)
		}
		return end
	case strings.IndexByte("?$!#@*-0123456789", n) >= 0:
		h.fill(pos, pos+2, hlVariable)
		f.static, f.evaluable = false, false
		return pos + 2
	}
	return literal()
}

// appendVar adds a variable's value to h.lit for evaluating a word.
// Unset and special variables ($status, $history...) make the word
// unknown: computing them may be costly or change state.
func (h *highlighter) appendVar(name string, inDQ bool, f *hlWord) {
	v, _ := h.sh.lookupVar(name)
	if v == nil {
		f.evaluable = false
		return
	}
	if inDQ {
		sep := " "
		if v.Path {
			sep = ":"
		}
		for i, x := range v.Vals {
			if i > 0 {
				h.lit = append(h.lit, sep...)
			}
			h.lit = append(h.lit, x...)
		}
		return
	}
	switch len(v.Vals) {
	case 0:
		f.evaluable = false // the whole word expands to nothing
	case 1:
		h.lit = append(h.lit, v.Vals[0]...)
	default:
		f.multi = true
		h.lit = append(h.lit, v.Vals[0]...)
	}
}

// bracedParam handles ${...}.
func (h *highlighter) bracedParam(pos int, inDQ bool, f *hlWord) int {
	src := h.src
	h.fill(pos, pos+2, hlVariable)
	p := pos + 2
	simple := true
	if p+1 < h.lim && (src[p] == '#' || src[p] == '!') && src[p+1] != '}' {
		h.col[p] = hlOperator // ${#name} length, ${!name} indirection
		p++
		simple = false
	}
	nameStart := p
	switch {
	case p < h.lim && isNameStartByte(src[p]):
		for p < h.lim && isNameByte(src[p]) {
			p++
		}
	case p < h.lim && src[p] >= '0' && src[p] <= '9':
		for p < h.lim && src[p] >= '0' && src[p] <= '9' {
			p++
		}
		simple = false
	case p < h.lim && strings.IndexByte("?$!#@*-", src[p]) >= 0:
		p++
		simple = false
	}
	h.fill(nameStart, p, hlVariable)
	name := src[nameStart:p]
	if p < h.lim && src[p] == '[' {
		simple = false
		e := h.scanTo(p+1, "]}\n")
		h.col[p] = hlOperator
		h.arithBody(p+1, e)
		p = e
		if p < h.lim && src[p] == ']' {
			h.col[p] = hlOperator
			p++
		}
	}
	if p < h.lim && src[p] != '}' {
		simple = false
		p = h.paramOp(p)
	}
	if p < h.lim && src[p] == '}' {
		h.col[p] = hlVariable
		p++
	}
	f.static = false
	if simple && name != "" && f.evaluable {
		h.appendVar(name, inDQ, f)
	} else {
		f.evaluable = false
	}
	return p
}

// paramOp colors the operator and argument of ${name...}, up to '}'.
func (h *highlighter) paramOp(p int) int {
	src := h.src
	c := src[p]
	n := 1
	ctx := wcParamArg
	switch c {
	case ':':
		if p+1 < h.lim && strings.IndexByte("-=?+", src[p+1]) >= 0 {
			n = 2
		} else {
			// ${x:offset:length}
			h.col[p] = hlOperator
			e := h.scanTo(p+1, "}\n")
			h.arithBody(p+1, e)
			for i := p + 1; i < e; i++ {
				if src[i] == ':' {
					h.col[i] = hlOperator
				}
			}
			return e
		}
	case '-', '=', '?', '+':
	case '#', '%', '^', ',':
		if p+1 < h.lim && src[p+1] == c {
			n = 2
		}
	case '/':
		if p+1 < h.lim && strings.IndexByte("/#%", src[p+1]) >= 0 {
			n = 2
		}
		ctx = wcParamPat
	case '@':
		if p+1 < h.lim && src[p+1] != '}' {
			n = 2
		}
		h.fill(p, p+n, hlOperator)
		return p + n
	default:
		e := p
		for e < h.lim && src[e] != '}' && src[e] != '\n' {
			e++
		}
		h.fill(p, e, hlError) // bad substitution
		return e
	}
	h.fill(p, p+n, hlOperator)
	p = h.word(p+n, ctx)
	if ctx == wcParamPat && p < h.lim && src[p] == '/' {
		h.col[p] = hlOperator
		p = h.word(p+1, wcParamArg)
	}
	return p
}

// substBody highlights a command list after an opening paren, and the
// closing paren. It returns the position after it.
func (h *highlighter) substBody(pos int) int {
	mark := len(h.stack)
	h.push(frSubst)
	h.depth++
	pos = h.list(pos, ')')
	h.depth--
	h.stack = h.stack[:mark]
	if pos < h.lim && h.src[pos] == ')' {
		h.col[pos] = hlOperator
		pos++
	}
	return pos
}

// backquote highlights `...`.
func (h *highlighter) backquote(pos int) int {
	src := h.src
	end := pos + 1
	for end < h.lim && src[end] != '`' {
		if src[end] == '\\' && end+1 < h.lim {
			end++
		}
		end++
	}
	if end > h.lim {
		end = h.lim
	}
	h.col[pos] = hlOperator
	saved := h.lim
	h.lim = end
	mark := len(h.stack)
	h.push(frSubst)
	h.depth++
	h.list(pos+1, 0)
	h.depth--
	h.stack = h.stack[:mark]
	h.lim = saved
	if end < h.lim {
		h.col[end] = hlOperator
		return end + 1
	}
	return end
}

// fishIndex highlights a fish index like [1], [2..-1] or [$i] at pos and
// returns the position after it, or pos if there is none.
func (h *highlighter) fishIndex(pos int) int {
	src := h.src
	if !h.lookAhead() {
		return pos
	}
	end, stop := hlMatchBracket(src[:h.lim], pos)
	h.charge(stop - pos)
	if end < 0 || !hlValidIndex(src[pos+1:end]) {
		return pos
	}
	h.col[pos], h.col[end] = hlOperator, hlOperator
	saved := h.lim
	h.lim = end
	p := pos + 1
	for p < end {
		if src[p] == ' ' || src[p] == '\t' {
			p++
			continue
		}
		e := h.word(p, wcIndex)
		if e == p {
			h.col[p] = hlError
			e = p + 1
		}
		h.classify(p, e, hlParam)
		p = e
	}
	h.lim = saved
	return end + 1
}

// braceExp highlights a brace expansion at pos and returns the position
// after it, or pos if the brace is just a brace.
func (h *highlighter) braceExp(pos int) int {
	src := h.src
	if !h.lookAhead() {
		return pos
	}
	end, comma, open, stop := hlScanBrace(src[:h.lim], pos)
	h.charge(stop - pos)
	if end < 0 {
		if !(open && comma && h.lim == len(src)) {
			return pos
		}
		end = h.lim // still being typed
	} else if !comma {
		if !hlBraceSeq(src[pos+1 : end]) {
			return pos
		}
		h.col[pos], h.col[end] = hlOperator, hlOperator
		for i := pos + 1; i < end; i++ {
			if src[i] == '.' && src[i+1] == '.' {
				h.col[i], h.col[i+1] = hlOperator, hlOperator
				i++
			} else {
				h.markPlain(i)
			}
		}
		return end + 1
	}
	h.col[pos] = hlOperator
	p := pos + 1
	for p < h.lim {
		e := h.word(p, wcBraceAlt)
		if e >= h.lim {
			return e
		}
		switch src[e] {
		case ',':
			h.col[e] = hlOperator
			p = e + 1
			continue
		case '}':
			h.col[e] = hlOperator
			return e + 1
		}
		if e == p {
			h.col[e] = hlError
			e++
		}
		p = e
	}
	return p
}

// tilde handles ~ or ~user at pos, returning pos if it is just a '~'.
func (h *highlighter) tilde(pos int, ctx hlWordCtx) int {
	src := h.src
	i := pos + 1
	for i < h.lim && (isNameByte(src[i]) || src[i] == '.' || src[i] == '-' || src[i] == '+') {
		i++
	}
	if i < h.lim {
		c := src[i]
		if !(c == '/' || hlIsMeta(c) || ctx == wcAssign && c == ':' || ctx == wcBraceAlt && (c == ',' || c == '}')) {
			return pos
		}
	}
	user := src[pos+1 : i]
	if user != "+" && user != "-" && strings.IndexByte(user, '+') >= 0 {
		return pos
	}
	h.col[pos] = hlOperator
	for j := pos + 1; j < i; j++ {
		h.markPlain(j)
	}
	h.lit = append(h.lit, src[pos:i]...)
	return i
}

// globClass highlights a [...] glob at pos, or returns pos.
func (h *highlighter) globClass(pos int) int {
	if pos < h.classFail {
		// A scan from an earlier '[' stopped beyond pos without finding a
		// ']', so this one won't find one either.
		return pos
	}
	src := h.src
	i := pos + 1
	if i < h.lim && (src[i] == '!' || src[i] == '^') {
		i++
	}
	if i < h.lim && src[i] == ']' {
		i++
	}
	for ; i < h.lim; i++ {
		c := src[i]
		if c == ']' {
			h.col[pos], h.col[i] = hlOperator, hlOperator
			for j := pos + 1; j < i; j++ {
				h.markPlain(j)
			}
			h.lit = append(h.lit, src[pos:i+1]...)
			return i + 1
		}
		if hlIsMeta(c) || c == '\'' || c == '"' || c == '\\' || c == '$' || c == '`' {
			break
		}
	}
	h.classFail = i
	return pos
}

// value returns the word just scanned with ~ and simple variables
// expanded, and whether that could be done without running anything.
// With first set, only the first field of a split variable is kept.
func (h *highlighter) value(first bool) (string, bool) {
	w := &h.w
	if !w.static && !w.evaluable || w.multi && !first {
		return "", false
	}
	s := h.lit
	if w.tilde {
		if w.tildeEnd != 1 {
			return "", false // ~user: don't look up accounts
		}
		return h.sh.HomeDir() + string(s[1:]), true
	}
	return string(s), true
}

// ---------------------------------------------------------------------
// Ported scanners from the parser (unexported there)

// hlMatchBracket returns the index of the ']' matching the '[' at i, or -1,
// and where the scan stopped.
func hlMatchBracket(s string, i int) (end, stop int) {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return j, j
			}
		case '(':
			k := hlMatchParen(s, j)
			if k < 0 {
				return -1, len(s)
			}
			j = k
		case '\n', ';', '|', '&':
			return -1, j
		}
	}
	return -1, len(s)
}

// hlMatchParen returns the index of the ')' matching the '(' at i, or -1
// after scanning to the end of s.
func hlMatchParen(s string, i int) int {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j
			}
		case '\'':
			k := strings.IndexByte(s[j+1:], '\'')
			if k < 0 {
				return -1
			}
			j += k + 1
		case '"':
			k := j + 1
			for k < len(s) && s[k] != '"' {
				if s[k] == '\\' {
					k++
				}
				k++
			}
			j = k
		}
	}
	return -1
}

// hlValidIndex accepts fish index lists like "1", "-1", "2..3", "..-2",
// "$i", "(math $i + 1)..", "1 3 5".
func hlValidIndex(s string) bool {
	depth, start, n := 0, -1, 0
	for i := 0; i <= len(s); i++ {
		c := byte(' ')
		if i < len(s) {
			c = s[i]
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		}
		if (c == ' ' || c == '\t') && depth == 0 {
			if start >= 0 {
				if !hlValidIndexToken(s[start:i]) {
					return false
				}
				n++
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	return n > 0
}

func hlValidIndexToken(t string) bool {
	if i := strings.Index(t, ".."); i >= 0 && !strings.Contains(t[:i], "(") {
		a, b := t[:i], t[i+2:]
		return (a == "" || hlValidIndexTerm(a)) && (b == "" || hlValidIndexTerm(b))
	} else if i >= 0 {
		k := hlMatchParen(t, strings.IndexByte(t, '('))
		if k >= 0 && strings.HasPrefix(t[k+1:], "..") {
			a, b := t[:k+1], t[k+3:]
			return hlValidIndexTerm(a) && (b == "" || hlValidIndexTerm(b))
		}
	}
	return hlValidIndexTerm(t)
}

func hlValidIndexTerm(e string) bool {
	if e == "" {
		return false
	}
	if e[0] == '(' {
		return hlMatchParen(e, 0) == len(e)-1
	}
	if e[0] == '$' {
		if len(e) < 2 || !isNameStartByte(e[1]) {
			return false
		}
		i := 2
		for i < len(e) && isNameByte(e[i]) {
			i++
		}
		return i == len(e) || e[i] == '['
	}
	if e[0] == '-' {
		e = e[1:]
	}
	if e == "" {
		return false
	}
	for i := 0; i < len(e); i++ {
		if e[i] < '0' || e[i] > '9' {
			return false
		}
	}
	return true
}

// hlScanBrace finds the '}' matching the '{' at i and reports whether the
// group has a top-level comma. open reports that the input ended inside
// the group; stop is where the scan stopped.
func hlScanBrace(s string, i int) (end int, comma, open bool, stop int) {
	depth := 0
	for j := i; j < len(s); j++ {
		switch c := s[j]; c {
		case '\\':
			j++
		case '\'':
			k := strings.IndexByte(s[j+1:], '\'')
			if k < 0 {
				return -1, comma, true, len(s)
			}
			j += k + 1
		case '"':
			k := j + 1
			for k < len(s) && s[k] != '"' {
				if s[k] == '\\' {
					k++
				}
				k++
			}
			if k >= len(s) {
				return -1, comma, true, len(s)
			}
			j = k
		case '$':
			if j+1 < len(s) && (s[j+1] == '(' || s[j+1] == '{') {
				var k int
				if s[j+1] == '(' {
					k = hlMatchParen(s, j+1)
				} else if k = strings.IndexByte(s[j+1:], '}'); k >= 0 {
					k += j + 1
				}
				if k < 0 {
					return -1, comma, true, len(s)
				}
				j = k
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j, comma, false, j
			}
		case ',':
			if depth == 1 {
				comma = true
			}
		case ' ', '\t', '\n', ';', '&', '|', '<', '>', ')':
			return -1, comma, false, j
		case '(':
			k := hlMatchParen(s, j)
			if k < 0 {
				return -1, comma, true, len(s)
			}
			j = k
		}
	}
	return -1, comma, true, len(s)
}

// hlBraceSeq reports whether s is a brace sequence like 1..10 or a..z.
func hlBraceSeq(s string) bool {
	parts := strings.Split(s, "..")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	isInt := func(x string) bool {
		if x != "" && (x[0] == '-' || x[0] == '+') {
			x = x[1:]
		}
		if x == "" {
			return false
		}
		for i := 0; i < len(x); i++ {
			if x[i] < '0' || x[i] > '9' {
				return false
			}
		}
		return true
	}
	isChar := func(x string) bool { return len(x) == 1 && (x[0] >= 'a' && x[0] <= 'z' || x[0] >= 'A' && x[0] <= 'Z') }
	a, b := parts[0], parts[1]
	if !(isInt(a) && isInt(b)) && !(isChar(a) && isChar(b)) {
		return false
	}
	return len(parts) == 2 || isInt(parts[2])
}

// ---------------------------------------------------------------------
// Coloring helpers

func (h *highlighter) fill(from, to int, r hlRole) {
	if to > len(h.col) {
		to = len(h.col)
	}
	for i := from; i < to; i++ {
		h.col[i] = r
	}
	p := h.plainAt
	for len(p) > 0 && int(p[len(p)-1]) >= from {
		p = p[:len(p)-1]
	}
	h.plainAt = p
}

// classify gives the plain text of the word just scanned, which starts at
// from, its role. Only that word's own plain bytes are visited, not text
// inside its substitutions, which keeps nesting linear.
func (h *highlighter) classify(from, to int, r hlRole) {
	p := h.plainAt
	for len(p) > 0 && int(p[len(p)-1]) >= from {
		i := int(p[len(p)-1])
		p = p[:len(p)-1]
		if i < to && h.col[i] == hlPlain {
			h.col[i] = r
		}
	}
	h.plainAt = p
}

// errorWord marks the word just scanned as an error.
func (h *highlighter) errorWord(from, to int) {
	if h.w.subst {
		h.classify(from, to, hlError) // spare the substituted commands
	} else {
		h.fill(from, to, hlError)
	}
}

// hlMaxNest bounds the nesting of words and expansions, like the parser's
// own limit.
const hlMaxNest = 1000

// tooDeep enters a nesting level. Past the limit it marks the rest of the
// input as an error and reports true; otherwise the caller must decrement
// h.nest when done.
func (h *highlighter) tooDeep(pos int) bool {
	if h.nest >= hlMaxNest {
		h.fill(pos, h.lim, hlError)
		return true
	}
	h.nest++
	return false
}

// lookAhead reports whether a scan for a closing bracket may run, and
// charge records how far one went.
func (h *highlighter) lookAhead() bool { return h.ahead > 0 }
func (h *highlighter) charge(n int)    { h.ahead -= n }

// scanTo returns the index of the first of the stop bytes at or after i,
// or the limit; with the look-ahead budget spent, just the limit.
func (h *highlighter) scanTo(i int, stops string) int {
	if !h.lookAhead() {
		return h.lim
	}
	e := i
	for e < h.lim && strings.IndexByte(stops, h.src[e]) < 0 {
		e++
	}
	h.charge(e - i)
	return e
}

func (h *highlighter) markPath(from, to int) {
	for i := from; i < to; i++ {
		h.col[i] |= hlValidPath
	}
}

func (h *highlighter) comment(pos int) int {
	end := h.lim
	if i := strings.IndexByte(h.src[pos:h.lim], '\n'); i >= 0 {
		end = pos + i
	}
	h.fill(pos, end, hlComment)
	return end
}

// blanks skips spaces, tabs and line continuations.
func (h *highlighter) blanks(pos int) int {
	src := h.src
	for pos < h.lim {
		switch src[pos] {
		case ' ', '\t', '\r':
			pos++
		case '\\':
			if pos+1 < h.lim && src[pos+1] == '\n' {
				h.col[pos] = hlEscape
				pos += 2
				continue
			}
			return pos
		default:
			return pos
		}
	}
	return pos
}

// ---------------------------------------------------------------------
// Rendering

func (h *highlighter) render() []string {
	src := h.src
	out := make([]string, utf8.RuneCountInString(src))
	k := 0
	for i := 0; i < len(src); k++ {
		out[k] = h.style(h.col[i])
		if src[i] < utf8.RuneSelf {
			i++
			continue
		}
		_, n := utf8.DecodeRuneInString(src[i:])
		i += n
	}
	return out
}

// style returns the SGR sequence for a role, computing each once per call.
func (h *highlighter) style(r hlRole) string {
	base := r & hlRoleMask
	if base == hlPlain {
		base = hlParam
	}
	if base >= hlNumRoles {
		base = hlNormal
	}
	vp := r&hlValidPath != 0
	i := int(base)
	if vp {
		i += int(hlNumRoles)
	}
	if h.sgrOK[i] {
		return h.sgr[i]
	}
	s := ""
	if base != hlNormal || vp {
		st := h.sh.Style(h.roleName(base))
		if vp {
			st = hlMergePath(st, h.sh.Style("valid_path"))
		}
		s = st.SGR()
	}
	h.sgr[i], h.sgrOK[i] = s, true
	return s
}

// roleName maps a role to its style name. Like fish, keywords use the
// command color and variables the operator color when only those were
// customized.
func (h *highlighter) roleName(r hlRole) string {
	switch r {
	case hlKeyword:
		if !h.styleSet("ash_color_keyword", "fish_color_keyword") && h.styleSet("ash_color_command", "fish_color_command") {
			return "command"
		}
	case hlVariable:
		if !h.styleSet("ash_color_variable", "fish_color_variable") && h.styleSet("ash_color_operator", "fish_color_operator") {
			return "operator"
		}
	}
	return hlRoleNames[r]
}

func (h *highlighter) styleSet(names ...string) bool {
	for _, n := range names {
		if v, ok := h.sh.getVar(n); ok && len(v) > 0 {
			return true
		}
	}
	return false
}

// hlMergePath applies the valid_path style on top of another, the way
// fish does: its foreground (if it has one) and attributes, not its
// background.
func hlMergePath(st, vp Style) Style {
	if vp.FG.Kind == 1 || vp.FG.Kind == 2 {
		st.FG = vp.FG
	}
	st.Bold = st.Bold || vp.Bold
	st.Dim = st.Dim || vp.Dim
	st.Italic = st.Italic || vp.Italic
	st.Underline = st.Underline || vp.Underline
	st.Reverse = st.Reverse || vp.Reverse
	return st
}

// ---------------------------------------------------------------------
// File system and command checks

type hlStat struct {
	path string
	mode os.FileMode
	mod  int64
	ok   bool
}

type hlCmdCheck struct {
	name string
	kind hlKind
	ok   bool
}

// slow reports whether the call is over its time budget.
func (h *highlighter) slow() bool {
	if !h.fsOff && time.Since(h.start) > hlBudget {
		h.fsOff = true
	}
	return h.fsOff
}

// stat is a cached os.Stat. It fails without looking when over budget,
// and callers then check h.fsOff.
func (h *highlighter) stat(path string) (hlStat, bool) {
	for i := range h.stats {
		if h.stats[i].path == path {
			return h.stats[i], h.stats[i].ok
		}
	}
	if h.slow() {
		return hlStat{}, false
	}
	st := hlStat{path: path}
	if fi, err := os.Stat(path); err == nil {
		st.mode, st.mod, st.ok = fi.Mode(), fi.ModTime().UnixNano(), true
	}
	if len(h.stats) < hlCacheMax {
		h.stats = append(h.stats, st)
	}
	return st, st.ok
}

// hlCacheMax bounds the per-call caches, which are searched linearly.
const hlCacheMax = 64

func (h *highlighter) access(path string, mode uint32) bool {
	if h.slow() {
		return true
	}
	return unix.Access(path, mode) == nil
}

// abs makes p absolute against the shell's directory without cleaning it
// (a trailing slash matters).
func (h *highlighter) abs(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	d := h.sh.dir
	if strings.HasSuffix(d, "/") {
		return d + p
	}
	return d + "/" + p
}

func hlDirname(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

// commandOK reports whether name can be run in a position that accepts
// commands of the given kind.
func (h *highlighter) commandOK(name string, kind hlKind) bool {
	for _, e := range h.cmds {
		if e.kind == kind && e.name == name {
			return e.ok
		}
	}
	ok := h.lookupCommand(name, kind)
	if len(h.cmds) < hlCacheMax {
		h.cmds = append(h.cmds, hlCmdCheck{name: name, kind: kind, ok: ok})
	}
	return ok
}

func (h *highlighter) lookupCommand(name string, kind hlKind) bool {
	sh := h.sh
	if name == "" {
		return false
	}
	switch kind {
	case kindBuiltin:
		_, ok := builtins[name]
		return ok
	case kindExternal:
		return h.program(name)
	}
	if _, ok := sh.aliases[name]; ok {
		return true
	}
	if _, ok := sh.funcs[name]; ok {
		return true
	}
	if _, ok := builtins[name]; ok {
		return true
	}
	if !h.probe && hlAbbr(sh, name) {
		return true
	}
	if strings.IndexByte(name, '/') >= 0 || name == ".." || name == "..." {
		// a program, or a directory to cd into
		p := name
		if p == "..." {
			p = "../.."
		}
		st, ok := h.stat(h.abs(p))
		if !ok {
			return h.fsOff
		}
		return st.mode.IsDir() || st.mode.IsRegular() && st.mode&0o111 != 0
	}
	if h.program(name) {
		return true
	}
	if h.slow() {
		return true
	}
	return sh.shared.autoload.has(sh, name)
}

// program reports whether name is an executable file in $PATH, or a path
// to one.
func (h *highlighter) program(name string) bool {
	if strings.IndexByte(name, '/') >= 0 {
		st, ok := h.stat(h.abs(name))
		if !ok {
			return h.fsOff
		}
		return st.mode.IsRegular() && st.mode&0o111 != 0
	}
	if h.slow() {
		return true
	}
	return h.inPath(name)
}

// inPath looks name up in $PATH the way the executor does. It shares the
// executor's cache of programs found but never records a failure there:
// a command highlighted before it is installed must run once it is.
func (h *highlighter) inPath(name string) bool {
	sh := h.sh
	path := sh.GetList("PATH")
	key := strings.Join(path, "\x00")
	c := sh.shared.paths
	c.mu.Lock()
	if key != c.key {
		c.key, c.hits, c.miss = key, map[string]string{}, map[string]bool{}
	}
	_, ok := c.hits[name]
	c.mu.Unlock()
	if ok {
		return true
	}
	for _, d := range path {
		if d == "" {
			d = "."
		}
		if !filepath.IsAbs(d) {
			d = filepath.Join(sh.dir, d)
		}
		p := filepath.Join(d, name)
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
			continue
		}
		c.mu.Lock()
		if c.key == key {
			c.hits[name] = p // also overrides a miss the shell remembers
		}
		c.mu.Unlock()
		return true
	}
	return false
}

// hlAbbr reports whether word is an abbreviation in command position,
// like ExpandAbbr(word, true, "") but without running --function ones.
func hlAbbr(sh *Shell, word string) bool {
	t := sh.shared.abbrs
	t.mu.RLock()
	defer t.mu.RUnlock()
	var match *Abbr
	if a, ok := t.index[word]; ok && a.Regex == nil {
		match = a
	} else {
		for _, a := range t.list {
			if a.Regex != nil && a.Regex.MatchString(word) {
				match = a
				break
			}
		}
	}
	return match != nil && match.Command == ""
}

// isPath reports whether p (relative to the shell's directory) names an
// existing file, or a directory if dir is set. While the word is still
// being typed, a prefix of an entry in its directory is enough.
func (h *highlighter) isPath(p string, typing, dir bool) bool {
	if p == "" || len(p) > 1024 {
		return false
	}
	abs := h.abs(p)
	mustDir := p[len(p)-1] == '/'
	if st, ok := h.stat(abs); ok && (!dir && !mustDir || st.mode.IsDir()) {
		return true
	}
	if !typing || mustDir || h.fsOff {
		return false
	}
	i := strings.LastIndexByte(abs, '/')
	d := abs[:i]
	if d == "" {
		d = "/"
	}
	return h.dirHasPrefix(d, abs[i+1:], dir)
}

// cdTarget reports whether cd could go to v (or, while typing, to a
// directory starting with v), honoring CDPATH.
func (h *highlighter) cdTarget(v string, typing bool) bool {
	if !strings.HasPrefix(v, "/") && !strings.HasPrefix(v, ".") {
		for _, cp := range h.sh.GetList("CDPATH") {
			if cp == "" || cp == "." {
				continue
			}
			base := h.sh.absPath(h.sh.expandTildeString(cp))
			if h.isPath(strings.TrimSuffix(base, "/")+"/"+v, typing, true) {
				return true
			}
		}
	}
	if v == "" {
		v = "."
	}
	return h.isPath(v, typing, true)
}

// cdProbe reports whether "cd v" is worth suggesting: v is a directory
// other than the current one.
func (h *highlighter) cdProbe(v string) bool {
	if v == "-" {
		return true
	}
	if v == "" {
		v = "."
	}
	dest := ""
	if !strings.HasPrefix(v, "/") && !strings.HasPrefix(v, ".") {
		for _, cp := range h.sh.GetList("CDPATH") {
			if cp == "" {
				continue
			}
			cand := strings.TrimSuffix(h.sh.absPath(h.sh.expandTildeString(cp)), "/") + "/" + v
			if st, ok := h.stat(cand); ok && st.mode.IsDir() {
				dest = cand
				break
			}
		}
	}
	if dest == "" {
		dest = h.abs(v)
		if st, ok := h.stat(dest); !ok || !st.mode.IsDir() {
			return false
		}
	}
	return !sameFile(dest, h.sh.dir)
}

// dirHasPrefix reports whether directory d has an entry starting with
// frag (a directory, if dir is set).
func (h *highlighter) dirHasPrefix(d, frag string, dir bool) bool {
	st, ok := h.stat(d)
	if !ok || !st.mode.IsDir() {
		return false
	}
	l := h.listDir(d, st.mod)
	if l == nil {
		return false
	}
	lf := strings.ToLower(frag)
	for i := sort.SearchStrings(l.lower, lf); i < len(l.lower) && strings.HasPrefix(l.lower[i], lf); i++ {
		if !hlFoldCase && !strings.HasPrefix(l.names[i], frag) {
			continue
		}
		if !dir {
			return true
		}
		t := l.types[i]
		if t.IsDir() {
			return true
		}
		if t&os.ModeSymlink != 0 {
			if st, ok := h.stat(strings.TrimSuffix(d, "/") + "/" + l.names[i]); ok && st.mode.IsDir() {
				return true
			}
		}
	}
	return false
}

// hlDirList is a directory listing sorted by lower-cased name. Listings
// are kept across calls (a few of them) and reused while the directory's
// modification time is unchanged, so typing a path in a big directory
// reads it only once.
type hlDirList struct {
	dir   string
	mod   int64
	big   bool // over hlDirMax entries: not listed
	lower []string
	names []string
	types []os.FileMode
}

// hlDirMax is the most entries read from one directory for prefix
// matches. Reading a huge one (a Maildir, /nix/store) would stall typing
// and keep its names in memory; such directories just get no underline.
var hlDirMax = 1 << 13

var hlDirCache struct {
	sync.Mutex
	lists [8]*hlDirList
	next  int
}

func (h *highlighter) listDir(d string, mod int64) *hlDirList {
	c := &hlDirCache
	c.Lock()
	for _, l := range c.lists {
		if l != nil && l.dir == d && l.mod == mod {
			c.Unlock()
			if l.big {
				return nil
			}
			return l
		}
	}
	c.Unlock()
	if h.slow() {
		return nil
	}
	f, err := os.Open(d)
	if err != nil {
		return nil
	}
	var ents []os.DirEntry
	big := false
	for {
		chunk, err := f.ReadDir(256)
		ents = append(ents, chunk...)
		if len(ents) > hlDirMax {
			big = true
			break
		}
		if err != nil || len(chunk) == 0 {
			break
		}
		if h.slow() {
			f.Close()
			return nil // out of time: maybe on the next keystroke
		}
	}
	f.Close()
	l := &hlDirList{dir: d, mod: mod, big: big}
	if !big {
		l.lower, l.names, l.types = make([]string, len(ents)), make([]string, len(ents)), make([]os.FileMode, len(ents))
		idx := make([]int, len(ents))
		low := make([]string, len(ents))
		for i, e := range ents {
			idx[i] = i
			low[i] = strings.ToLower(e.Name())
		}
		sort.Slice(idx, func(a, b int) bool { return low[idx[a]] < low[idx[b]] })
		for i, j := range idx {
			l.lower[i], l.names[i], l.types[i] = low[j], ents[j].Name(), ents[j].Type()
		}
	}
	c.Lock()
	slot := -1
	for i, x := range c.lists {
		if x != nil && x.dir == d {
			slot = i
			break
		}
	}
	if slot < 0 {
		slot = c.next
		c.next = (c.next + 1) % len(c.lists)
	}
	c.lists[slot] = l
	c.Unlock()
	if big {
		return nil
	}
	return l
}
