package editor

import "testing"

func searchEditor(hist fakeHist) *Editor {
	e := newTestEditor(80)
	e.cfg.History = hist
	e.cfg.SearchHistory = func(q string, n int) []SearchResult {
		var out []SearchResult
		for _, s := range hist.Search(q, false) {
			out = append(out, SearchResult{Text: s})
		}
		return out
	}
	return e
}

// Pasting while the history search is open adds to the query.
func TestPasteIntoHistorySearch(t *testing.T) {
	e := searchEditor(fakeHist{"git status", "echo hello world", "ls -la"})
	e.insert([]rune("ec"))
	e.call("history-pager", Key{})
	if e.search == nil {
		t.Fatal("search not open")
	}
	e.handleKey(Key{Paste: "ho\the"})
	if got := string(e.search.query); got != "echo he" {
		t.Fatalf("query %q", got)
	}
	if got := string(e.buf); got != "echo hello world" {
		t.Fatalf("preview %q", got)
	}
	e.handleKey(Key{Code: KeyEscape})
	if got := string(e.buf); got != "ec" {
		t.Fatalf("escape should restore the line: %q", got)
	}
}
