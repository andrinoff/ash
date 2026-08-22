package shell

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/andrinoff/ash/internal/syntax"
)

func (sh *Shell) expandParam(pe *syntax.ParamExp, io *IO, quoted bool) ([]string, valKind, error) {
	name := pe.Name
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
		return out, kindSplit, nil
	}

	vals, isSet := sh.getVar(name)
	kind := kindList
	if name == "@" || name == "*" {
		kind = kindSplit
		if name == "*" && quoted {
			kind = kindScalar
			ifs := " "
			if v, ok := sh.getVar("IFS"); ok {
				j := strings.Join(v, "")
				if j == "" {
					ifs = ""
				} else {
					ifs = j[:1]
				}
			}
			vals = []string{strings.Join(vals, ifs)}
		}
	} else if v, _ := sh.lookupVar(name); v != nil && v.Path {
		kind = kindPathList
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
			if pe.Index.Star && quoted {
				kind = kindScalar
				vals = []string{strings.Join(vals, " ")}
			} else {
				kind = kindSplit
			}
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
		return len(vals) == 1 && vals[0] == ""
	}

	switch pe.Op {
	case syntax.OpNone:
		if !isSet && sh.opts.nounset && name != "@" && name != "*" {
			return nil, kindScalar, errorf("%s: unbound variable", name)
		}
		return vals, kind, nil
	case syntax.OpDefault, syntax.OpDefaultUnset:
		if !isSet || pe.Op == syntax.OpDefault && isNull() {
			s, err := sh.expandString(pe.Arg, io)
			return []string{s}, kindScalar, err
		}
		return vals, kind, nil
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
		return vals, kind, nil
	case syntax.OpError, syntax.OpErrorUnset:
		if !isSet || pe.Op == syntax.OpError && isNull() {
			msg, _ := sh.expandString(pe.Arg, io)
			if msg == "" {
				msg = "parameter null or not set"
			}
			return nil, kindScalar, errorf("%s: %s", name, msg)
		}
		return vals, kind, nil
	case syntax.OpAlt, syntax.OpAltUnset:
		if !isSet || pe.Op == syntax.OpAlt && isNull() {
			return nil, kindScalar, nil
		}
		s, err := sh.expandString(pe.Arg, io)
		return []string{s}, kindScalar, err
	case syntax.OpTrimPrefix, syntax.OpTrimPrefixLong, syntax.OpTrimSuffix, syntax.OpTrimSuffixLong:
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
		return out, scalarIfOne(kind, out), nil
	case syntax.OpReplace, syntax.OpReplaceAll, syntax.OpReplacePrefix, syntax.OpReplaceSuffix:
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
		return out, scalarIfOne(kind, out), nil
	case syntax.OpSubstr:
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
			return sliceList(vals, off, length, hasLen, name == "@"), kindSplit, nil
		}
		return []string{substr(strings.Join(vals, " "), off, length, hasLen)}, kindScalar, nil
	case syntax.OpUpperFirst, syntax.OpUpperAll, syntax.OpLowerFirst, syntax.OpLowerAll:
		out := make([]string, len(vals))
		for i, v := range vals {
			out[i] = caseConvert(v, pe.Op)
		}
		return out, scalarIfOne(kind, out), nil
	case syntax.OpTransform:
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
		return out, scalarIfOne(kind, out), nil
	}
	return vals, kind, nil
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

func trimPrefixPattern(s, pat string, long bool) string {
	if long {
		for i := len(s); i >= 0; i-- {
			if matchPattern(pat, s[:i]) {
				return s[i:]
			}
		}
		return s
	}
	for i := 0; i <= len(s); i++ {
		if matchPattern(pat, s[:i]) {
			return s[i:]
		}
	}
	return s
}

func trimSuffixPattern(s, pat string, long bool) string {
	if long {
		for i := 0; i <= len(s); i++ {
			if matchPattern(pat, s[i:]) {
				return s[:i]
			}
		}
		return s
	}
	for i := len(s); i >= 0; i-- {
		if matchPattern(pat, s[i:]) {
			return s[:i]
		}
	}
	return s
}

func replacePattern(s, pat, rep string, op syntax.ParamOp) string {
	switch op {
	case syntax.OpReplacePrefix:
		for i := len(s); i >= 0; i-- {
			if matchPattern(pat, s[:i]) {
				return rep + s[i:]
			}
		}
		return s
	case syntax.OpReplaceSuffix:
		for i := 0; i <= len(s); i++ {
			if matchPattern(pat, s[i:]) {
				return s[:i] + rep
			}
		}
		return s
	}
	var b strings.Builder
	i := 0
	replaced := false
	for i <= len(s) {
		if replaced && op != syntax.OpReplaceAll {
			b.WriteString(s[i:])
			return b.String()
		}
		// longest match starting at i
		found := -1
		for j := len(s); j > i; j-- {
			if matchPattern(pat, s[i:j]) {
				found = j
				break
			}
		}
		if found > 0 {
			b.WriteString(rep)
			i = found
			replaced = true
			continue
		}
		if i == len(s) {
			break
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
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
					start, end := 1, n
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
					if start < 0 {
						start += n + 1
					}
					if end < 0 {
						end += n + 1
					}
					if n == 0 {
						continue
					}
					if start <= end {
						if start < 1 {
							start = 1
						}
						if end > n {
							end = n
						}
						for k := start; k <= end; k++ {
							out = append(out, vals[k-1])
						}
					} else {
						if start > n {
							start = n
						}
						if end < 1 {
							end = 1
						}
						for k := start; k >= end; k-- {
							out = append(out, vals[k-1])
						}
					}
					continue
				}
				v, err := strconv.Atoi(one)
				if err != nil {
					return nil, errorf("invalid index %q", one)
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
