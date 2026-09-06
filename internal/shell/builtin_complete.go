package shell

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func init() {
	builtins["complete"] = bComplete
	builtinHelp["complete"] = "define or query tab completions"
}

const completeUsage = `usage: complete ((-c | --command) | (-p | --path)) COMMAND [OPTIONS]
       complete (-C | --do-complete) [--escape] STRING

  -s, --short-option X       complete the option -X
  -l, --long-option NAME     complete the option --NAME
  -o, --old-option NAME      complete the option -NAME
  -a, --arguments ARGS       arguments to offer ((cmd) is run when completing)
  -d, --description TEXT     description shown in the pager
  -n, --condition CODE       only if CODE succeeds
  -f, --no-files             no file names after this
  -F, --force-files          file names even if another spec says -f
  -r, --require-parameter    the option takes the next word as argument
  -x, --exclusive            -r and -f
  -k, --keep-order           don't sort the arguments
  -w, --wraps COMMAND        also use the completions of COMMAND
  -e, --erase                erase completions
  -C, --do-complete STRING   print the completions for STRING
`

// completeLongOpts maps complete's long options to their short letters.
var completeLongOpts = map[string]byte{
	"command":           'c',
	"path":              'p',
	"short-option":      's',
	"long-option":       'l',
	"old-option":        'o',
	"description":       'd',
	"arguments":         'a',
	"condition":         'n',
	"wraps":             'w',
	"no-files":          'f',
	"force-files":       'F',
	"require-parameter": 'r',
	"requires-param":    'r',
	"exclusive":         'x',
	"keep-order":        'k',
	"erase":             'e',
	"do-complete":       'C',
	"escape":            'E',
	"help":              'h',
}

func bComplete(sh *Shell, io *IO, args []string) int {
	var (
		cmds, paths          []string
		shorts, olds, longs  []string
		desc                 string
		argList              []string
		conds                []string
		wraps                []string
		noFiles, forceFiles  bool
		reqParam, keepOrder  bool
		erase, escape        bool
		doComplete, doParamS bool
		doParam              string
		positional           []string
		defining             bool
	)
	set := func(c byte, v string) {
		defining = defining || c != 'c' && c != 'p'
		switch c {
		case 'c':
			cmds = append(cmds, v)
		case 'p':
			paths = append(paths, v)
		case 's':
			shorts = append(shorts, v)
		case 'l':
			longs = append(longs, v)
		case 'o':
			olds = append(olds, v)
		case 'd':
			desc = v
		case 'a':
			argList = append(argList, v)
		case 'n':
			conds = append(conds, v)
		case 'w':
			wraps = append(wraps, v)
		}
	}
	flag := func(c byte) bool {
		switch c {
		case 'f':
			noFiles = true
		case 'F':
			forceFiles = true
		case 'r':
			reqParam = true
		case 'x':
			reqParam, noFiles = true, true
		case 'k':
			keepOrder = true
		case 'e':
			erase = true
		case 'E':
			escape = true
		default:
			return false
		}
		defining = defining || c != 'E'
		return true
	}
	a := args[1:]
	for i := 0; i < len(a); i++ {
		arg := a[i]
		if arg == "--" {
			positional = append(positional, a[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positional = append(positional, arg)
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, val, hasVal := strings.Cut(arg[2:], "=")
			c, ok := completeLongOpts[name]
			if !ok {
				// unambiguous prefixes are accepted, as with getopt
				var match []string
				for k := range completeLongOpts {
					if strings.HasPrefix(k, name) {
						match = append(match, k)
					}
				}
				if len(match) == 1 || len(match) == 2 && completeLongOpts[match[0]] == completeLongOpts[match[1]] {
					c, ok = completeLongOpts[match[0]], true
				}
			}
			if !ok {
				fmt.Fprintf(io.Err, "complete: %s: unknown option\n", arg)
				return 2
			}
			switch c {
			case 'h':
				io.Out.WriteString(completeUsage)
				return 0
			case 'C':
				doComplete = true
				if hasVal {
					doParam, doParamS = val, true
				}
			case 'f', 'F', 'r', 'x', 'k', 'e', 'E':
				flag(c)
			default:
				if !hasVal {
					if i+1 >= len(a) {
						fmt.Fprintf(io.Err, "complete: %s: option requires an argument\n", arg)
						return 2
					}
					i++
					val = a[i]
				}
				set(c, val)
			}
			continue
		}
		for j := 1; j < len(arg); j++ {
			c := arg[j]
			switch c {
			case 'c', 'p', 's', 'l', 'o', 'd', 'a', 'n', 'w':
				var v string
				switch {
				case j+1 < len(arg):
					v = arg[j+1:]
				case i+1 < len(a):
					i++
					v = a[i]
				default:
					fmt.Fprintf(io.Err, "complete: -%c: option requires an argument\n", c)
					return 2
				}
				set(c, v)
				j = len(arg)
			case 'C':
				doComplete = true
				if j+1 < len(arg) {
					doParam, doParamS = arg[j+1:], true
					j = len(arg)
				}
			case 'h':
				io.Out.WriteString(completeUsage)
				return 0
			default:
				// (--escape has no short form)
				if c == 'E' || !flag(c) {
					fmt.Fprintf(io.Err, "complete: -%c: unknown option\n", c)
					return 2
				}
			}
		}
	}
	if len(positional) > 0 {
		switch {
		case doComplete && !doParamS && len(positional) == 1:
			doParam, doParamS = positional[0], true
		case !doComplete && len(cmds) == 0 && len(paths) == 0 && len(positional) == 1:
			cmds = append(cmds, positional[0])
		default:
			fmt.Fprintln(io.Err, "complete: too many arguments")
			return 2
		}
	}
	if doComplete {
		if !doParamS {
			// the command line: the one being completed, if any
			ed := sh.Editor
			if r := sh.shared.complete.active.Load(); r != nil {
				ed = r.ed
			}
			if ed == nil {
				return 0
			}
			buf, pos := ed.Buffer()
			if pos > len(buf) {
				pos = len(buf)
			}
			doParam = buf[:pos]
		}
		return sh.printCompletions(io, doParam, escape)
	}
	c := sh.shared.complete
	if len(cmds) == 0 && len(paths) == 0 {
		// like fish: nothing to define for, so list everything
		c.print(io.Out, nil, nil)
		return 0
	}
	if !defining {
		c.print(io.Out, cmds, paths)
		return 0
	}
	for _, s := range shorts {
		if utf8.RuneCountInString(s) != 1 {
			fmt.Fprintf(io.Err, "complete: -s %s: a short option is a single character\n", Quote(s))
			return 2
		}
	}
	if erase {
		var match func(*compOpt) bool
		if len(shorts)+len(olds)+len(longs) > 0 {
			match = func(o *compOpt) bool {
				var l []string
				switch o.kind {
				case optShort:
					l = shorts
				case optOld:
					l = olds
				case optLong:
					l = longs
				}
				for _, n := range l {
					if n == o.name {
						return true
					}
				}
				return false
			}
		}
		for _, cmd := range cmds {
			c.removeOpts(cmd, false, match)
			for _, w := range wraps {
				c.removeWrap(cmd, w)
			}
		}
		for _, p := range paths {
			c.removeOpts(p, true, match)
		}
		return 0
	}
	fish := sh.fishContext()
	base := &compOpt{
		args:       strings.Join(argList, " "),
		desc:       desc,
		conds:      conds,
		reqParam:   reqParam,
		noFiles:    noFiles,
		forceFiles: forceFiles,
		keepOrder:  keepOrder,
	}
	for _, cond := range conds {
		f, err := parseCompCode(cond, fish)
		if err != nil {
			fmt.Fprintf(io.Err, "complete: -n %s: %v\n", Quote(cond), err)
			return 1
		}
		base.condStmts = append(base.condStmts, f.Stmts)
	}
	if base.args != "" {
		words, err := parseCompArgs(base.args, fish)
		if err != nil {
			fmt.Fprintf(io.Err, "complete: -a %s: contains a syntax error: %v\n", Quote(base.args), err)
			return 1
		}
		base.argWords = words
	}
	add := func(cmd string, isPath bool) {
		if len(shorts)+len(olds)+len(longs) == 0 {
			if base.args != "" || base.noFiles || base.forceFiles || base.desc != "" || len(base.conds) > 0 || len(wraps) == 0 {
				o := *base
				o.kind = optArgs
				c.addOpt(cmd, isPath, &o)
			}
			return
		}
		for _, s := range shorts {
			o := *base
			o.kind, o.name = optShort, s
			c.addOpt(cmd, isPath, &o)
		}
		for _, s := range longs {
			o := *base
			o.kind, o.name = optLong, s
			c.addOpt(cmd, isPath, &o)
		}
		for _, s := range olds {
			o := *base
			o.kind, o.name = optOld, s
			c.addOpt(cmd, isPath, &o)
		}
	}
	for _, cmd := range cmds {
		add(cmd, false)
		for _, w := range wraps {
			c.addWrap(cmd, w)
		}
	}
	for _, p := range paths {
		add(p, true)
	}
	return 0
}

// fishContext reports whether the code running now uses fish quoting
// rules (it comes from a .fish file).
func (sh *Shell) fishContext() bool {
	if n := len(sh.srcStack); n > 0 {
		return strings.HasSuffix(sh.srcStack[n-1], ".fish")
	}
	if n := len(sh.callStack); n > 0 {
		return strings.HasSuffix(sh.callStack[n-1].file, ".fish")
	}
	return false
}

// printCompletions prints the completions of s as complete -C does: the
// completed token (unescaped unless escape is set), a tab and the
// description.
func (sh *Shell) printCompletions(io *IO, s string, escape bool) int {
	comp, line := sh.completeLine(s, len(s))
	var b strings.Builder
	for _, it := range comp.Items {
		text := s[line.tok.start:comp.Start] + it.Text
		if !escape {
			text = unescapeToken(text)
		}
		b.WriteString(text)
		if it.Description != "" {
			b.WriteByte('\t')
			b.WriteString(it.Description)
		}
		b.WriteByte('\n')
	}
	io.Out.WriteString(b.String())
	return 0
}
