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
		if a <= b {
			for i := a; i <= b && len(out) < 100000; i += step {
				out = append(out, format(i))
			}
		} else {
			for i := a; i >= b && len(out) < 100000; i -= step {
				out = append(out, format(i))
			}
		}
		return out
	}
	ca, cb := seq.Start[0], seq.End[0]
	if ca <= cb {
		for c := int(ca); c <= int(cb); c += step {
			out = append(out, string(rune(c)))
		}
	} else {
		for c := int(ca); c >= int(cb); c -= step {
			out = append(out, string(rune(c)))
		}
	}
	return out
}
