package editor

import (
	"sort"
	"strings"
	"time"
	"unicode"
)

type binding struct {
	fns      []string // named editor functions
	cmd      string   // shell code
	setsMode string
	user     bool
}

type editFn func(e *Editor, k Key)

// functions maps fish-compatible editor function names to their code.
var functions map[string]editFn

// FunctionNames lists the named editor functions.
func FunctionNames() []string {
	out := make([]string, 0, len(functions))
	for n := range functions {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

var emacsKeys = map[string]string{
	"ctrl-a":          "beginning-of-line",
	"home":            "beginning-of-line",
	"ctrl-e":          "end-of-line",
	"end":             "end-of-line",
	"ctrl-b":          "backward-char",
	"left":            "backward-char",
	"ctrl-f":          "forward-char",
	"right":           "forward-char",
	"alt-b":           "backward-word",
	"ctrl-left":       "backward-word",
	"alt-left":        "prevd-or-backward-word",
	"alt-f":           "forward-word",
	"ctrl-right":      "forward-word",
	"alt-right":       "nextd-or-forward-word",
	"ctrl-d":          "delete-or-exit",
	"delete":          "delete-char",
	"backspace":       "backward-delete-char",
	"ctrl-h":          "backward-delete-char",
	"shift-backspace": "backward-delete-char",
	"ctrl-k":          "kill-line",
	"ctrl-u":          "backward-kill-line",
	"ctrl-w":          "backward-kill-path-component",
	"alt-backspace":   "backward-kill-word",
	"ctrl-backspace":  "backward-kill-word",
	"alt-d":           "kill-word",
	"ctrl-delete":     "kill-word",
	"alt-delete":      "kill-word",
	"ctrl-y":          "yank",
	"alt-y":           "yank-pop",
	"ctrl-t":          "transpose-chars",
	"alt-t":           "transpose-words",
	"alt-u":           "upcase-word",
	"alt-c":           "capitalize-word",
	"ctrl-_":          "undo",
	"ctrl-z":          "resume-job-or-undo",
	"alt-/":           "redo",
	"ctrl-shift-z":    "redo",
	"up":              "up-or-search",
	"down":            "down-or-search",
	"ctrl-p":          "up-or-search",
	"ctrl-n":          "down-or-search",
	"alt-up":          "history-token-search-backward",
	"alt-down":        "history-token-search-forward",
	"alt-.":           "yank-last-arg",
	"pageup":          "beginning-of-history",
	"pagedown":        "end-of-history",
	"tab":             "complete",
	"shift-tab":       "complete-and-search",
	"ctrl-r":          "history-pager",
	"ctrl-s":          "history-pager",
	"enter":           "execute",
	"alt-enter":       "insert-newline",
	"shift-enter":     "insert-newline",
	"ctrl-c":          "cancel-commandline",
	"ctrl-l":          "clear-screen",
	"alt-e":           "edit-command-buffer",
	"alt-v":           "edit-command-buffer",
	"ctrl-x,ctrl-e":   "edit-command-buffer",
	"ctrl-x":          "copy-commandline",
	"ctrl-v":          "paste-clipboard",
	"alt-s":           "prepend-sudo",
	"alt-h":           "man-page",
	"alt-w":           "explain-line",
	"alt-l":           "list-directory",
	"alt-p":           "append-pager",
	"ctrl-space":      "insert-space",
	"escape":          "cancel",
	"ctrl-g":          "cancel",
	"ctrl-q":          "quoted-insert",
	"alt-#":           "comment-line",
}

var viInsertKeys = map[string]string{
	"escape":     "vi-normal",
	"ctrl-[":     "vi-normal",
	"backspace":  "backward-delete-char",
	"ctrl-h":     "backward-delete-char",
	"delete":     "delete-char",
	"ctrl-w":     "backward-kill-path-component",
	"ctrl-u":     "backward-kill-line",
	"ctrl-a":     "beginning-of-line",
	"ctrl-e":     "end-of-line",
	"ctrl-f":     "forward-char",
	"ctrl-b":     "backward-char",
	"ctrl-k":     "kill-line",
	"ctrl-y":     "yank",
	"alt-f":      "forward-word",
	"alt-b":      "backward-word",
	"alt-.":      "yank-last-arg",
	"left":       "backward-char",
	"right":      "forward-char",
	"up":         "up-or-search",
	"down":       "down-or-search",
	"ctrl-p":     "up-or-search",
	"ctrl-n":     "down-or-search",
	"home":       "beginning-of-line",
	"end":        "end-of-line",
	"tab":        "complete",
	"shift-tab":  "complete-and-search",
	"ctrl-r":     "history-pager",
	"enter":      "execute",
	"alt-enter":  "insert-newline",
	"ctrl-c":     "cancel-commandline",
	"ctrl-d":     "delete-or-exit",
	"ctrl-l":     "clear-screen",
	"alt-e":      "edit-command-buffer",
	"alt-s":      "prepend-sudo",
	"ctrl-space": "insert-space",
}

func (e *Editor) defaultsFor(mode string) map[string]string {
	if e.keymap == "vi" {
		if mode == "insert" {
			return viInsertKeys
		}
		return nil
	}
	return emacsKeys
}

func (e *Editor) lookup(mode, seq string) (binding, bool) {
	if m := e.binds[mode]; m != nil {
		if b, ok := m[seq]; ok {
			return b, true
		}
	}
	if d := e.defaultsFor(mode); d != nil {
		if fn, ok := d[seq]; ok {
			return binding{fns: []string{fn}}, true
		}
	}
	return binding{}, false
}

func (e *Editor) hasLonger(mode, seq string) bool {
	prefix := seq + ","
	for k := range e.binds[mode] {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	for k := range e.defaultsFor(mode) {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// Bind binds a key sequence to editor functions or shell code.
func (e *Editor) Bind(mode, key, command, setsMode string) error {
	seq, err := ParseKeySpec(key)
	if err != nil {
		return err
	}
	if mode == "" {
		mode = "default"
	}
	b := binding{setsMode: setsMode, user: true}
	words := strings.Fields(command)
	allFns := len(words) > 0
	for _, w := range words {
		if _, ok := functions[w]; !ok {
			allFns = false
			break
		}
	}
	if allFns {
		b.fns = words
	} else {
		b.cmd = command
	}
	if e.binds[mode] == nil {
		e.binds[mode] = map[string]binding{}
	}
	e.binds[mode][strings.Join(seq, ",")] = b
	return nil
}

// Unbind removes a user binding.
func (e *Editor) Unbind(mode, key string) bool {
	seq, err := ParseKeySpec(key)
	if err != nil {
		return false
	}
	if mode == "" {
		mode = "default"
	}
	k := strings.Join(seq, ",")
	if _, ok := e.binds[mode][k]; ok {
		delete(e.binds[mode], k)
		return true
	}
	return false
}

// Bindings lists bindings in `bind` syntax.
func (e *Editor) Bindings() []string {
	var out []string
	add := func(mode, key, what string) {
		prefix := "bind "
		if mode != "default" {
			prefix += "-M " + mode + " "
		}
		out = append(out, prefix+key+" "+what)
	}
	modes := make([]string, 0, len(e.binds))
	for m := range e.binds {
		modes = append(modes, m)
	}
	sort.Strings(modes)
	for _, m := range modes {
		keys := make([]string, 0, len(e.binds[m]))
		for k := range e.binds[m] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b := e.binds[m][k]
			if b.cmd != "" {
				add(m, k, shellQuote(b.cmd))
			} else {
				add(m, k, strings.Join(b.fns, " "))
			}
		}
	}
	keys := make([]string, 0, len(emacsKeys))
	for k := range emacsKeys {
		if _, user := e.binds["default"][k]; !user {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		add("default", k, emacsKeys[k])
	}
	return out
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_./=:,+@", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// handleKey dispatches one key press.
func (e *Editor) handleKey(k Key) {
	e.message = ""
	if k.Paste != "" {
		if len(e.pendingKeys) > 0 {
			// keys typed before the paste come first
			e.flushChord()
		}
		if e.search != nil {
			// pasted into the search query, not the previewed line
			e.search.handle(e, k)
			return
		}
		e.paste(k.Paste)
		return
	}
	if e.quoted {
		e.quoted = false
		r := k.Rune
		if k.Raw != "" {
			for _, c := range k.Raw {
				e.insert([]rune{c})
			}
			return
		}
		e.insert([]rune{r})
		return
	}
	if e.search != nil {
		e.search.handle(e, k)
		return
	}
	if e.pager != nil && e.pager.handle(e, k) {
		return
	}
	if e.keymap == "vi" && e.viMode != "insert" && len(e.pendingKeys) == 0 {
		if e.viKey(k) {
			return
		}
	}
	seq := append(append([]Key(nil), e.pendingKeys...), k)
	key := seqName(seq)
	mode := e.keymapName()
	if e.hasLonger(mode, key) {
		e.pendingKeys = seq
		e.startChordTimer()
		return
	}
	if b, ok := e.lookup(mode, key); ok {
		e.pendingKeys = nil
		e.runBinding(b, k)
		return
	}
	if len(e.pendingKeys) > 0 {
		// The keys so far began a longer binding that this one doesn't
		// continue: they count as typed on their own.
		e.pendingKeys = nil
		e.runKeys(seq)
		return
	}
	e.unbound(k)
}

// unbound handles a key with no binding: printable keys insert themselves.
func (e *Editor) unbound(k Key) {
	if k.Code == KeyNone && k.Mod&(ModCtrl|ModAlt|ModSuper) == 0 && k.Rune >= ' ' {
		e.selfInsert(k.Rune)
		return
	}
	if k.Code == KeySpace && k.Mod == 0 {
		e.selfInsert(' ')
	}
}

// runKeys handles keys that didn't make up a longer binding: the longest
// bound sequence they start with runs (or else the first key on its own),
// then the keys after it are handled again.
func (e *Editor) runKeys(keys []Key) {
	mode := e.keymapName()
	n := 1
	b, ok := binding{}, false
	for i := len(keys); i > 0 && !ok; i-- {
		if b, ok = e.lookup(mode, seqName(keys[:i])); ok {
			n = i
		}
	}
	if ok {
		e.runBinding(b, keys[n-1])
	} else {
		e.unbound(keys[0])
	}
	for _, k := range keys[n:] {
		if e.done {
			return
		}
		e.handleKey(k)
	}
}

func seqName(keys []Key) string {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.Name()
	}
	return strings.Join(names, ",")
}

func (e *Editor) startChordTimer() {
	if e.chordTimer != nil {
		e.chordTimer.Stop()
	}
	e.chordTimer = time.NewTimer(500 * time.Millisecond)
}

// flushChord handles a pending key sequence after the chord timeout.
func (e *Editor) flushChord() {
	seq := e.pendingKeys
	e.pendingKeys = nil
	if len(seq) > 0 {
		e.runKeys(seq)
	}
}

func (e *Editor) runBinding(b binding, k Key) {
	for _, fn := range b.fns {
		e.call(fn, k)
		if e.done {
			return
		}
	}
	if b.cmd != "" {
		e.runShell(b.cmd)
	}
	if b.setsMode != "" {
		e.setViMode(b.setsMode)
	}
}

func (e *Editor) call(name string, k Key) {
	f, ok := functions[name]
	if !ok {
		return
	}
	f(e, k)
	e.lastCmd = name
}

// runShell runs shell code bound to a key with the terminal in normal
// mode, then redraws. If the command moved the cursor (it printed
// something or cleared the screen) the prompt is drawn fresh where the
// cursor is; otherwise it is repainted in place.
func (e *Editor) runShell(cmd string) {
	if e.cfg.RunCommand == nil {
		return
	}
	before, okBefore := e.queryCursor()
	e.suspend()
	e.cfg.RunCommand(cmd)
	e.resume()
	e.previewStale()
	after, okAfter := e.queryCursor()
	queued := e.queued
	e.queued = nil
	for _, fn := range queued {
		e.call(fn, Key{})
	}
	e.update()
	if okBefore && okAfter && before != after {
		// Something moved the cursor: start a new prompt where it is.
		if after.col > 1 {
			e.out.WriteString("\r\n")
		}
		e.cursorRow = 0
		e.marked = false
	}
	e.prev = nil
	e.requestPrompt()
}

type cursorPos struct{ row, col int }

// queryCursor asks the terminal where the cursor is (DSR/CPR). Typeahead
// read while waiting is kept.
func (e *Editor) queryCursor() (cursorPos, bool) {
	if e.reader == nil {
		return cursorPos{}, false
	}
	e.out.WriteString("\x1b[6n")
	deadline := time.After(150 * time.Millisecond)
	for {
		if pos, ok := e.extractCPR(); ok {
			return pos, true
		}
		e.reader.ask(1)
		select {
		case d, ok := <-e.reader.data:
			if !ok {
				return cursorPos{}, false
			}
			e.reader.asked = false
			e.input = append(e.input, d.b...)
		case <-deadline:
			return cursorPos{}, false
		}
	}
}

func (e *Editor) extractCPR() (cursorPos, bool) {
	for i := 0; i+1 < len(e.input); i++ {
		if e.input[i] != 0x1b || e.input[i+1] != '[' {
			continue
		}
		j := i + 2
		row, col := 0, 0
		semi := false
		for j < len(e.input) {
			c := e.input[j]
			switch {
			case c >= '0' && c <= '9':
				if semi {
					col = col*10 + int(c-'0')
				} else {
					row = row*10 + int(c-'0')
				}
			case c == ';' && !semi:
				semi = true
			case c == 'R' && semi:
				e.input = append(e.input[:i], e.input[j+1:]...)
				return cursorPos{row, col}, true
			default:
				j = len(e.input)
				continue
			}
			j++
		}
	}
	return cursorPos{}, false
}
