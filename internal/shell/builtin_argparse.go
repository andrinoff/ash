package shell

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// argparse follows fish 4.8: messages, statuses and the corner cases of
// its option parser (abbreviated long options, -long, clustered short
// flags, integer flags, $argv_opts) were checked against fish itself.

func init() {
	builtins["argparse"] = bArgparse
	builtinHelp["argparse"] = "parse options passed to a fish script or function"
	builtins["_validate_int"] = bValidateInt
	builtinHelp["_validate_int"] = "check an argparse flag value is an integer (for name=!_validate_int)"
}

// foptArg says whether an option takes a value.
type foptArg uint8

const (
	foptNone foptArg = iota
	foptRequired
	foptOptional
)

// foptDef describes one option for foptParser.
type foptDef struct {
	short rune   // 0: no short form
	long  string // "": no long form
	arg   foptArg
}

// foptParser walks arguments the way fish's wgetopt does: clustered short
// flags (-abc, -nVALUE), --long, --long=VALUE, unique prefixes of long
// names, and -long when its first letter is not a short option.
type foptParser struct {
	defs    []foptDef
	permute bool // keep looking for options after non-options
}

// foptHit is one option found by foptParser.
type foptHit struct {
	def   int // index into defs
	value string
	has   bool // value present
}

// parse calls on for every option in args. It returns the non-option
// arguments and whether a "--" ended the options. On a usage error it
// returns a message like "-z: unknown option"; when on returns false it
// stops with an empty message (on printed its own).
func (p *foptParser) parse(args []string, on func(foptHit) bool) (rest []string, dashdash bool, errMsg string, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(rest, args[i+1:]...), true, "", true
		}
		if len(a) < 2 || a[0] != '-' {
			if !p.permute {
				return append(rest, args[i:]...), false, "", true
			}
			rest = append(rest, a)
			continue
		}
		first, _ := utf8.DecodeRuneInString(a[1:])
		if strings.HasPrefix(a, "--") || p.shortDef(first) < 0 {
			body := strings.TrimPrefix(a[1:], "-")
			name, val, hasVal := strings.Cut(body, "=")
			d := p.longDef(name)
			if d < 0 {
				return nil, false, a + ": unknown option", false
			}
			h := foptHit{def: d}
			switch p.defs[d].arg {
			case foptNone:
				if hasVal {
					return nil, false, a + ": option does not take an argument", false
				}
			case foptRequired:
				switch {
				case hasVal:
					h.value, h.has = val, true
				case i+1 < len(args):
					i++
					h.value, h.has = args[i], true
				default:
					return nil, false, a + ": option requires an argument", false
				}
			case foptOptional:
				h.value, h.has = val, hasVal
			}
			if !on(h) {
				return nil, false, "", false
			}
			continue
		}
		for j := 1; j < len(a); {
			c, size := utf8.DecodeRuneInString(a[j:])
			rest := a[j+size:]
			j += size
			d := p.shortDef(c)
			if d < 0 {
				return nil, false, a + ": unknown option", false
			}
			h := foptHit{def: d}
			switch p.defs[d].arg {
			case foptRequired:
				switch {
				case rest != "":
					h.value, h.has = rest, true
				case i+1 < len(args):
					i++
					h.value, h.has = args[i], true
				default:
					return nil, false, "-" + string(c) + ": option requires an argument", false
				}
				j = len(a)
			case foptOptional:
				h.value, h.has = rest, rest != ""
				j = len(a)
			}
			if !on(h) {
				return nil, false, "", false
			}
		}
	}
	return rest, false, "", true
}

func (p *foptParser) shortDef(c rune) int {
	for i, d := range p.defs {
		if d.short != 0 && d.short == c {
			return i
		}
	}
	return -1
}

// longDef finds a long option by exact name or unique prefix.
func (p *foptParser) longDef(name string) int {
	if name == "" {
		return -1
	}
	found := -1
	for i, d := range p.defs {
		if d.long == "" {
			continue
		}
		if d.long == name {
			return i
		}
		if strings.HasPrefix(d.long, name) {
			if found >= 0 {
				return -1 // ambiguous
			}
			found = i
		}
	}
	return found
}

// argparseInt parses an integer the way fish_wcstol does: base 10,
// optional sign, surrounding whitespace allowed, no overflow.
func argparseInt(s string) (int64, bool) {
	t := strings.TrimFunc(s, unicode.IsSpace)
	digits := strings.TrimPrefix(strings.TrimPrefix(t, "+"), "-")
	if len(t)-len(digits) > 1 || digits == "" {
		return 0, false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(t, "+"), 10, 64)
	return n, err == nil
}

// argparseIsInt reports whether s matches ^-?[0-9]+$.
func argparseIsInt(s string) bool {
	s = strings.TrimPrefix(s, "-")
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

func argparseAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }

// apOpt is one parsed option spec.
type apOpt struct {
	short    rune // 0: none
	shortOK  bool // -c works and sets _flag_c (false for the h-help form)
	long     string
	arg      foptArg
	multi    bool // =+ and =*: keep every value
	isInt    bool // # spec: -NUMBER sets this flag
	intChar  rune // name used for an integer flag without a short letter
	noSave   bool // &: not copied to $argv_opts
	validate string
	vals     []string
	seen     int
}

// label is how fish names the option in "cannot be used together".
func (o *apOpt) label() string {
	s := ""
	if o.shortOK {
		s = string(o.short)
	}
	if o.long != "" {
		if o.shortOK {
			s += "/"
		}
		s += o.long
	}
	return s
}

// Where an argument ends up.
const (
	apToOpts = iota // $argv_opts
	apToArgv        // $argv
	apDrop          // nowhere (& options)
)

type apPiece struct {
	text string
	dest int
}

type argparser struct {
	sh   *Shell
	io   *IO
	cmd  string
	name string

	stopNonopt    bool
	ignoreUnknown bool
	moveUnknown   bool
	strict        bool
	unknownArg    foptArg
	minArgs       int64
	maxArgs       int64 // -1: no limit
	exclusive     []string

	opts    []*apOpt
	byShort map[rune]*apOpt
	byLong  map[string]*apOpt
	intOpt  *apOpt
	xsets   [][]*apOpt

	argv     []string
	argvOpts []string
}

func (p *argparser) fail(status int, format string, args ...any) int {
	fmt.Fprintf(p.io.Err, "%s: %s\n", p.name, fmt.Sprintf(format, args...))
	return status
}

// argparse's own options, indexes into argparseOwnOpts.
const (
	apStopNonopt = iota
	apIgnoreUnknown
	apMoveUnknown
	apStrictLongopts
	apUnknownArguments
	apName
	apExclusive
	apHelp
	apMinArgs
	apMaxArgs
)

var argparseOwnOpts = []foptDef{
	apStopNonopt:       {'s', "stop-nonopt", foptNone},
	apIgnoreUnknown:    {'i', "ignore-unknown", foptNone},
	apMoveUnknown:      {'u', "move-unknown", foptNone},
	apStrictLongopts:   {'S', "strict-longopts", foptNone},
	apUnknownArguments: {0, "unknown-arguments", foptRequired},
	apName:             {'n', "name", foptRequired},
	apExclusive:        {'x', "exclusive", foptRequired},
	apHelp:             {'h', "help", foptNone},
	apMinArgs:          {'N', "min-args", foptRequired},
	apMaxArgs:          {'X', "max-args", foptRequired},
}

func bArgparse(sh *Shell, io *IO, args []string) int {
	p := &argparser{
		sh: sh, io: io, cmd: args[0], maxArgs: -1, unknownArg: foptOptional,
		byShort: map[rune]*apOpt{}, byLong: map[string]*apOpt{},
	}
	usageErr := func(msg string) int {
		fmt.Fprintf(io.Err, "%s: %s\n", p.cmd, msg)
		return 2
	}
	own := &foptParser{defs: argparseOwnOpts}
	help, kindGiven := false, false
	status := 0
	rest, dashdash, msg, ok := own.parse(args[1:], func(h foptHit) bool {
		switch h.def {
		case apStopNonopt:
			p.stopNonopt = true
		case apIgnoreUnknown:
			p.ignoreUnknown = true
		case apMoveUnknown:
			p.moveUnknown = true
		case apStrictLongopts:
			p.strict = true
		case apUnknownArguments:
			switch h.value {
			case "optional":
				p.unknownArg = foptOptional
			case "required":
				p.unknownArg = foptRequired
			case "none":
				p.unknownArg = foptNone
			default:
				status = usageErr(fmt.Sprintf("Invalid --unknown-arguments value '%s'", h.value))
				return false
			}
			kindGiven = true
		case apName:
			p.name = h.value
		case apExclusive:
			p.exclusive = append(p.exclusive, h.value)
		case apHelp:
			help = true
		case apMinArgs, apMaxArgs:
			n, ok := argparseInt(h.value)
			if !ok || n < 0 {
				status = usageErr(fmt.Sprintf("Invalid --%s value '%s'", own.defs[h.def].long, h.value))
				return false
			}
			if h.def == apMinArgs {
				p.minArgs = n
			} else {
				p.maxArgs = n
			}
		}
		return true
	})
	if !ok {
		if msg != "" {
			return usageErr(msg)
		}
		return status
	}
	if help {
		io.Out.WriteString(argparseUsage)
		return 0
	}
	if p.ignoreUnknown && p.moveUnknown {
		return usageErr("--ignore-unknown --move-unknown: options cannot be used together")
	}
	if kindGiven && !p.ignoreUnknown {
		p.moveUnknown = true
	}
	if p.name == "" {
		p.name = p.cmd
		if n := len(sh.callStack); n > 0 {
			p.name = sh.callStack[n-1].name
		}
	}
	toParse := rest
	if !dashdash {
		found := false
		for i, spec := range rest {
			if spec == "--" {
				toParse, found = rest[i+1:], true
				break
			}
			if msg := p.addSpec(spec); msg != "" {
				return p.fail(1, "%s", msg)
			}
		}
		if !found {
			return usageErr("Missing -- separator")
		}
	}
	if st := p.parseExclusive(); st != 0 {
		return st
	}
	if st := p.parseArgs(toParse); st != 0 {
		return st
	}
	if st := p.checkExclusive(); st != 0 {
		return st
	}
	if n := int64(len(p.argv)); n < p.minArgs {
		return p.fail(1, "expected >= %d arguments; got %d", p.minArgs, n)
	}
	if n := int64(len(p.argv)); p.maxArgs >= 0 && n > p.maxArgs {
		return p.fail(1, "expected <= %d arguments; got %d", p.maxArgs, n)
	}
	p.setVars()
	return 0
}

const argparseUsage = `argparse - parse options passed to a fish script or function

usage: argparse [OPTIONS] OPTION_SPEC... -- [ARG...]

  -n, --name NAME           name used in error messages (default: the function)
  -x, --exclusive A,B,...   these options cannot be used together
  -N, --min-args NUMBER     require at least NUMBER non-option arguments
  -X, --max-args NUMBER     allow at most NUMBER non-option arguments
  -s, --stop-nonopt         stop at the first non-option argument
  -i, --ignore-unknown      keep unknown options in $argv
  -u, --move-unknown        move unknown options to $argv_opts
  -S, --strict-longopts     no abbreviated long options and no -long
      --unknown-arguments optional|required|none
                            whether unknown options take a value
  -h, --help                show this help

Option specs: h/help (-h or --help), help (--help only), h (-h or --h),
n/name= (needs a value), n/name=? (optional value), n/name=+ (repeatable),
n/name=* (repeatable, optional value), n#max or #-max (-NUMBER), a
trailing & keeps the option out of $argv_opts, and !COMMAND validates the
value (with $_flag_name, $_flag_value and $_argparse_cmd set).
Seen options set $_flag_SHORT and $_flag_LONG; $argv gets the remaining
arguments and $argv_opts the options.
`

// claimInt makes o the implicit integer flag.
func (p *argparser) claimInt(o *apOpt) string {
	if p.intOpt != nil {
		c := p.intOpt.short
		if c == 0 {
			c = '#'
		}
		return fmt.Sprintf("Implicit int flag '%c' already defined", c)
	}
	p.intOpt = o
	return ""
}

// addSpec parses one option spec, returning an error message or "".
func (p *argparser) addSpec(spec string) string {
	if spec == "" {
		return "An option spec must have at least a short or a long flag"
	}
	rs := []rune(spec)
	invalid := func(at int) string {
		return fmt.Sprintf("Invalid option spec '%s' at char '%c'", spec, rs[at])
	}
	readLong := func(i int) (string, int) {
		j := i
		for j < len(rs) && (argparseAlnum(rs[j]) || rs[j] == '-' || rs[j] == '_') {
			j++
		}
		return string(rs[i:j]), j
	}
	o := &apOpt{}
	i := 0
	switch c := rs[0]; {
	case c == '/' && len(rs) > 1:
		i = 1
		if rs[1] == '-' { // "/-name" is "/name"
			if len(rs) == 2 {
				return invalid(1)
			}
			i = 2
		}
		o.long, i = readLong(i)
	case c == '#':
		if len(rs) == 1 {
			return invalid(0)
		}
		// An integer flag without a letter: fish calls it '#' when spelled
		// "#-name", and shows its internal placeholder U+E000 otherwise.
		o.isInt, o.intChar, i = true, '\uE000', 1
		if rs[1] == '-' {
			o.intChar, i = '#', 2
		}
		if msg := p.claimInt(o); msg != "" {
			return msg
		}
		o.long, i = readLong(i)
	case !argparseAlnum(c):
		return fmt.Sprintf("Short flag '%c' invalid, must be alphanum or '#'", c)
	case len(rs) == 1:
		// A lone letter works as both -c and --c.
		o.short, o.shortOK, o.long, i = c, true, string(c), 1
	case rs[1] == '/' || rs[1] == '-':
		o.short, o.shortOK = c, rs[1] == '/'
		if len(rs) == 2 {
			return invalid(1)
		}
		o.long, i = readLong(2)
	case rs[1] == '#':
		o.short, o.shortOK, o.isInt = c, true, true
		if msg := p.claimInt(o); msg != "" {
			return msg
		}
		o.long, i = readLong(2)
	case argparseAlnum(rs[1]) || rs[1] == '_':
		o.long, i = readLong(0)
	default:
		o.short, o.shortOK, i = c, true, 1
	}
	if o.isInt {
		if i < len(rs) && rs[i] != '!' && rs[i] != '&' {
			c := o.intChar
			if o.short != 0 {
				c = o.short
			}
			return fmt.Sprintf("Implicit int short flag '%c' does not allow modifiers like '%c'", c, rs[i])
		}
		// fish quirk: without a short letter, --long is a plain switch.
		if o.short != 0 {
			o.arg = foptRequired
		}
	}
	if i < len(rs) && rs[i] == '=' {
		i++
		o.arg = foptRequired
		if i < len(rs) {
			switch rs[i] {
			case '?':
				o.arg, i = foptOptional, i+1
			case '+':
				o.multi, i = true, i+1
			case '*':
				o.arg, o.multi, i = foptOptional, true, i+1
			}
		}
	}
	if i < len(rs) && rs[i] == '&' {
		o.noSave, i = true, i+1
	}
	if i < len(rs) && rs[i] == '!' {
		if o.arg == foptNone && !o.isInt {
			// fish complains but carries on with a plain switch.
			p.fail(1, "%s", invalid(i))
		} else {
			o.validate = string(rs[i+1:])
		}
		i = len(rs)
	}
	if i < len(rs) {
		return invalid(i)
	}
	if o.long != "" && p.byLong[o.long] != nil {
		return fmt.Sprintf("Long flag '%s' already defined", o.long)
	}
	if o.short != 0 && p.byShort[o.short] != nil {
		return fmt.Sprintf("Short flag '%c' already defined", o.short)
	}
	if o.long != "" {
		p.byLong[o.long] = o
	}
	if o.short != 0 {
		p.byShort[o.short] = o
	} else if o.isInt && o.intChar == '#' && p.byShort['#'] == nil {
		p.byShort['#'] = o // lets -x name it '#', like fish
	}
	p.opts = append(p.opts, o)
	return ""
}

func (p *argparser) parseExclusive() int {
	for _, raw := range p.exclusive {
		flags := strings.Split(raw, ",")
		if len(flags) < 2 {
			return p.fail(1, "exclusive flag string '%s' is not valid", raw)
		}
		var set []*apOpt
		for _, f := range flags {
			var o *apOpt
			if r, size := utf8.DecodeRuneInString(f); size > 0 && size == len(f) {
				o = p.byShort[r]
			}
			if o == nil {
				o = p.byLong[f]
			}
			if o == nil {
				return p.fail(1, "exclusive flag '%s' is not valid", f)
			}
			set = append(set, o)
		}
		p.xsets = append(p.xsets, set)
	}
	return 0
}

func (p *argparser) checkExclusive() int {
	for _, o := range p.opts {
		if o.seen == 0 {
			continue
		}
		for _, set := range p.xsets {
			mine := false
			for _, x := range set {
				mine = mine || x == o
			}
			if !mine {
				continue
			}
			for _, x := range set {
				if x == o || x.seen == 0 {
					continue
				}
				a, b := o.label(), x.label()
				if a > b {
					a, b = b, a
				}
				return p.fail(2, "%s %s: options cannot be used together", a, b)
			}
		}
	}
	return 0
}

// findLong looks up a long option by name or, unless --strict-longopts,
// by unique prefix.
func (p *argparser) findLong(name string) *apOpt {
	if name == "" {
		return nil
	}
	if o := p.byLong[name]; o != nil {
		return o
	}
	if p.strict {
		return nil
	}
	var found *apOpt
	for _, o := range p.opts {
		if o.long != "" && strings.HasPrefix(o.long, name) {
			if found != nil {
				return nil
			}
			found = o
		}
	}
	return found
}

func (p *argparser) emit(dest int, s string) {
	switch dest {
	case apToOpts:
		p.argvOpts = append(p.argvOpts, s)
	case apToArgv:
		p.argv = append(p.argv, s)
	}
}

func (o *apOpt) dest() int {
	if o.noSave {
		return apDrop
	}
	return apToOpts
}

func (p *argparser) unknownDest() int {
	if p.ignoreUnknown {
		return apToArgv
	}
	return apToOpts
}

func (p *argparser) parseArgs(args []string) int {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.argv = append(p.argv, args[i+1:]...)
			return 0
		}
		if len(a) < 2 || a[0] != '-' {
			if p.stopNonopt {
				p.argv = append(p.argv, args[i:]...)
				return 0
			}
			p.argv = append(p.argv, a)
			continue
		}
		used, st := p.parseOption(args, i)
		if st != 0 {
			return st
		}
		i += used
	}
	return 0
}

// parseOption handles the option args[i] and returns how many following
// arguments it consumed.
func (p *argparser) parseOption(args []string, i int) (int, int) {
	a := args[i]
	first, _ := utf8.DecodeRuneInString(a[1:])
	long := strings.HasPrefix(a, "--")
	if o := p.byShort[first]; !long && o != nil && o.shortOK {
		return p.parseCluster(args, i)
	}
	if long || !p.strict {
		name, _, _ := strings.Cut(strings.TrimPrefix(a[1:], "-"), "=")
		if o := p.findLong(name); o != nil {
			return p.parseLong(args, i, o)
		}
	}
	if p.intOpt != nil {
		if _, ok := argparseInt(a[1:]); ok {
			o := p.intOpt
			o.seen++
			if st := p.check(o, p.flagName(o, false), a[1:]); st != 0 {
				return 0, st
			}
			o.vals = []string{a[1:]}
			p.emit(o.dest(), a)
			return 0, 0
		}
	}
	if !p.ignoreUnknown && !p.moveUnknown {
		return 0, p.fail(2, "%s: unknown option", a)
	}
	if long && p.unknownArg == foptNone && strings.Contains(a, "=") {
		return 0, p.fail(2, "%s: option does not take an argument", a)
	}
	// An unknown option leading its group swallows the whole word.
	p.emit(p.unknownDest(), a)
	return 0, 0
}

func (p *argparser) parseLong(args []string, i int, o *apOpt) (int, int) {
	a := args[i]
	_, val, hasVal := strings.Cut(a, "=")
	used := 0
	var value *string
	switch o.arg {
	case foptNone:
		if hasVal {
			return 0, p.fail(2, "%s: option does not take an argument", a)
		}
	case foptRequired:
		switch {
		case hasVal:
			value = &val
		case i+1 < len(args):
			value, used = &args[i+1], 1
		default:
			return 0, p.fail(2, "%s: option requires an argument", a)
		}
	case foptOptional:
		if hasVal {
			value = &val
		}
	}
	if st := p.record(o, value, true); st != 0 {
		return 0, st
	}
	p.emit(o.dest(), a)
	if used > 0 {
		p.emit(o.dest(), args[i+1])
	}
	return used, 0
}

// parseCluster handles -abc where a is a known short flag.
func (p *argparser) parseCluster(args []string, i int) (int, int) {
	a := args[i]
	var pieces []apPiece
	used, usedDest := 0, apDrop
	for j := 1; j < len(a); {
		c, size := utf8.DecodeRuneInString(a[j:])
		text, rest := a[j:j+size], a[j+size:]
		j += size
		if o := p.byShort[c]; o != nil && o.shortOK {
			var value *string
			switch o.arg {
			case foptRequired:
				switch {
				case rest != "":
					value, text = &rest, text+rest
				case i+1 < len(args):
					value, used, usedDest = &args[i+1], 1, o.dest()
				default:
					return 0, p.fail(2, "-%c: option requires an argument", c)
				}
				j = len(a)
			case foptOptional:
				if rest != "" {
					value, text = &rest, text+rest
				}
				j = len(a)
			}
			if st := p.record(o, value, false); st != 0 {
				return 0, st
			}
			pieces = append(pieces, apPiece{text, o.dest()})
			continue
		}
		if !p.ignoreUnknown && !p.moveUnknown {
			return 0, p.fail(2, "%s: unknown option", a)
		}
		switch p.unknownArg {
		case foptOptional:
			text += rest
			j = len(a)
		case foptRequired:
			switch {
			case rest != "":
				text += rest
			case i+1 < len(args):
				used, usedDest = 1, p.unknownDest()
			default:
				return 0, p.fail(2, "%s: option requires an argument", a)
			}
			j = len(a)
		}
		pieces = append(pieces, apPiece{text, p.unknownDest()})
	}
	same := used == 0 || usedDest == pieces[0].dest
	for _, pc := range pieces {
		same = same && pc.dest == pieces[0].dest
	}
	if same {
		p.emit(pieces[0].dest, a)
		if used > 0 {
			p.emit(usedDest, args[i+1])
		}
		return used, 0
	}
	// Split the group: -i keeps unknown letters in $argv, & drops letters.
	for _, d := range []int{apToOpts, apToArgv} {
		var b strings.Builder
		for _, pc := range pieces {
			if pc.dest == d {
				b.WriteString(pc.text)
			}
		}
		if b.Len() > 0 {
			p.emit(d, "-"+b.String())
		}
		if used > 0 && usedDest == d {
			p.emit(d, args[i+1])
		}
	}
	return used, 0
}

// flagName is what $_flag_name holds while validating.
func (p *argparser) flagName(o *apOpt, viaLong bool) string {
	switch {
	case viaLong:
		return o.long
	case o.short != 0:
		return string(o.short)
	}
	return string(o.intChar)
}

// record stores one occurrence of an option.
func (p *argparser) record(o *apOpt, value *string, viaLong bool) int {
	o.seen++
	if o.arg == foptNone {
		if viaLong {
			o.vals = append(o.vals, "--"+o.long)
		} else {
			o.vals = append(o.vals, "-"+string(o.short))
		}
		return 0
	}
	if value != nil {
		if st := p.check(o, p.flagName(o, viaLong), *value); st != 0 {
			return st
		}
	}
	switch {
	case o.multi && o.arg == foptOptional:
		v := ""
		if value != nil {
			v = *value
		}
		o.vals = append(o.vals, v)
	case o.multi:
		o.vals = append(o.vals, *value)
	case value != nil:
		o.vals = []string{*value}
	default:
		o.vals = []string{}
	}
	return 0
}

// check validates a value with the spec's !command, or as an integer for
// # flags.
func (p *argparser) check(o *apOpt, name, value string) int {
	if o.validate != "" {
		return p.runValidation(o.validate, name, value)
	}
	if o.isInt && !argparseIsInt(value) {
		return p.fail(1, "Value '%s' for flag '%s' is not an integer", value, name)
	}
	return 0
}

// runValidation runs a !command like fish: in a fresh function-like scope
// (the caller's locals are hidden) with $_argparse_cmd, $_flag_name and
// $_flag_value exported, its stdout going to stderr.
func (p *argparser) runValidation(src, name, value string) int {
	sh := p.sh
	// fish makes the three exported locals, which functions called from
	// the check (like _validate_int) still see. ash functions only see
	// globals, so they are globals for the duration of the check.
	saved := map[string]*Var{}
	for k, v := range map[string]string{"_argparse_cmd": p.name, "_flag_name": name, "_flag_value": value} {
		saved[k] = sh.global.get(k)
		sh.global.put(k, &Var{Vals: []string{v}, Export: true})
	}
	scope := sh.scope
	sh.scope = newScope(sh.global, scopeFunc)
	out := &lockedBuffer{}
	vio := &IO{In: p.io.In, Out: &Stream{W: out}, Err: p.io.Err, Extra: p.io.Extra}
	status := sh.RunString(src, "argparse.fish", vio)
	sh.scope = scope
	for k, v := range saved {
		sh.global.put(k, v)
	}
	switch sh.ctrl {
	case ctrlReturn, ctrlBreak, ctrlContinue:
		sh.ctrl = ctrlNone
		status = sh.status
	}
	if s := out.String(); s != "" {
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		p.io.Err.WriteString(s)
	}
	return status
}

// argparseFlagVar turns a flag into a variable name: characters that cannot
// appear in one become underscores.
func argparseFlagVar(flag string) string {
	b := []byte("_flag_")
	for _, r := range flag {
		if r < utf8.RuneSelf && (r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b = append(b, byte(r))
		} else {
			b = append(b, '_')
		}
	}
	return string(b)
}

// setVars stores the results in the calling function's scope. Flags that
// were not given are left alone, as fish does (scripts preset defaults).
func (p *argparser) setVars() {
	local := setOpts{scope: setFunction}
	for _, o := range p.opts {
		if o.seen == 0 {
			continue
		}
		if o.shortOK {
			p.sh.SetVar(argparseFlagVar(string(o.short)), append([]string{}, o.vals...), local)
		}
		if o.long != "" && !(o.shortOK && o.long == string(o.short)) {
			p.sh.SetVar(argparseFlagVar(o.long), append([]string{}, o.vals...), local)
		}
	}
	p.sh.setArgs(p.argv)
	p.sh.SetVar("argv_opts", append([]string{}, p.argvOpts...), local)
}

// bValidateInt is fish's _validate_int, for 'n/num=!_validate_int --min 0'.
func bValidateInt(sh *Shell, io *IO, args []string) int {
	var bounds [2]*string
	op := &foptParser{defs: []foptDef{{0, "min", foptRequired}, {0, "max", foptRequired}}, permute: true}
	_, _, msg, ok := op.parse(args[1:], func(h foptHit) bool {
		v := h.value
		bounds[h.def] = &v
		return true
	})
	if !ok {
		fmt.Fprintf(io.Err, "_argparse_validate_int: %s\n", msg)
		return 2
	}
	cmd, name, value := sh.Get("_argparse_cmd"), sh.Get("_flag_name"), sh.Get("_flag_value")
	if !argparseIsInt(value) {
		fmt.Fprintf(io.Err, "%s: Value '%s' for flag '%s' is not an integer\n", cmd, value, name)
		return 1
	}
	v, _ := new(big.Int).SetString(value, 10)
	for i, word := range []string{"less than min", "greater than max"} {
		b := bounds[i]
		if b == nil {
			continue
		}
		n, ok := new(big.Int).SetString(strings.TrimSpace(*b), 10)
		if !ok {
			continue
		}
		if c := v.Cmp(n); i == 0 && c < 0 || i == 1 && c > 0 {
			fmt.Fprintf(io.Err, "%s: Value '%s' for flag '%s' %s allowed of '%s'\n", cmd, value, name, word, *b)
			return 1
		}
	}
	return 0
}
