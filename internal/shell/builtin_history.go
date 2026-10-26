package shell

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// HistoryFilter is a History that remembers the directory and exit status
// of each command, for history --here and --failed.
type HistoryFilter interface {
	// Filter returns the commands whose last run keep accepts, newest
	// first, and at most n of them if n > 0.
	Filter(keep func(dir string, status int) bool, n int) []string
}

func bHistory(sh *Shell, io *IO, args []string) int {
	if sh.History == nil {
		fmt.Fprintln(io.Err, "history: not available in this shell")
		return 1
	}
	a := args[1:]
	sub := "search"
	if len(a) > 0 && !strings.HasPrefix(a[0], "-") {
		switch a[0] {
		case "browse":
			if b, ok := builtins["__ash_history_browse"]; ok {
				return b(sh, io, append([]string{"__ash_history_browse"}, a[1:]...))
			}
			fmt.Fprintln(io.Err, "history browse: not available")
			return 1
		case "search", "delete", "clear", "merge", "save", "import", "append", "clear-session":
			sub = a[0]
			a = a[1:]
		}
	}
	max := 0
	prefix, exact := false, false
	reverse := false
	here, failed := false, false
	var terms []string
	for i := 0; i < len(a); i++ {
		switch o := a[i]; {
		case o == "-n" || o == "--max":
			if i+1 < len(a) {
				max, _ = strconv.Atoi(a[i+1])
				i++
			}
		case strings.HasPrefix(o, "--max="):
			max, _ = strconv.Atoi(strings.TrimPrefix(o, "--max="))
		case o == "-p" || o == "--prefix":
			prefix = true
		case o == "-e" || o == "--exact":
			exact = true
		case o == "-c" || o == "--contains":
		case o == "-R" || o == "--reverse":
			reverse = true
		case o == "--here":
			here = true
		case o == "--failed":
			failed = true
		case o == "-C" || o == "--case-sensitive", o == "-z" || o == "--null", o == "-t" || o == "--show-time":
		case strings.HasPrefix(o, "-") && len(o) > 1 && isAllDigits(o[1:]):
			max, _ = strconv.Atoi(o[1:])
		default:
			terms = append(terms, o)
		}
	}
	if (here || failed) && sub != "search" {
		fmt.Fprintf(io.Err, "history %s: --here and --failed only work when listing commands\n", sub)
		return 2
	}
	switch sub {
	case "clear", "clear-session":
		sh.History.Clear()
		return 0
	case "delete":
		n := 0
		for _, t := range terms {
			n += sh.History.Delete(t)
		}
		if n == 0 {
			return 1
		}
		return 0
	case "merge":
		// pick up what other running shells have added
		if m, ok := sh.History.(interface{ Merge() }); ok {
			m.Merge()
		}
		return 0
	case "save":
		return 0 // every command is written as it runs
	case "import":
		kind := "fish"
		if len(terms) > 0 {
			kind = terms[0]
		}
		n, err := sh.History.Import(kind)
		if err != nil {
			fmt.Fprintf(io.Err, "history import: %v\n", err)
			return 1
		}
		fmt.Fprintf(io.Out, "imported %d entries from %s\n", n, kind)
		return 0
	}
	if len(terms) == 1 && isAllDigits(terms[0]) {
		// bash-style: history 20
		max, _ = strconv.Atoi(terms[0])
		terms = nil
	}
	var hf HistoryFilter
	var keep func(dir string, status int) bool
	if here || failed {
		var ok bool
		if hf, ok = sh.History.(HistoryFilter); !ok {
			fmt.Fprintln(io.Err, "history: this history doesn't know where commands ran or how they ended")
			return 1
		}
		cwd := sh.Dir()
		keep = func(dir string, status int) bool {
			return (!here || dir == cwd) && (!failed || status != 0)
		}
	}
	var entries []string
	if len(terms) == 0 {
		if keep != nil {
			entries = hf.Filter(keep, max)
		} else {
			entries = sh.History.Recent(max)
		}
	} else {
		var only map[string]bool
		if keep != nil {
			only = map[string]bool{}
			for _, e := range hf.Filter(keep, 0) {
				only[e] = true
			}
		}
		for _, t := range terms {
			for _, e := range sh.History.Search(t, prefix) {
				if exact && e != t || only != nil && !only[e] {
					continue
				}
				entries = append(entries, e)
			}
		}
		if max > 0 && len(entries) > max {
			entries = entries[:max]
		}
	}
	if reverse {
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	}
	w := bufio.NewWriter(io.Out)
	for _, e := range entries {
		w.WriteString(e)
		w.WriteByte('\n')
	}
	w.Flush()
	return 0
}
