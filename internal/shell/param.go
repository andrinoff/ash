package shell

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/andrinoff/ash/internal/syntax"
)

func (sh *Shell) expandParam(pe *syntax.ParamExp, io *IO, quoted bool) ([]string, valKind, error) {
	if pe.Op == syntax.OpBad {
		text, _ := pe.Arg.LitString()
		return nil, kindScalar, errorf("%s: bad substitution", text)
	}
	name := pe.Name
	if pe.Indirect && pe.Short {
		// fish's $$name[i]: the variables named by $name[i], one after another
		refs, _ := sh.getVar(name)
		if pe.Index != nil {
			var err error
			if refs, err = sh.applyFishIndex(refs, pe.Index, io); err != nil {
				return nil, kindScalar, err
			}
		}
		var out []string
		for _, r := range refs {
			vals, _ := sh.getVar(r)
			out = append(out, vals...)
		}
		return out, kindList, nil
	}
	if pe.Indirect {
		ref := sh.Get(name)
		if ref == "" {
			return nil, kindScalar, nil
		}
		name = ref
		if i := strings.IndexByte(ref, '['); i > 0 && strings.HasSuffix(ref, "]") {
			// ${!ref} where ref="arr[2]"
			sub := ref[i+1 : len(ref)-1]
			name = ref[:i]
			vals, _ := sh.getVar(name)
			if sub == "*" && quoted {
				return []string{strings.Join(vals, sh.ifsFirst())}, kindScalar, nil
			}
			if sub == "@" || sub == "*" {
				return vals, kindSplit, nil
			}
			n, err := sh.arith(sub)
			if err != nil {
				return nil, kindScalar, err
			}
			if v, ok := bashIndex(vals, n); ok {
				return []string{v}, kindScalar, nil
			}
			return nil, kindScalar, nil
		}
	}
	switch pe.Op {
	case syntax.OpPrefixNames:
		var out []string
		for _, n := range sh.VarNames() {
			if strings.HasPrefix(n, name) {
				out = append(out, n)
			}
		}
		return out, kindSplit, nil
	case syntax.OpIndices:
		vals, _ := sh.getVar(name)
		out := make([]string, len(vals))
		for i := range vals {
			out[i] = strconv.Itoa(i)
		}
		if pe.Index.Star && quoted {
			return []string{strings.Join(out, sh.ifsFirst())}, kindScalar, nil
		}
		return out, kindSplit, nil
	}

	vals, isSet := sh.getVar(name)
	kind := kindList
	// star marks "$*" and "${arr[*]}": the elements are joined with the
	// first character of IFS, after the operator (if any) has been
	// applied to each of them.
	star := false
	if name == "@" || name == "*" {
		kind = kindSplit
		star = name == "*" && quoted
	} else if v, _ := sh.lookupVar(name); v != nil && v.Path {
		kind = kindPathList
	}
	result := func(vals []string, kind valKind) ([]string, valKind, error) {
		if star {
			return []string{strings.Join(vals, sh.ifsFirst())}, kindScalar, nil
		}
		return vals, kind, nil
	}

	if pe.Index != nil {
		if pe.Index.Fish {
			var err error
			vals, err = sh.applyFishIndex(vals, pe.Index, io)
			if err != nil {
				return nil, kindScalar, err
			}
			if kind == kindPathList {
				kind = kindList
			}
		} else if pe.Index.All {
			kind = kindSplit
			star = pe.Index.Star && quoted
		} else {
			s, err := sh.expandString(pe.Index.Expr, io)
			if err != nil {
				return nil, kindScalar, err
			}
			n, err := sh.arith(s)
			if err != nil {
				return nil, kindScalar, err
			}
			if v, ok := bashIndex(vals, n); ok {
				vals = []string{v}
			} else {
				vals = nil
				isSet = false
			}
			kind = kindScalar
		}
	}

	if pe.Length {
		if pe.Index != nil && (pe.Index.All || pe.Index.Fish) {
			return []string{strconv.Itoa(len(vals))}, kindScalar, nil
		}
		if name == "@" || name == "*" {
			return []string{strconv.Itoa(len(vals))}, kindScalar, nil
		}
		sep := " "
		if kind == kindPathList {
			sep = ":"
		}
		return []string{strconv.Itoa(utf8.RuneCountInString(strings.Join(vals, sep)))}, kindScalar, nil
	}

	isNull := func() bool {
		if !isSet || len(vals) == 0 {
			return true
		}
		if star {
			return strings.Join(vals, sh.ifsFirst()) == ""
		}
		return len(vals) == 1 && vals[0] == ""
	}

	switch pe.Op {
	case syntax.OpNone:
		if !isSet && sh.opts.nounset && name != "@" && name != "*" {
			return nil, kindScalar, sh.fatalExpansion(errorf("%s: unbound variable", name))
		}
		return result(vals, kind)
	case syntax.OpDefault, syntax.OpDefaultUnset:
		if !isSet || pe.Op == syntax.OpDefault && isNull() {
			s, err := sh.expandString(pe.Arg, io)
			return []string{s}, kindScalar, err
		}
		return result(vals, kind)
	case syntax.OpAssign, syntax.OpAssignUnset:
		if !isSet || pe.Op == syntax.OpAssign && isNull() {
			s, err := sh.expandString(pe.Arg, io)
			if err != nil {
				return nil, kindScalar, err
			}
			if err := sh.SetVar(name, []string{s}, setOpts{scope: setShAuto}); err != nil {
				return nil, kindScalar, err
			}
			return []string{s}, kindScalar, nil
		}
		return result(vals, kind)
	case syntax.OpError, syntax.OpErrorUnset:
		if !isSet || pe.Op == syntax.OpError && isNull() {
			msg, _ := sh.expandString(pe.Arg, io)
			if msg == "" {
				msg = "parameter null or not set"
			}
			return nil, kindScalar, sh.fatalExpansion(errorf("%s: %s", name, msg))
		}
		return result(vals, kind)
	case syntax.OpAlt, syntax.OpAltUnset:
		if !isSet || pe.Op == syntax.OpAlt && isNull() {
			return nil, kindScalar, nil
		}
		s, err := sh.expandString(pe.Arg, io)
		return []string{s}, kindScalar, err
	case syntax.OpTrimPrefix, syntax.OpTrimPrefixLong, syntax.OpTrimSuffix, syntax.OpTrimSuffixLong:
		vals, kind = joinPathList(vals, kind)
		pat, err := sh.expandPattern(pe.Arg, io, false)
		if err != nil {
			return nil, kindScalar, err
		}
		out := make([]string, len(vals))
		for i, v := range vals {
			switch pe.Op {
			case syntax.OpTrimPrefix:
				out[i] = trimPrefixPattern(v, pat, false)
			case syntax.OpTrimPrefixLong:
				out[i] = trimPrefixPattern(v, pat, true)
			case syntax.OpTrimSuffix:
				out[i] = trimSuffixPattern(v, pat, false)
			default:
				out[i] = trimSuffixPattern(v, pat, true)
			}
		}
		return result(out, scalarIfOne(kind, out))
	case syntax.OpReplace, syntax.OpReplaceAll, syntax.OpReplacePrefix, syntax.OpReplaceSuffix:
		vals, kind = joinPathList(vals, kind)
		pat, err := sh.expandPattern(pe.Arg, io, false)
		if err != nil {
			return nil, kindScalar, err
		}
		rep := ""
		if pe.HasArg2 {
			rep, err = sh.expandString(pe.Arg2, io)
			if err != nil {
				return nil, kindScalar, err
			}
		}
		out := make([]string, len(vals))
		for i, v := range vals {
			out[i] = replacePattern(v, pat, rep, pe.Op)
		}
		return result(out, scalarIfOne(kind, out))
	case syntax.OpSubstr:
		vals, kind = joinPathList(vals, kind)
		offS, err := sh.expandString(pe.Arg, io)
		if err != nil {
			return nil, kindScalar, err
		}
		off, err := sh.arith(offS)
		if err != nil {
			return nil, kindScalar, err
		}
		length := int64(-1)
		hasLen := false
		if pe.HasArg2 {
			ls, err := sh.expandString(pe.Arg2, io)
			if err != nil {
				return nil, kindScalar, err
			}
			length, err = sh.arith(ls)
			if err != nil {
				return nil, kindScalar, err
			}
			hasLen = true
		}
		if kind == kindSplit {
			return result(sliceList(vals, off, length, hasLen, name == "@" || name == "*"), kindSplit)
		}
		return []string{substr(strings.Join(vals, " "), off, length, hasLen)}, kindScalar, nil
	case syntax.OpUpperFirst, syntax.OpUpperAll, syntax.OpLowerFirst, syntax.OpLowerAll:
		vals, kind = joinPathList(vals, kind)
		out := make([]string, len(vals))
		for i, v := range vals {
			out[i] = caseConvert(v, pe.Op)
		}
		return result(out, scalarIfOne(kind, out))
	case syntax.OpTransform:
		vals, kind = joinPathList(vals, kind)
		op, _ := pe.Arg.LitString()
		out := make([]string, len(vals))
		for i, v := range vals {
			switch op {
			case "Q":
				out[i] = Quote(v)
			case "U":
				out[i] = strings.ToUpper(v)
			case "L":
				out[i] = strings.ToLower(v)
			case "u":
				out[i] = caseConvert(v, syntax.OpUpperFirst)
			case "E":
				out[i] = unescapeEcho(v)
			case "A":
				out[i] = name + "=" + Quote(v)
			default:
				out[i] = v
			}
		}
		return result(out, scalarIfOne(kind, out))
	}
	return result(vals, kind)
}

// fatalExpansion returns err for ${x:?msg} and set -u failures, which
// end a non-interactive shell (or subshell) the way POSIX requires,
// whatever command or assignment the expansion was part of: a script
// guarded with : "${VAR:?}" must not go on without VAR.
func (sh *Shell) fatalExpansion(err error) error {
	if !sh.Interactive {
		sh.ctrl = ctrlExit
		sh.exitStatus = 1
	}
	return err
}

// joinPathList makes ${PATH%%:*} and friends see the ':'-joined string,
// like bash, instead of operating on each element.
func joinPathList(vals []string, kind valKind) ([]string, valKind) {
	if kind != kindPathList {
		return vals, kind
	}
	return []string{strings.Join(vals, ":")}, kindScalar
}

func scalarIfOne(kind valKind, out []string) valKind {
	if kind == kindSplit {
		return kindSplit
	}
	if len(out) == 1 {
		return kindScalar
	}
	return kind
}

func bashIndex(vals []string, n int64) (string, bool) {
	if n < 0 {
		n += int64(len(vals))
	}
	if n < 0 || n >= int64(len(vals)) {
		return "", false
	}
	return vals[n], true
}

func sliceList(vals []string, off, length int64, hasLen, positional bool) []string {
	if positional {
		// ${@:0} includes $0 in bash; we keep it simple and treat 0 as 1
		if off > 0 {
			off--
		}
	}
	n := int64(len(vals))
	if off < 0 {
		off += n
		if off < 0 {
			return nil
		}
	}
	if off > n {
		return nil
	}
	end := n
	if hasLen {
		if length < 0 {
			end = n + length
		} else {
			end = off + length
		}
	}
	if end > n {
		end = n
	}
	if end <= off {
		return nil
	}
	return vals[off:end]
}

func substr(s string, off, length int64, hasLen bool) string {
	rs := []rune(s)
	n := int64(len(rs))
	if off < 0 {
		off += n
		if off < 0 {
			off = 0
		}
	}
	if off > n {
		return ""
	}
	end := n
	if hasLen {
		if length < 0 {
			end = n + length
		} else {
			end = off + length
		}
	}
	if end > n {
		end = n
	}
	if end <= off {
		return ""
	}
	return string(rs[off:end])
}

func caseConvert(s string, op syntax.ParamOp) string {
	switch op {
	case syntax.OpUpperAll:
		return strings.ToUpper(s)
	case syntax.OpLowerAll:
		return strings.ToLower(s)
	}
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	if op == syntax.OpUpperFirst {
		return string(unicode.ToUpper(r)) + s[size:]
	}
	return string(unicode.ToLower(r)) + s[size:]
}

// charBounds returns the offsets where the characters of s start, and
// len(s). The trim and replace operators only cut s there: ${x%?} must
// remove the last character, not the last byte of a multibyte one. An
// invalid byte counts as one character, as in the matcher.
func charBounds(s string) []int {
	b := make([]int, 0, len(s)+1)
	for i := 0; i < len(s); {
		b = append(b, i)
		_, n := utf8.DecodeRuneInString(s[i:])
		i += n
	}
	return append(b, len(s))
}

// wholeMatcher matches a pattern against whole strings. It first rules
// out candidates that can't match, so that the trim and replace
// operators, which try many substrings, stay linear for the usual
// patterns (${x//a/b}, ${x##*/}, ${x%?}) rather than running the
// matcher on every one.
type wholeMatcher struct {
	pat            string
	prefix, suffix string // literal text every match starts and ends with
	minLen         int    // fewest bytes in a match
	chars          int    // characters in every match, or -1 if pat has a *
}

func newWholeMatcher(pat string) *wholeMatcher {
	m := &wholeMatcher{pat: pat}
	// Split pat into one token per character it matches, as match()
	// reads it: kind '*', '?' (any one character) or 'l' (literal).
	type token struct {
		kind byte
		lit  string
	}
	var toks []token
	for i := 0; i < len(pat); {
		switch c := pat[i]; {
		case c == '*':
			toks = append(toks, token{kind: '*'})
			i++
			continue
		case c == '?':
			toks = append(toks, token{kind: '?'})
			i++
			continue
		case c == '[':
			if _, n := matchClass(pat[i:], 0, false); n > 0 {
				toks = append(toks, token{kind: '?'})
				i += n
				continue
			}
		case c == '\\' && i+1 < len(pat):
			i++
		}
		r, n := utf8.DecodeRuneInString(pat[i:])
		if r == utf8.RuneError {
			// match() takes any invalid byte for any other.
			toks = append(toks, token{kind: '?'})
		} else {
			toks = append(toks, token{kind: 'l', lit: pat[i : i+n]})
		}
		i += n
	}
	for _, t := range toks {
		switch t.kind {
		case '*':
			m.chars = -1
		case '?':
			m.minLen++
		default:
			m.minLen += len(t.lit)
		}
		if t.kind != '*' && m.chars >= 0 {
			m.chars++
		}
	}
	var b strings.Builder
	for _, t := range toks {
		if t.kind != 'l' {
			break
		}
		b.WriteString(t.lit)
	}
	m.prefix = b.String()
	k := len(toks)
	for k > 0 && toks[k-1].kind == 'l' {
		k--
	}
	b.Reset()
	for _, t := range toks[k:] {
		b.WriteString(t.lit)
	}
	m.suffix = b.String()
	return m
}

func (m *wholeMatcher) match(t string) bool {
	return len(t) >= m.minLen && strings.HasPrefix(t, m.prefix) && strings.HasSuffix(t, m.suffix) && matchPattern(m.pat, t)
}

// prefixLen returns how many characters of s (with character offsets
// b) the shortest or longest matching prefix has, or -1.
func (m *wholeMatcher) prefixLen(s string, b []int, long bool) int {
	if m.chars >= 0 {
		// Every match is m.chars characters long.
		if m.chars < len(b) && m.match(s[:b[m.chars]]) {
			return m.chars
		}
		return -1
	}
	if long {
		for k := len(b) - 1; k >= 0; k-- {
			if m.match(s[:b[k]]) {
				return k
			}
		}
		return -1
	}
	for k := range b {
		if m.match(s[:b[k]]) {
			return k
		}
	}
	return -1
}

// suffixStart returns the index in b where the shortest or longest
// matching suffix of s starts, or -1.
func (m *wholeMatcher) suffixStart(s string, b []int, long bool) int {
	if m.chars >= 0 {
		if k := len(b) - 1 - m.chars; k >= 0 && m.match(s[b[k]:]) {
			return k
		}
		return -1
	}
	if long {
		for k := range b {
			if m.match(s[b[k]:]) {
				return k
			}
		}
		return -1
	}
	for k := len(b) - 1; k >= 0; k-- {
		if m.match(s[b[k]:]) {
			return k
		}
	}
	return -1
}

func trimPrefixPattern(s, pat string, long bool) string {
	b := charBounds(s)
	if k := newWholeMatcher(pat).prefixLen(s, b, long); k >= 0 {
		return s[b[k]:]
	}
	return s
}

func trimSuffixPattern(s, pat string, long bool) string {
	b := charBounds(s)
	if k := newWholeMatcher(pat).suffixStart(s, b, long); k >= 0 {
		return s[:b[k]]
	}
	return s
}

func replacePattern(s, pat, rep string, op syntax.ParamOp) string {
	b := charBounds(s)
	m := newWholeMatcher(pat)
	switch op {
	case syntax.OpReplacePrefix:
		if k := m.prefixLen(s, b, true); k >= 0 {
			return rep + s[b[k]:]
		}
		return s
	case syntax.OpReplaceSuffix:
		if k := m.suffixStart(s, b, true); k >= 0 {
			return s[:b[k]] + rep
		}
		return s
	}
	var out strings.Builder
	replaced := false
	for k := 0; k < len(b); {
		i := b[k]
		if replaced && op != syntax.OpReplaceAll {
			out.WriteString(s[i:])
			return out.String()
		}
		// longest non-empty match starting at i
		found := -1
		switch {
		case m.chars >= 0:
			if e := k + m.chars; m.chars > 0 && e < len(b) && m.match(s[i:b[e]]) {
				found = e
			}
		case strings.HasPrefix(s[i:], m.prefix):
			for e := len(b) - 1; e > k; e-- {
				if m.match(s[i:b[e]]) {
					found = e
					break
				}
			}
		}
		if found > 0 {
			out.WriteString(rep)
			k = found
			replaced = true
			continue
		}
		if k == len(b)-1 {
			break
		}
		out.WriteString(s[i:b[k+1]])
		k++
	}
	return out.String()
}

// fishRange resolves the fish slice start..end over a list of n elements.
// It returns the 1-based positions to visit, from and to inclusive, and
// the direction; step 0 means the slice selects nothing. Negative indices
// count from the end. As in fish, when exactly one end is negative the
// signs fix the direction: 2..-1 only runs forward and -1..2 only
// backward, so $argv[2..-1] is empty when there is just one argument
// instead of reversing into the first one.
func fishRange(start, end, n int) (from, to, step int) {
	i, j := start, end
	if i < 0 {
		i += n + 1
	}
	if j < 0 {
		j += n + 1
	}
	if (start < 0) != (end < 0) {
		if start >= 0 && i > j || start < 0 && i < j {
			return 0, 0, 0
		}
	}
	step = 1
	lo, hi := i, j
	if j < i {
		step = -1
		lo, hi = j, i
	}
	// Positions outside 1..n select nothing; clamp to what exists.
	if lo < 1 {
		lo = 1
	}
	if hi > n {
		hi = n
	}
	if lo > hi {
		return 0, 0, 0
	}
	if step < 0 {
		return hi, lo, step
	}
	return lo, hi, step
}

// applyFishIndex selects elements with fish's 1-based indices and ranges.
func (sh *Shell) applyFishIndex(vals []string, idx *syntax.Index, io *IO) ([]string, error) {
	var out []string
	n := len(vals)
	for _, w := range idx.Words {
		specs, err := sh.expandFields([]*syntax.Word{w}, io, expNoGlob)
		if err != nil {
			return nil, err
		}
		for _, spec := range specs {
			for _, one := range strings.Fields(spec) {
				if i := strings.Index(one, ".."); i >= 0 {
					a, b := one[:i], one[i+2:]
					// A missing start is 1, a missing end is -1 (the last).
					start, end := 1, -1
					if a != "" {
						v, err := strconv.Atoi(a)
						if err != nil {
							return nil, errorf("invalid index %q", one)
						}
						start = v
					}
					if b != "" {
						v, err := strconv.Atoi(b)
						if err != nil {
							return nil, errorf("invalid index %q", one)
						}
						end = v
					}
					if start == 0 || end == 0 {
						return nil, errIndexZero
					}
					from, to, step := fishRange(start, end, n)
					for k := from; step != 0; k += step {
						out = append(out, vals[k-1])
						if k == to {
							break
						}
					}
					continue
				}
				v, err := strconv.Atoi(one)
				if err != nil {
					return nil, errorf("invalid index %q", one)
				}
				if v == 0 {
					return nil, errIndexZero
				}
				if v < 0 {
					v += n + 1
				}
				if v >= 1 && v <= n {
					out = append(out, vals[v-1])
				}
			}
		}
	}
	return out, nil
}

// errIndexZero is fish's error for $x[0]: indices count from 1, and a 0
// (often an off-by-one in a computed range like $x[1..$n]) must not
// quietly select something else.
var errIndexZero = errorf("array indices start at 1, not 0")
