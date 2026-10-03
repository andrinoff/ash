package shell

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/andrinoff/ash/internal/syntax"
)

func braceText(be *syntax.BraceExp) string {
	if be.Seq != nil {
		s := "{" + be.Seq.Start + ".." + be.Seq.End
		if be.Seq.Step != "" {
			s += ".." + be.Seq.Step
		}
		return s + "}"
	}
	var alts []string
	for _, a := range be.Alts {
		var b strings.Builder
		for _, p := range a {
			switch p := p.(type) {
			case *syntax.Lit:
				b.WriteString(p.Value)
			case *syntax.Quoted:
				b.WriteString(p.Value)
			}
		}
		alts = append(alts, b.String())
	}
	return "{" + strings.Join(alts, ",") + "}"
}

// isLetterSeq reports whether seq is a letter range like {a..e}.
func isLetterSeq(seq *syntax.BraceSeq) bool {
	isLetter := func(s string) bool {
		return len(s) == 1 && (s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z')
	}
	return isLetter(seq.Start) && isLetter(seq.End)
}

// expandBraces is braceExpand for a whole word. A word that starts with
// ~ once its braces are expanded gets tilde expansion, as in bash and
// fish: ~{,/x} is ~ and ~/x (the parser can't see a tilde prefix in
// ~{...} itself).
func expandBraces(parts []syntax.WordPart) [][]syntax.WordPart {
	words := braceExpand(parts)
	if len(parts) == 0 {
		return words
	}
	if l, ok := parts[0].(*syntax.Lit); !ok || !strings.HasPrefix(l.Value, "~") {
		return words
	}
	for _, p := range parts {
		if _, ok := p.(*syntax.BraceExp); ok {
			for i, w := range words {
				words[i] = tildePrefix(w)
			}
			break
		}
	}
	return words
}

// tildePrefix turns a leading unquoted ~user (up to a / or the end of
// the word) into a Tilde part.
func tildePrefix(parts []syntax.WordPart) []syntax.WordPart {
	var b strings.Builder
	k := 0
	for ; k < len(parts); k++ {
		l, ok := parts[k].(*syntax.Lit)
		if !ok {
			break
		}
		b.WriteString(l.Value)
	}
	s := b.String()
	end := strings.IndexByte(s, '/')
	if end < 0 {
		if k < len(parts) {
			return parts // ~ followed by a quote or an expansion
		}
		end = len(s)
	}
	user := s[1:end]
	for i := 0; i < len(user); i++ {
		c := user[i]
		if !(c == '_' || c == '.' || c == '-' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') && !(user == "+") {
			return parts
		}
	}
	out := []syntax.WordPart{&syntax.Tilde{User: user}}
	if end < len(s) {
		out = append(out, &syntax.Lit{Value: s[end:]})
	}
	return append(out, parts[k:]...)
}

// braceExpand performs brace expansion, returning one part list per
// alternative.
func braceExpand(parts []syntax.WordPart) [][]syntax.WordPart {
	idx := -1
	for i, p := range parts {
		if _, ok := p.(*syntax.BraceExp); ok {
			idx = i
			break
		}
	}
	if idx < 0 {
		return [][]syntax.WordPart{parts}
	}
	be := parts[idx].(*syntax.BraceExp)
	prefix := parts[:idx]
	suffixes := braceExpand(parts[idx+1:])
	var alts [][]syntax.WordPart
	if be.Seq != nil {
		for _, s := range expandSeq(be.Seq) {
			alts = append(alts, []syntax.WordPart{&syntax.Lit{Value: s}})
		}
	} else {
		for _, a := range be.Alts {
			alts = append(alts, braceExpand(a)...)
		}
	}
	var out [][]syntax.WordPart
	for _, a := range alts {
		for _, s := range suffixes {
			np := make([]syntax.WordPart, 0, len(prefix)+len(a)+len(s))
			np = append(np, prefix...)
			np = append(np, a...)
			np = append(np, s...)
			out = append(out, np)
		}
	}
	return out
}

func expandSeq(seq *syntax.BraceSeq) []string {
	step := 1
	if seq.Step != "" {
		step, _ = strconv.Atoi(seq.Step)
		if step < 0 {
			step = -step
		}
		if step == 0 {
			step = 1
		}
	}
	a, errA := strconv.Atoi(seq.Start)
	b, errB := strconv.Atoi(seq.End)
	var out []string
	if (errA != nil || errB != nil) && !isLetterSeq(seq) {
		// A number that doesn't fit in 64 bits: leave the text alone
		// rather than reading {1..99999999999999999999} as {1..9}.
		return []string{braceText(&syntax.BraceExp{Seq: seq})}
	}
	if errA == nil && errB == nil {
		width := 0
		if (len(seq.Start) > 1 && strings.TrimLeft(seq.Start, "-")[0] == '0') || (len(seq.End) > 1 && strings.TrimLeft(seq.End, "-")[0] == '0') {
			width = len(seq.Start)
			if len(seq.End) > width {
				width = len(seq.End)
			}
		}
		format := func(n int) string {
			if width > 0 {
				return fmt.Sprintf("%0*d", width, n)
			}
			return strconv.Itoa(n)
		}
		// Stop before i += step can overflow: {9223372036854775806..
		// 9223372036854775807} wrapped around and produced junk. The
		// distance to the end is exact as a uint64.
		if a <= b {
			for i := a; len(out) < 100000; i += step {
				out = append(out, format(i))
				if uint64(b-i) < uint64(step) {
					break
				}
			}
		} else {
			for i := a; len(out) < 100000; i -= step {
				out = append(out, format(i))
				if uint64(i-b) < uint64(step) {
					break
				}
			}
		}
		return out
	}
	// Letters too: c += step overflowed for a huge step, so {a..e..
	// 9223372036854775807} gave junk and {a..e..-9223372036854775808}
	// (whose step stays negative) never ended.
	ca, cb := int(seq.Start[0]), int(seq.End[0])
	if ca <= cb {
		for c := ca; ; c += step {
			out = append(out, string(rune(c)))
			if uint64(cb-c) < uint64(step) {
				break
			}
		}
	} else {
		for c := ca; ; c -= step {
			out = append(out, string(rune(c)))
			if uint64(c-cb) < uint64(step) {
				break
			}
		}
	}
	return out
}
