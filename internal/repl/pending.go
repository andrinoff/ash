package repl

import (
	"path/filepath"
	"time"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/history"
	"github.com/andrinoff/ash/internal/shell"
)

// Temporary stand-ins for components that are being built separately
// (highlighter, completion engine).

type historyBackend interface {
	shell.History
	editor.HistorySource
	Add(cmd, dir string, when time.Time, status int, dur time.Duration)
	Suggest(prefix, dir string) string
	Fuzzy(q string, limit int, home string) []editor.SearchResult
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

func (h *storeHistory) Fuzzy(q string, limit int, home string) []editor.SearchResult {
	ms := h.Store.Fuzzy(q, limit)
	out := make([]editor.SearchResult, len(ms))
	for i, m := range ms {
		info := relTime(m.When)
		if m.Dir != "" {
			info += "  " + shortDir(m.Dir, home)
		}
		if m.Status != 0 {
			info = "✗ " + info
		}
		out[i] = editor.SearchResult{Text: m.Cmd, Positions: m.Positions, Info: info}
	}
	return out
}

func (h *storeHistory) Close() {}

func (r *REPL) highlight(buf string) []string { return nil }

func (r *REPL) complete(buf string, cursor int) editor.Completion { return editor.Completion{} }

func (r *REPL) suggestionOK(s string) bool { return true }
