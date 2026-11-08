package editor

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestCursorCode(t *testing.T) {
	for shape, want := range map[string]string{
		"block": "\x1b[2 q", "block blink": "\x1b[1 q",
		"underscore": "\x1b[4 q", "underscore blink": "\x1b[3 q",
		"line": "\x1b[6 q", "line blink": "\x1b[5 q", "bar": "\x1b[6 q",
		"": "", "triangle": "", "line slow": "", "line blink twice": "",
	} {
		if got := cursorCode(shape); got != want {
			t.Errorf("%q: %q, want %q", shape, got, want)
		}
	}
}

func TestSearchAndPagerRows(t *testing.T) {
	results := make([]SearchResult, 30)
	for i := range results {
		results[i] = SearchResult{Text: "cmd " + itoa(i)}
	}
	e := newTestEditor(60)
	e.cfg.SearchHistory = func(string, string, int) []SearchResult { return results }
	e.handleKey(keyCtrlR)
	if n := len(e.search.render(e, 60, 40)); n != 1+10+1 {
		t.Errorf("default: %d lines", n)
	}
	e.opts.SearchRows = 4
	if n := len(e.search.render(e, 60, 40)); n != 1+4+1 {
		t.Errorf("4 rows: %d lines", n)
	}

	p := &pager{comp: Completion{}, selected: -1}
	for i := 0; i < 30; i++ {
		p.comp.Items = append(p.comp.Items, Candidate{Text: "a-rather-long-candidate-" + itoa(i)})
	}
	e.rows = 80
	if n := len(p.render(e, 40, 40)); n != 12+1 {
		t.Errorf("pager default: %d lines", n)
	}
	e.opts.PagerRows = 5
	if n := len(p.render(e, 40, 40)); n != 5+1 {
		t.Errorf("pager with 5 rows: %d lines", n)
	}
}

// TestBottomShrink checks that with the prompt at the bottom, a frame
// that got shorter (a menu closed) moves down by the lines it gave up.
func TestBottomShrink(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEditor(40)
	e.out = w
	e.prev = []string{"prompt", "menu 1", "menu 2", "menu 3"}
	e.cursorRow = 0
	e.anchorBottom(1)
	w.Close()
	got, _ := io.ReadAll(r)
	if !strings.HasSuffix(string(got), "\r\x1b[J\x1b[3B") || e.prev != nil || e.cursorRow != 0 {
		t.Fatalf("wrote %q, prev %q", got, e.prev)
	}
}
