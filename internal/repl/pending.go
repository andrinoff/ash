package repl

import (
	"strings"
	"sync"
	"time"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/shell"
)

// Temporary stand-ins for components that are being built separately
// (history store, highlighter, completion engine).

type historyBackend interface {
	shell.History
	editor.HistorySource
	Add(cmd, dir string, when time.Time, status int, dur time.Duration)
	Suggest(prefix, dir string) string
	Fuzzy(q string, limit int, home string) []editor.SearchResult
	Close()
}

type memHistory struct {
	mu      sync.Mutex
	entries []string // oldest first
}

func openHistory(sh *shell.Shell) historyBackend { return &memHistory{} }

func (h *memHistory) Add(cmd, dir string, when time.Time, status int, dur time.Duration) {
	if strings.HasPrefix(cmd, " ") {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, e := range h.entries {
		if e == cmd {
			h.entries = append(h.entries[:i], h.entries[i+1:]...)
			break
		}
	}
	h.entries = append(h.entries, cmd)
}

func (h *memHistory) Recent(n int) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for i := len(h.entries) - 1; i >= 0; i-- {
		out = append(out, h.entries[i])
		if n > 0 && len(out) >= n {
			break
		}
	}
	return out
}

func (h *memHistory) Search(q string, prefix bool) []string {
	var out []string
	for _, e := range h.Recent(0) {
		if prefix && strings.HasPrefix(e, q) || !prefix && strings.Contains(e, q) {
			out = append(out, e)
		}
	}
	return out
}

func (h *memHistory) Suggest(prefix, dir string) string {
	for _, e := range h.Recent(0) {
		if strings.HasPrefix(e, prefix) && len(e) > len(prefix) {
			return e
		}
	}
	return ""
}

func (h *memHistory) Fuzzy(q string, limit int, home string) []editor.SearchResult {
	var out []editor.SearchResult
	for _, e := range h.Search(q, false) {
		out = append(out, editor.SearchResult{Text: e})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (h *memHistory) Delete(cmd string) int { return 0 }
func (h *memHistory) Clear()                {}
func (h *memHistory) Import(kind string) (int, error) {
	return 0, nil
}
func (h *memHistory) Close() {}

func (r *REPL) highlight(buf string) []string { return nil }

func (r *REPL) complete(buf string, cursor int) editor.Completion { return editor.Completion{} }

func (r *REPL) suggestionOK(s string) bool { return true }
