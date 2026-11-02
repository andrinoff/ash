package editor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLayoutPreview(t *testing.T) {
	e := newTestEditor(40)
	e.buf = []rune("ls *.go")
	e.pos = len(e.buf)
	e.pv.text = "3 matches: a.go b.go c.go"
	e.message = "copied to clipboard"
	f := e.layout(Prompt{Left: "$ "}, false)
	want := []string{"$ ls *.go", "3 matches: a.go b.go c.go", "copied to clipboard"}
	if got := plainLines(f); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", got, want)
	}
	if f.row != 0 || f.col != 9 {
		t.Fatalf("cursor at %d,%d", f.row, f.col)
	}
	// dim like the autosuggestion unless a preview style is set
	if !strings.HasPrefix(f.lines[1], "\x1b[90m") {
		t.Errorf("preview style: %q", f.lines[1])
	}
	e.cfg.Style = func(role string) string {
		if role == "preview" {
			return "\x1b[3m"
		}
		return ""
	}
	if f := e.layout(Prompt{Left: "$ "}, false); !strings.HasPrefix(f.lines[1], "\x1b[3m") {
		t.Errorf("preview role: %q", f.lines[1])
	}
	e.cfg.Style = nil

	// not in the accepted line
	if got := plainLines(e.layout(Prompt{Left: "$ "}, true)); len(got) != 1 {
		t.Errorf("final render: %q", got)
	}
	// not while the pager or the history search is open
	e.message = ""
	e.pager = &pager{comp: Completion{Items: []Candidate{{Text: "a.go"}, {Text: "b.go"}}}, selected: -1}
	for _, l := range plainLines(e.layout(Prompt{Left: "$ "}, false)) {
		if strings.Contains(l, "3 matches") {
			t.Errorf("preview shown with the pager: %q", l)
		}
	}
	e.pager = nil
	e.search = &histSearch{}
	for _, l := range plainLines(e.layout(Prompt{Left: "$ "}, false)) {
		if strings.Contains(l, "3 matches") {
			t.Errorf("preview shown with the search: %q", l)
		}
	}
	e.search = nil
	// not for read
	e.opts.Simple = true
	if got := plainLines(e.layout(Prompt{Left: "$ "}, false)); len(got) != 1 {
		t.Errorf("simple mode: %q", got)
	}
	e.opts.Simple = false

	// a multi-line command: under its last line
	e.buf = []rune("for f in *.go\necho $f")
	e.pos = 12
	e.opts.Continuation = ". "
	got := plainLines(e.layout(Prompt{Left: "$ "}, false))
	if len(got) != 3 || got[2] != "3 matches: a.go b.go c.go" {
		t.Errorf("multi-line: %q", got)
	}
}

func TestLayoutPreviewCut(t *testing.T) {
	e := newTestEditor(20)
	e.buf = []rune("echo $x")
	e.pos = len(e.buf)
	e.pv.text = "a long value that does not fit"
	l := plainLines(e.layout(Prompt{Left: "$ "}, false))[1]
	if l != "a long value that d…" || plainWidth(l) != 20 {
		t.Errorf("cut: %q (%d)", l, plainWidth(l))
	}
	// wide runes don't push it past the edge
	e.pv.text = strings.Repeat("漢", 15)
	if l := plainLines(e.layout(Prompt{Left: "$ "}, false))[1]; plainWidth(l) > 20 || !strings.HasSuffix(l, "…") {
		t.Errorf("wide: %q (%d)", l, plainWidth(l))
	}
	// control characters can't reach the terminal
	e.pv.text = "a\x1b[2Jb\tc"
	line := e.layout(Prompt{Left: "$ "}, false).lines[1]
	if strings.Contains(line, "\x1b[2J") || !strings.Contains(stripANSI(line), "a^[[2Jb    c") {
		t.Errorf("control characters: %q", line)
	}
}

// previewHook is a Preview hook that records its calls.
type previewHook struct {
	mu    sync.Mutex
	calls []string
	block chan struct{} // if set, calls wait for it
}

func (h *previewHook) preview(buf string, cursor int) string {
	if h.block != nil {
		<-h.block
	}
	h.mu.Lock()
	h.calls = append(h.calls, buf[:cursor])
	h.mu.Unlock()
	if strings.HasSuffix(buf[:cursor], "boom") {
		panic("boom")
	}
	return "preview of " + buf[:cursor]
}

func (h *previewHook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

// deliver runs the next function sent to the editor's async channel.
func deliver(t *testing.T, e *Editor) {
	t.Helper()
	select {
	case f := <-e.async:
		f()
	case <-time.After(5 * time.Second):
		t.Fatal("no preview result")
	}
}

func TestPreviewScheduling(t *testing.T) {
	e := newTestEditor(80)
	e.async = make(chan func(), 64)
	h := &previewHook{}
	e.cfg.Preview = h.preview
	set := func(s string, pos int) {
		e.buf, e.pos = []rune(s), pos
		e.previewCheck()
	}

	set("ls *.go", 7)
	if !e.pv.armed || e.pv.text != "" {
		t.Fatalf("after an edit: armed %v text %q", e.pv.armed, e.pv.text)
	}
	e.runPreview()
	deliver(t, e)
	if e.pv.text != "preview of ls *.go" {
		t.Fatalf("text %q", e.pv.text)
	}
	// rendering again without a change computes nothing new
	e.pv.armed = false
	e.previewCheck()
	if e.pv.armed {
		t.Error("rescheduled without a change")
	}

	// editing the word keeps the old preview until the new one is in
	set("ls *.g", 6)
	if e.pv.text != "preview of ls *.go" {
		t.Errorf("flicker: %q", e.pv.text)
	}
	e.runPreview()
	deliver(t, e)
	if e.pv.text != "preview of ls *.g" {
		t.Errorf("text %q", e.pv.text)
	}

	// a word with nothing to expand clears it at once
	set("ls *.g -la", 10)
	if e.pv.text != "" {
		t.Errorf("not cleared: %q", e.pv.text)
	}
	set("ls *.g -la", 3)
	e.runPreview()
	deliver(t, e)

	// a result for an older buffer is dropped, and the newer one computed
	h.block = make(chan struct{})
	set("echo $x", 7)
	e.runPreview()
	set("echo $xy", 8)
	e.runPreview() // the first call is still running: queued
	if !e.pv.queued {
		t.Fatal("second call not queued")
	}
	before := e.pv.text
	close(h.block)
	deliver(t, e)
	if e.pv.text != before {
		t.Errorf("stale result shown: %q", e.pv.text)
	}
	deliver(t, e)
	if e.pv.text != "preview of echo $xy" {
		t.Errorf("queued call: %q", e.pv.text)
	}
	h.block = nil

	// a hook that panics shows nothing and doesn't stop later previews
	set("echo boom", 9)
	e.runPreview()
	deliver(t, e)
	if e.pv.text != "" || e.pv.busy {
		t.Errorf("after panic: %q busy %v", e.pv.text, e.pv.busy)
	}
	set("echo $boomx", 11)
	e.runPreview()
	deliver(t, e)
	if e.pv.text != "preview of echo $boomx" {
		t.Errorf("after panic: %q", e.pv.text)
	}

	// a new line starts empty; a call still running is dropped
	h.block = make(chan struct{})
	set("ls $x", 5)
	e.runPreview()
	e.previewReset()
	close(h.block)
	deliver(t, e)
	if e.pv.text != "" || e.pv.armed {
		t.Errorf("after reset: %q armed %v", e.pv.text, e.pv.armed)
	}
	h.block = nil

	// read doesn't preview
	n := h.count()
	e.opts.Simple = true
	set("ls *.go", 7)
	e.runPreview()
	if e.pv.armed || h.count() != n {
		t.Error("preview in simple mode")
	}
}

// TestPreviewOtherWord: the old text stays up only while the same word is
// edited. On another word, or another line from the history, it would be
// about the wrong thing.
func TestPreviewOtherWord(t *testing.T) {
	e := newTestEditor(80)
	e.async = make(chan func(), 64)
	h := &previewHook{}
	e.cfg.Preview = h.preview
	set := func(s string, pos int) {
		e.buf, e.pos = []rune(s), pos
		e.previewCheck()
	}
	show := func(s string, pos int) {
		t.Helper()
		set(s, pos)
		e.runPreview()
		deliver(t, e)
		if want := "preview of " + s[:pos]; e.pv.text != want {
			t.Fatalf("text %q want %q", e.pv.text, want)
		}
	}
	show("cp *.go $HOME", 7)
	if set("cp *.go $HOME", 5); e.pv.text == "" {
		t.Error("cleared while the cursor stayed in the word")
	}
	if set("cp *.go $HOME", 13); e.pv.text != "" {
		t.Errorf("kept on another word: %q", e.pv.text)
	}
	show("cp *.go $HOME", 13)
	if set("cp *.go $HOMEX", 14); e.pv.text == "" {
		t.Error("cleared while typing the word")
	}
	show("cp *.go $HOMEX", 14)
	// a line from the history whose last word starts at the same place
	if set("rm *.gz $HOMEX", 14); e.pv.text != "" {
		t.Errorf("kept on another line: %q", e.pv.text)
	}
	// a word that can't expand: cleared, and the hook isn't asked
	show("rm *.gz $HOMEX", 14)
	if set("rm *.gz plain", 13); e.pv.text != "" || e.pv.armed {
		t.Errorf("plain word: text %q armed %v", e.pv.text, e.pv.armed)
	}
}

// TestRunShellRefreshesPreview: shell code run by a key binding may change
// what the words expand to (alt-c cd's into another directory), so the
// preview is worked out again although the line stayed the same.
func TestRunShellRefreshesPreview(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	out, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	e := newTestEditor(80)
	e.in, e.out, e.fd = inR, out, int(inR.Fd())
	e.async = make(chan func(), 64)
	var calls atomic.Int32
	e.cfg.Preview = func(buf string, cursor int) string {
		return "call " + strconv.Itoa(int(calls.Add(1)))
	}
	ran := false
	e.cfg.RunCommand = func(string) { ran = true }

	e.buf, e.pos = []rune("ls *.go"), 7
	e.previewCheck()
	e.runPreview()
	deliver(t, e)
	e.pv.armed = false
	e.runShell("cd elsewhere")
	defer func() {
		if e.reader != nil {
			e.reader.stop()
		}
	}()
	if !ran {
		t.Fatal("the binding didn't run")
	}
	if e.previewCheck(); !e.pv.armed || e.pv.text != "call 1" {
		t.Fatalf("after the binding: armed %v text %q", e.pv.armed, e.pv.text)
	}
	e.runPreview()
	deliver(t, e)
	if e.pv.text != "call 2" {
		t.Errorf("text %q", e.pv.text)
	}
}

func TestWordMayExpand(t *testing.T) {
	cases := []struct {
		buf  string
		pos  int
		want bool
	}{
		{"ls *.go", 7, true},
		{"ls *.go", 3, true},
		{"ls *.go", 2, false},
		{"ls *.go -la", 10, false},
		{"ls *.go ", 8, false},
		{"echo $HOME", 10, true},
		{`echo "a $x b"`, 12, true},
		{`echo "a $x b" c`, 15, false},
		{"echo ${x:-a b}", 13, true},
		{"echo $x[1 2]", 11, true},
		{"echo $(ls *.go)", 11, true},
		{`echo 'a b' c`, 12, false},
		{"cd ~/src", 8, true},
		{"sudo !!", 7, true},
		{"echo plain", 10, false},
		{"", 0, false},
		{"ls  ", 3, false},
		{"echo \\$x", 8, true},
	}
	for _, c := range cases {
		if got := wordMayExpand([]rune(c.buf), c.pos); got != c.want {
			t.Errorf("wordMayExpand(%q, %d) = %v, want %v", c.buf, c.pos, got, c.want)
		}
	}
}

// BenchmarkPreviewCheck is the preview's cost per keystroke: noticing the
// change, the quick word check and resetting the timer.
func BenchmarkPreviewCheck(b *testing.B) {
	e := newTestEditor(80)
	e.cfg.Preview = func(string, int) string { return "" }
	e.buf = []rune("git commit -m 'a long message about the change' && ls *.go | grep -v _test " + strings.Repeat("arg ", 20))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		e.pos = len(e.buf) - i%2 // a cursor move every time
		e.previewCheck()
	}
}
