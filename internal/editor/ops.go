package editor

import (
	"encoding/base64"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode"
)

func init() {
	functions = map[string]editFn{
		"beginning-of-line":              fnBeginningOfLine,
		"end-of-line":                    fnEndOfLine,
		"beginning-of-buffer":            func(e *Editor, k Key) { e.pos = 0 },
		"end-of-buffer":                  func(e *Editor, k Key) { e.pos = len(e.buf); e.update() },
		"backward-char":                  fnBackwardChar,
		"forward-char":                   fnForwardChar,
		"forward-single-char":            fnForwardChar,
		"backward-word":                  func(e *Editor, k Key) { e.pos = e.wordLeft(e.pos, isWordRune) },
		"forward-word":                   fnForwardWord,
		"backward-bigword":               func(e *Editor, k Key) { e.pos = e.wordLeft(e.pos, isBigWordRune) },
		"forward-bigword":                func(e *Editor, k Key) { e.pos = e.wordRight(e.pos, isBigWordRune) },
		"prevd-or-backward-word":         fnPrevdOrBackwardWord,
		"nextd-or-forward-word":          fnNextdOrForwardWord,
		"up-line":                        func(e *Editor, k Key) { e.moveLine(-1) },
		"down-line":                      func(e *Editor, k Key) { e.moveLine(1) },
		"delete-char":                    fnDeleteChar,
		"backward-delete-char":           fnBackwardDeleteChar,
		"delete-or-exit":                 fnDeleteOrExit,
		"exit":                           func(e *Editor, k Key) { e.finish("", ErrEOF) },
		"kill-line":                      fnKillLine,
		"backward-kill-line":             fnBackwardKillLine,
		"kill-whole-line":                fnKillWholeLine,
		"kill-word":                      func(e *Editor, k Key) { e.killTo(e.wordRight(e.pos, isWordRune)) },
		"backward-kill-word":             func(e *Editor, k Key) { e.killTo(e.wordLeft(e.pos, isWordRune)) },
		"kill-bigword":                   func(e *Editor, k Key) { e.killTo(e.wordRight(e.pos, isBigWordRune)) },
		"backward-kill-bigword":          func(e *Editor, k Key) { e.killTo(e.wordLeft(e.pos, isBigWordRune)) },
		"backward-kill-path-component":   func(e *Editor, k Key) { e.killTo(e.wordLeft(e.pos, isPathRune)) },
		"yank":                           fnYank,
		"yank-pop":                       fnYankPop,
		"transpose-chars":                fnTransposeChars,
		"transpose-words":                fnTransposeWords,
		"upcase-word":                    func(e *Editor, k Key) { e.mapWord(strings.ToUpper) },
		"downcase-word":                  func(e *Editor, k Key) { e.mapWord(strings.ToLower) },
		"capitalize-word":                func(e *Editor, k Key) { e.mapWord(capitalize) },
		"togglecase-char":                fnToggleCaseChar,
		"undo":                           fnUndo,
		"resume-job-or-undo":             fnResumeJobOrUndo,
		"redo":                           fnRedo,
		"up-or-search":                   fnUpOrSearch,
		"down-or-search":                 fnDownOrSearch,
		"history-search-backward":        func(e *Editor, k Key) { e.histStep(1) },
		"history-search-forward":         func(e *Editor, k Key) { e.histStep(-1) },
		"history-prefix-search-backward": func(e *Editor, k Key) { e.histStepPrefix(1) },
		"history-prefix-search-forward":  func(e *Editor, k Key) { e.histStepPrefix(-1) },
		"history-token-search-backward":  func(e *Editor, k Key) { e.tokenSearch(1) },
		"history-token-search-forward":   func(e *Editor, k Key) { e.tokenSearch(-1) },
		"beginning-of-history":           func(e *Editor, k Key) { e.histJump(true) },
		"end-of-history":                 func(e *Editor, k Key) { e.histJump(false) },
		"yank-last-arg":                  fnYankLastArg,
		"history-pager":                  fnHistoryPager,
		"complete":                       func(e *Editor, k Key) { e.complete(false) },
		"complete-and-search":            func(e *Editor, k Key) { e.complete(true) },
		"accept-autosuggestion":          fnAcceptSuggestion,
		"suppress-autosuggestion":        func(e *Editor, k Key) { e.sugg = nil; e.noSugg = true },
		"execute":                        fnExecute,
		"accept-line":                    fnExecute,
		"insert-newline":                 func(e *Editor, k Key) { e.insert([]rune{'\n'}) },
		"cancel-commandline":             fnCancelCommandline,
		"cancel":                         fnCancel,
		"clear-screen":                   func(e *Editor, k Key) { e.clearScreen() },
		"repaint":                        func(e *Editor, k Key) { e.prev = nil; e.requestPrompt() },
		"force-repaint":                  func(e *Editor, k Key) { e.prev = nil },
		"repaint-mode":                   func(e *Editor, k Key) { e.requestPrompt() },
		"edit-command-buffer":            fnEditCommandBuffer,
		"copy-commandline":               fnCopy,
		"paste-clipboard":                fnPaste,
		"prepend-sudo":                   fnPrependSudo,
		"man-page":                       func(e *Editor, k Key) { e.helpCommand("man") },
		"whatis":                         fnWhatis,
		"explain-line":                   fnExplainLine,
		"list-directory":                 fnListDirectory,
		"append-pager":                   func(e *Editor, k Key) { e.insertAtEnd(" | " + pagerCmd()) },
		"insert-space":                   func(e *Editor, k Key) { e.insert([]rune{' '}) },
		"quoted-insert":                  func(e *Editor, k Key) { e.quoted = true },
		"comment-line":                   fnCommentLine,
		"self-insert":                    func(e *Editor, k Key) { e.selfInsert(k.Rune) },
		"expand-abbr":                    func(e *Editor, k Key) { e.expandAbbr() },
		"vi-normal":                      fnViNormal,
		"vi-insert":                      func(e *Editor, k Key) { e.setViMode("insert") },
		"pager-toggle-search":            func(e *Editor, k Key) { e.complete(true) },
		"and":                            func(e *Editor, k Key) {},
		"or":                             func(e *Editor, k Key) {},
		"begin-undo-group":               func(e *Editor, k Key) {},
		"end-undo-group":                 func(e *Editor, k Key) {},
		"nothing":                        func(e *Editor, k Key) {},
	}
}

func isWordRune(r rune) bool    { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
func isBigWordRune(r rune) bool { return !unicode.IsSpace(r) }
func isPathRune(r rune) bool {
	return !unicode.IsSpace(r) && !strings.ContainsRune("/=:@{},'\"|;&<>()", r)
}

// wordLeft finds the start of the word before pos.
func (e *Editor) wordLeft(pos int, in func(rune) bool) int {
	for pos > 0 && !in(e.buf[pos-1]) {
		pos--
	}
	for pos > 0 && in(e.buf[pos-1]) {
		pos--
	}
	return pos
}

// wordRight finds the end of the word after pos.
func (e *Editor) wordRight(pos int, in func(rune) bool) int {
	for pos < len(e.buf) && !in(e.buf[pos]) {
		pos++
	}
	for pos < len(e.buf) && in(e.buf[pos]) {
		pos++
	}
	return pos
}

func (e *Editor) pushUndo() { e.pushUndoState(e.buf, e.pos) }

// pushUndoState records buf and pos as the state undo returns to.
func (e *Editor) pushUndoState(buf []rune, pos int) {
	cp := make([]rune, len(buf))
	copy(cp, buf)
	if n := len(e.undo); n > 0 && string(e.undo[n-1].buf) == string(cp) {
		return
	}
	e.undo = append(e.undo, snapshot{cp, pos})
	if len(e.undo) > 500 {
		e.undo = e.undo[len(e.undo)-500:]
	}
	e.redo = nil
}

func (e *Editor) edited() {
	e.hist.active = false
	e.hist.matchStart = -1
	e.lastArg.active = false
	e.noSugg = false
	e.update()
}

// insert puts text at the cursor.
func (e *Editor) insert(text []rune) {
	if e.lastCmd != "self-insert" || len(text) != 1 || text[0] == ' ' {
		e.pushUndo()
	}
	nb := make([]rune, 0, len(e.buf)+len(text))
	nb = append(nb, e.buf[:e.pos]...)
	nb = append(nb, text...)
	nb = append(nb, e.buf[e.pos:]...)
	e.buf = nb
	e.pos += len(text)
	e.edited()
}

func (e *Editor) insertAtEnd(s string) {
	e.pos = len(e.buf)
	e.insert([]rune(s))
}

func (e *Editor) selfInsert(r rune) {
	if e.keymap == "vi" && e.viMode == "replace" {
		e.pushUndo()
		if e.pos < len(e.buf) && e.buf[e.pos] != '\n' {
			e.buf[e.pos] = r
			e.pos++
		} else {
			e.buf = append(e.buf[:e.pos], append([]rune{r}, e.buf[e.pos:]...)...)
			e.pos++
		}
		e.edited()
		return
	}
	switch r {
	case ' ', ';', '|', '&':
		e.expandAbbr()
	}
	e.insert([]rune{r})
	e.lastCmd = "self-insert"
}

func (e *Editor) paste(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if e.pager != nil {
		e.pager.accept(e)
	}
	e.pushUndo()
	e.lastCmd = "paste"
	nb := make([]rune, 0, len(e.buf)+len(s))
	nb = append(nb, e.buf[:e.pos]...)
	ins := []rune(s)
	nb = append(nb, ins...)
	nb = append(nb, e.buf[e.pos:]...)
	e.buf = nb
	e.pos += len(ins)
	e.edited()
}

// deleteRange removes buf[a:b].
func (e *Editor) deleteRange(a, b int) {
	if a > b {
		a, b = b, a
	}
	if a < 0 {
		a = 0
	}
	if b > len(e.buf) {
		b = len(e.buf)
	}
	if a == b {
		return
	}
	e.pushUndo()
	e.buf = append(e.buf[:a:a], e.buf[b:]...)
	e.pos = a
	e.edited()
}

// killTo cuts from the cursor to pos into the kill ring.
func (e *Editor) killTo(pos int) {
	a, b := e.pos, pos
	if a > b {
		a, b = b, a
	}
	if a == b {
		return
	}
	text := string(e.buf[a:b])
	if strings.HasPrefix(e.lastCmd, "kill") || strings.HasPrefix(e.lastCmd, "backward-kill") {
		if pos < e.pos {
			e.kill.prepend(text)
		} else {
			e.kill.append(text)
		}
	} else {
		e.kill.push(text)
	}
	e.deleteRange(a, b)
}

type killRing struct {
	items []string
	idx   int
}

func (k *killRing) push(s string) {
	k.items = append(k.items, s)
	if len(k.items) > 50 {
		k.items = k.items[1:]
	}
	k.idx = len(k.items) - 1
}

func (k *killRing) append(s string) {
	if len(k.items) == 0 {
		k.push(s)
		return
	}
	k.items[len(k.items)-1] += s
	k.idx = len(k.items) - 1
}

func (k *killRing) prepend(s string) {
	if len(k.items) == 0 {
		k.push(s)
		return
	}
	k.items[len(k.items)-1] = s + k.items[len(k.items)-1]
	k.idx = len(k.items) - 1
}

func (k *killRing) current() string {
	if len(k.items) == 0 {
		return ""
	}
	return k.items[k.idx]
}

func fnBeginningOfLine(e *Editor, k Key) {
	for e.pos > 0 && e.buf[e.pos-1] != '\n' {
		e.pos--
	}
}

func fnEndOfLine(e *Editor, k Key) {
	if e.pos == len(e.buf) && len(e.sugg) > 0 {
		fnAcceptSuggestion(e, k)
		return
	}
	for e.pos < len(e.buf) && e.buf[e.pos] != '\n' {
		e.pos++
	}
	e.update()
}

// Moving and deleting by character skips the zero-width parts of a
// character: combining marks and the rest of an emoji sequence.

func fnBackwardChar(e *Editor, k Key) {
	if e.pos > 0 {
		ws := widths(e.buf)
		e.pos--
		for e.pos > 0 && ws[e.pos] == 0 {
			e.pos--
		}
	}
	e.sugg = nil
}

func fnForwardChar(e *Editor, k Key) {
	if e.pos == len(e.buf) {
		if len(e.sugg) > 0 {
			fnAcceptSuggestion(e, k)
		}
		return
	}
	ws := widths(e.buf)
	e.pos++
	for e.pos < len(e.buf) && ws[e.pos] == 0 {
		e.pos++
	}
	e.update()
}

func fnForwardWord(e *Editor, k Key) {
	if e.pos == len(e.buf) && len(e.sugg) > 0 {
		// accept one word of the suggestion
		s := e.sugg
		i := 0
		for i < len(s) && !isWordRune(s[i]) {
			i++
		}
		for i < len(s) && isWordRune(s[i]) {
			i++
		}
		e.insert(append([]rune(nil), s[:i]...))
		return
	}
	e.pos = e.wordRight(e.pos, isWordRune)
	e.update()
}

func fnPrevdOrBackwardWord(e *Editor, k Key) {
	if len(e.buf) == 0 && e.cfg.RunCommand != nil {
		e.runShell("prevd")
		return
	}
	e.pos = e.wordLeft(e.pos, isWordRune)
}

func fnNextdOrForwardWord(e *Editor, k Key) {
	if len(e.buf) == 0 && e.cfg.RunCommand != nil {
		e.runShell("nextd")
		return
	}
	fnForwardWord(e, k)
}

func fnAcceptSuggestion(e *Editor, k Key) {
	if len(e.sugg) == 0 {
		return
	}
	s := append([]rune(nil), e.sugg...)
	e.pos = len(e.buf)
	e.insert(s)
}

func fnDeleteChar(e *Editor, k Key) {
	if e.pos < len(e.buf) {
		ws := widths(e.buf)
		end := e.pos + 1
		for end < len(e.buf) && ws[end] == 0 {
			end++
		}
		p := e.pos
		e.deleteRange(e.pos, end)
		e.pos = p
	}
}

func fnBackwardDeleteChar(e *Editor, k Key) {
	if e.pos == 0 {
		return
	}
	ws := widths(e.buf)
	start := e.pos - 1
	for start > 0 && ws[start] == 0 {
		start--
	}
	e.deleteRange(start, e.pos)
}

func fnDeleteOrExit(e *Editor, k Key) {
	if len(e.buf) == 0 {
		e.finish("", ErrEOF)
		return
	}
	fnDeleteChar(e, k)
}

func fnKillLine(e *Editor, k Key) {
	end := e.pos
	for end < len(e.buf) && e.buf[end] != '\n' {
		end++
	}
	if end == e.pos && end < len(e.buf) {
		end++ // kill the newline itself
	}
	e.killTo(end)
}

func fnBackwardKillLine(e *Editor, k Key) {
	start := e.pos
	for start > 0 && e.buf[start-1] != '\n' {
		start--
	}
	if start == e.pos && start > 0 {
		start--
	}
	e.killTo(start)
}

func fnKillWholeLine(e *Editor, k Key) {
	start := e.pos
	for start > 0 && e.buf[start-1] != '\n' {
		start--
	}
	end := e.pos
	for end < len(e.buf) && e.buf[end] != '\n' {
		end++
	}
	if end < len(e.buf) {
		end++
	}
	e.pos = start
	e.killTo(end)
}

func fnYank(e *Editor, k Key) {
	s := e.kill.current()
	if s == "" {
		return
	}
	e.yankStart = e.pos
	e.insert([]rune(s))
	e.yankEnd = e.pos
	e.lastCmd = "yank"
}

func fnYankPop(e *Editor, k Key) {
	if (e.lastCmd != "yank" && e.lastCmd != "yank-pop") || len(e.kill.items) < 2 {
		return
	}
	e.kill.idx--
	if e.kill.idx < 0 {
		e.kill.idx = len(e.kill.items) - 1
	}
	e.deleteRange(e.yankStart, e.yankEnd)
	s := e.kill.current()
	e.insert([]rune(s))
	e.yankEnd = e.pos
	e.lastCmd = "yank-pop"
}

func fnTransposeChars(e *Editor, k Key) {
	if len(e.buf) < 2 {
		return
	}
	p := e.pos
	if p == len(e.buf) {
		p--
	}
	if p == 0 {
		return
	}
	e.pushUndo()
	e.buf[p-1], e.buf[p] = e.buf[p], e.buf[p-1]
	e.pos = p + 1
	e.edited()
}

func fnTransposeWords(e *Editor, k Key) {
	end2 := e.wordRight(e.pos, isWordRune)
	start2 := e.wordLeft(end2, isWordRune)
	start1 := e.wordLeft(start2, isWordRune)
	end1 := start1
	for end1 < len(e.buf) && isWordRune(e.buf[end1]) {
		end1++
	}
	if start1 >= start2 || end1 > start2 {
		return
	}
	e.pushUndo()
	w1 := append([]rune(nil), e.buf[start1:end1]...)
	mid := append([]rune(nil), e.buf[end1:start2]...)
	w2 := append([]rune(nil), e.buf[start2:end2]...)
	nb := append([]rune(nil), e.buf[:start1]...)
	nb = append(nb, w2...)
	nb = append(nb, mid...)
	nb = append(nb, w1...)
	nb = append(nb, e.buf[end2:]...)
	e.buf = nb
	e.pos = end2
	e.edited()
}

func capitalize(s string) string {
	rs := []rune(strings.ToLower(s))
	for i, r := range rs {
		if unicode.IsLetter(r) {
			rs[i] = unicode.ToUpper(r)
			break
		}
	}
	return string(rs)
}

func (e *Editor) mapWord(f func(string) string) {
	start := e.pos
	for start < len(e.buf) && !isWordRune(e.buf[start]) {
		start++
	}
	end := start
	for end < len(e.buf) && isWordRune(e.buf[end]) {
		end++
	}
	if start == end {
		return
	}
	e.pushUndo()
	w := []rune(f(string(e.buf[start:end])))
	nb := append(append(append([]rune(nil), e.buf[:start]...), w...), e.buf[end:]...)
	e.buf = nb
	e.pos = start + len(w)
	e.edited()
}

func fnToggleCaseChar(e *Editor, k Key) {
	if e.pos >= len(e.buf) {
		return
	}
	e.pushUndo()
	r := e.buf[e.pos]
	if unicode.IsUpper(r) {
		r = unicode.ToLower(r)
	} else {
		r = unicode.ToUpper(r)
	}
	e.buf[e.pos] = r
	if e.pos < len(e.buf)-1 {
		e.pos++
	}
	e.edited()
}

func fnUndo(e *Editor, k Key) {
	if len(e.undo) == 0 {
		return
	}
	cp := append([]rune(nil), e.buf...)
	e.redo = append(e.redo, snapshot{cp, e.pos})
	s := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.buf, e.pos = s.buf, s.pos
	if e.pos > len(e.buf) {
		e.pos = len(e.buf)
	}
	e.lastCmd = "undo"
	e.hist.active = false
	e.update()
}

func fnRedo(e *Editor, k Key) {
	if len(e.redo) == 0 {
		return
	}
	cp := append([]rune(nil), e.buf...)
	e.undo = append(e.undo, snapshot{cp, e.pos})
	s := e.redo[len(e.redo)-1]
	e.redo = e.redo[:len(e.redo)-1]
	e.buf, e.pos = s.buf, s.pos
	e.update()
}

// moveLine moves the cursor up or down a line in a multi-line buffer,
// keeping the column.
func (e *Editor) moveLine(dir int) bool {
	start := e.pos
	for start > 0 && e.buf[start-1] != '\n' {
		start--
	}
	col := e.pos - start
	if dir < 0 {
		if start == 0 {
			return false
		}
		prevEnd := start - 1
		prevStart := prevEnd
		for prevStart > 0 && e.buf[prevStart-1] != '\n' {
			prevStart--
		}
		e.pos = prevStart + min(col, prevEnd-prevStart)
		return true
	}
	end := e.pos
	for end < len(e.buf) && e.buf[end] != '\n' {
		end++
	}
	if end == len(e.buf) {
		return false
	}
	nextStart := end + 1
	nextEnd := nextStart
	for nextEnd < len(e.buf) && e.buf[nextEnd] != '\n' {
		nextEnd++
	}
	e.pos = nextStart + min(col, nextEnd-nextStart)
	return true
}

func fnUpOrSearch(e *Editor, k Key) {
	if !e.hist.active && e.moveLine(-1) {
		return
	}
	e.histStep(1)
}

func fnDownOrSearch(e *Editor, k Key) {
	if !e.hist.active && e.moveLine(1) {
		return
	}
	e.histStep(-1)
}

func fnExecute(e *Editor, k Key) {
	if e.pager != nil && e.pager.selected >= 0 {
		e.pager = nil
		e.update()
		return
	}
	e.pager = nil
	e.expandAbbr()
	line := string(e.buf)
	// (`read` takes any text: an apostrophe isn't an open quote there)
	if e.cfg.Incomplete != nil && !e.opts.Simple && strings.TrimSpace(line) != "" && e.cfg.Incomplete(line) {
		e.insert([]rune{'\n'})
		return
	}
	e.sugg = nil
	e.finish(line, nil)
}

func fnCancelCommandline(e *Editor, k Key) {
	if e.pager != nil {
		e.pager.cancel(e)
		return
	}
	e.sugg = nil
	e.finish("", ErrInterrupt)
}

func fnCancel(e *Editor, k Key) {
	if e.pager != nil {
		e.pager.cancel(e)
		return
	}
	if e.hist.active {
		e.buf = []rune(e.hist.saved)
		e.pos = len(e.buf)
		e.hist.active = false
		e.hist.matchStart = -1
		e.update()
		return
	}
	e.sugg = nil
	e.noSugg = true
}

func fnEditCommandBuffer(e *Editor, k Key) {
	if e.cfg.EditCommand == nil {
		return
	}
	e.suspend()
	s, ok := e.cfg.EditCommand(string(e.buf))
	e.resume()
	e.prev = nil
	if ok {
		e.pushUndo()
		e.buf = []rune(strings.TrimRight(s, "\n"))
		e.pos = len(e.buf)
		e.edited()
	}
}

func fnCopy(e *Editor, k Key) {
	if len(e.buf) == 0 {
		return
	}
	e.copyToClipboard(string(e.buf))
	e.message = e.style("pager_progress") + "copied to clipboard" + reset
}

func fnPaste(e *Editor, k Key) {
	var cmd *exec.Cmd
	switch {
	case hasCmd("pbpaste"):
		cmd = exec.Command("pbpaste")
	case os.Getenv("WAYLAND_DISPLAY") != "" && hasCmd("wl-paste"):
		cmd = exec.Command("wl-paste", "--no-newline")
	case hasCmd("xclip"):
		cmd = exec.Command("xclip", "-selection", "clipboard", "-o")
	case hasCmd("xsel"):
		cmd = exec.Command("xsel", "--clipboard", "--output")
	default:
		return
	}
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		e.paste(string(out))
	}
}

// copyToClipboard puts s on the clipboard: with pbcopy, wl-copy or xclip
// locally, and through the terminal (OSC 52) over SSH or when none of
// those exist, which also reaches the local clipboard from inside tmux.
func (e *Editor) copyToClipboard(s string) {
	if s == "" {
		return
	}
	if os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != "" {
		e.write("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\x07")
		return
	}
	var cmd *exec.Cmd
	switch {
	case hasCmd("pbcopy"):
		cmd = exec.Command("pbcopy")
	case os.Getenv("WAYLAND_DISPLAY") != "" && hasCmd("wl-copy"):
		cmd = exec.Command("wl-copy")
	case hasCmd("xclip"):
		cmd = exec.Command("xclip", "-selection", "clipboard")
	default:
		e.write("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\x07")
		return
	}
	cmd.Stdin = strings.NewReader(s)
	go cmd.Run()
}

var cmdCache = map[string]bool{}

func hasCmd(name string) bool {
	if v, ok := cmdCache[name]; ok {
		return v
	}
	_, err := exec.LookPath(name)
	cmdCache[name] = err == nil
	return err == nil
}

func fnPrependSudo(e *Editor, k Key) {
	if len(e.buf) == 0 {
		if e.history() != nil {
			if h := e.history().Search("", false); len(h) > 0 {
				e.buf = []rune(h[0])
			}
		}
	}
	s := string(e.buf)
	if strings.HasPrefix(s, "sudo ") {
		e.pushUndo()
		e.buf = e.buf[5:]
		if e.pos >= 5 {
			e.pos -= 5
		} else {
			e.pos = 0
		}
	} else {
		e.pushUndo()
		e.buf = append([]rune("sudo "), e.buf...)
		e.pos += 5
	}
	e.edited()
}

// commandAtCursor returns the command name of the process under the cursor.
func (e *Editor) commandAtCursor() string {
	s := string(e.buf[:e.pos])
	i := strings.LastIndexAny(s, "|;&\n(")
	rest := string(e.buf)[i+1:] // i is a byte offset, not a rune index
	fields := strings.Fields(rest)
	for _, f := range fields {
		if strings.Contains(f, "=") || f == "sudo" || f == "command" || f == "builtin" || f == "exec" || f == "time" || f == "env" {
			continue
		}
		return f
	}
	return ""
}

func (e *Editor) helpCommand(tool string) {
	cmd := e.commandAtCursor()
	if cmd == "" || e.cfg.RunCommand == nil {
		return
	}
	e.runShell(tool + " " + shellQuote(cmd))
}

func fnWhatis(e *Editor, k Key) {
	cmd := e.commandAtCursor()
	if cmd == "" {
		return
	}
	out, err := exec.Command("whatis", cmd).Output()
	if err != nil || len(out) == 0 {
		e.message = e.style("pager_progress") + cmd + ": nothing appropriate" + reset
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 5 {
		lines = lines[:5]
	}
	e.message = strings.Join(lines, "\n")
}

// fnResumeJobOrUndo brings back the last stopped job when the line is
// empty, so ctrl-z goes back and forth between an editor and the shell.
// Otherwise it undoes.
func fnResumeJobOrUndo(e *Editor, k Key) {
	if len(e.buf) == 0 && e.cfg.ResumeJob != nil {
		if cmd := e.cfg.ResumeJob(); cmd != "" {
			e.runShell(cmd)
			return
		}
	}
	fnUndo(e, k)
}

// fnExplainLine shows what the command and each of its options does,
// below the command line. Without an explanation it falls back to whatis.
func fnExplainLine(e *Editor, k Key) {
	if e.cfg.Explain == nil || strings.TrimSpace(string(e.buf)) == "" {
		fnWhatis(e, k)
		return
	}
	msg := strings.TrimRight(e.cfg.Explain(string(e.buf)), "\n")
	if msg == "" {
		fnWhatis(e, k)
		return
	}
	lines := strings.Split(msg, "\n")
	if max := e.rows / 2; max >= 3 && len(lines) > max {
		more := len(lines) - max + 1
		lines = append(lines[:max-1], e.style("pager_progress")+"… "+strconv.Itoa(more)+" more (the explain command shows everything)"+reset)
	}
	e.message = strings.Join(lines, "\n")
}

func fnListDirectory(e *Editor, k Key) {
	// list the directory under the cursor's token (or the current one)
	start := e.pos
	for start > 0 && !strings.ContainsRune(" \t\n|;&<>", e.buf[start-1]) {
		start--
	}
	tok := string(e.buf[start:e.pos])
	if e.cfg.RunCommand == nil {
		return
	}
	cmd := "ls"
	if tok != "" {
		cmd += " -- " + tok
	}
	e.runShell(cmd)
}

func fnCommentLine(e *Editor, k Key) {
	s := string(e.buf)
	e.pushUndo()
	if strings.HasPrefix(s, "#") {
		e.buf = []rune(strings.TrimPrefix(strings.TrimPrefix(s, "#"), " "))
	} else {
		e.buf = []rune("# " + s)
	}
	e.pos = len(e.buf)
	fnExecute(e, k)
}

func pagerCmd() string {
	if p := os.Getenv("PAGER"); p != "" {
		return p
	}
	return "less"
}

func fnYankLastArg(e *Editor, k Key) {
	if e.history() == nil {
		return
	}
	n := &e.lastArg
	if !n.active || e.lastCmd != "yank-last-arg" {
		n.active = true
		n.entries = e.history().Search("", false)
		n.idx = -1
		n.start = e.pos
		n.end = e.pos
	}
	for {
		n.idx++
		if n.idx >= len(n.entries) {
			n.idx = len(n.entries) - 1
			return
		}
		f := splitWords(n.entries[n.idx])
		if len(f) > 0 {
			arg := []rune(f[len(f)-1])
			e.pushUndo()
			nb := append(append(append([]rune(nil), e.buf[:n.start]...), arg...), e.buf[n.end:]...)
			e.buf = nb
			n.end = n.start + len(arg)
			e.pos = n.end
			e.update()
			e.lastCmd = "yank-last-arg"
			return
		}
	}
}

type lastArgNav struct {
	active     bool
	entries    []string
	idx        int
	start, end int
}

// splitWords splits a command line into words, keeping quotes intact.
func splitWords(s string) []string {
	var out []string
	var cur strings.Builder
	var q rune
	esc := false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && q != '\'':
			cur.WriteRune(r)
			esc = true
		case q != 0:
			cur.WriteRune(r)
			if r == q {
				q = 0
			}
		case r == '\'' || r == '"':
			cur.WriteRune(r)
			q = r
		case unicode.IsSpace(r) || strings.ContainsRune("|;&", r):
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// expandAbbr expands an abbreviation (or !!) before the cursor.
func (e *Editor) expandAbbr() {
	if e.cfg.ExpandAbbr == nil || e.opts.Simple {
		return
	}
	s := string(e.buf)
	cur := len(string(e.buf[:e.pos]))
	ns, ncur, ok := e.cfg.ExpandAbbr(s, cur)
	if !ok {
		return
	}
	e.pushUndo()
	e.buf = []rune(ns)
	if ncur > len(ns) {
		ncur = len(ns)
	}
	e.pos = len([]rune(ns[:ncur]))
	e.edited()
}
