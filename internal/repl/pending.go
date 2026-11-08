package repl

import (
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/history"
	"github.com/andrinoff/ash/internal/shell"
)

// Adapters between the editor and the shell's components.

type historyBackend interface {
	shell.History
	editor.HistorySource
	Add(cmd, dir string, when time.Time, status int, dur time.Duration)
	Suggest(prefix, dir string) string
	Fuzzy(q string, limit int, keep func(history.Entry) bool, where func(dir string) string) []editor.SearchResult
	Close()
}

// storeHistory adapts the history package to what the REPL and the
// editor need.
type storeHistory struct {
	*history.Store
}

func openHistory(sh *shell.Shell) historyBackend {
	return &storeHistory{history.Open(filepath.Join(sh.DataDir(), "history"))}
}

func (h *storeHistory) Add(cmd, dir string, when time.Time, status int, dur time.Duration) {
	h.Store.Add(history.Entry{Cmd: cmd, When: when, Dir: dir, Status: status, Duration: dur})
}

// Fuzzy is the ctrl-r search over the commands keep accepts, or all of
// them if keep is nil. where shows the directory a command ran in next to
// it; nil leaves the directory out.
func (h *storeHistory) Fuzzy(q string, limit int, keep func(history.Entry) bool, where func(dir string) string) []editor.SearchResult {
	ms := h.Store.FuzzyFilter(q, limit, keep)
	out := make([]editor.SearchResult, len(ms))
	for i, m := range ms {
		r := editor.SearchResult{Text: m.Cmd, Positions: m.Positions, Age: relTime(m.When),
			Took: took(m.Duration), Failed: m.Status != 0, Status: m.Status}
		if m.Dir != "" && where != nil {
			r.Dir = where(m.Dir)
		}
		out[i] = r
	}
	return out
}

// took is how long a command ran, when that's a second or more.
func took(d time.Duration) string {
	switch {
	case d < time.Second:
		return ""
	case d < 10*time.Second:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	case d < time.Minute:
		return itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m" + fmt.Sprintf("%02ds", int(d.Seconds())%60)
	}
	return itoa(int(d.Hours())) + "h" + fmt.Sprintf("%02dm", int(d.Minutes())%60)
}

// EachRecent gives the history browser when, where and with what status
// each command ran.
func (h *storeHistory) EachRecent(f func(cmd string, when time.Time, dir string, status int) bool) {
	for _, e := range h.Store.Entries() {
		if !f(e.Cmd, e.When, e.Dir, e.Status) {
			return
		}
	}
}

// The history builtin's --here and --failed need Filter.
var _ shell.HistoryFilter = (*storeHistory)(nil)

// Filter returns the commands whose last run keep accepts, given the
// directory it ran in and its exit status, newest first, and at most n of
// them if n > 0.
func (h *storeHistory) Filter(keep func(dir string, status int) bool, n int) []string {
	ms := h.Store.FuzzyFilter("", n, func(e history.Entry) bool { return keep(e.Dir, e.Status) })
	out := make([]string, len(ms))
	for i := range ms {
		out[i] = ms[i].Cmd
	}
	return out
}

func (h *storeHistory) Close() {}

func (r *REPL) highlight(buf string) []string { return r.sh.Highlight(buf) }

func (r *REPL) complete(buf string, cursor int) editor.Completion { return r.sh.Complete(buf, cursor) }

func (r *REPL) suggestionOK(s string) bool { return r.sh.SuggestionOK(s) }
