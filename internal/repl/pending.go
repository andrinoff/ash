package repl

import (
	"path/filepath"
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
		info := relTime(m.When)
		if m.Dir != "" && where != nil {
			info += "  " + where(m.Dir)
		}
		if m.Status != 0 {
			info = "✗ " + info
		}
		out[i] = editor.SearchResult{Text: m.Cmd, Positions: m.Positions, Info: info}
	}
	return out
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

func (h *storeHistory) Close() {}

func (r *REPL) highlight(buf string) []string { return r.sh.Highlight(buf) }

func (r *REPL) complete(buf string, cursor int) editor.Completion { return r.sh.Complete(buf, cursor) }

func (r *REPL) suggestionOK(s string) bool { return r.sh.SuggestionOK(s) }
