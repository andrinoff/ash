package editor

import (
	"strings"
	"unicode"
)

// viState is the state of vi normal/visual mode.
type viState struct {
	count       int
	op          rune // pending operator d, c, y
	opCount     int
	find        rune // pending f F t T
	lastFind    rune
	lastChar    rune
	replaceOne  bool
	g           bool
	textObj     rune // pending i or a after an operator
	visualStart int
}

func (e *Editor) setViMode(m string) {
	if m == "normal" {
		m = "default"
	}
	prev := e.viMode
	e.viMode = m
	if m == "default" && (prev == "insert" || prev == "replace") && e.pos > 0 && e.buf[e.pos-1] != '\n' {
		// like vi, leaving insert (or replace) mode steps back onto the
		// character before the cursor (but not onto the previous line)
		e.pos--
	}
	if m == "visual" {
		e.vi.visualStart = e.pos
	} else {
		e.vi.visualStart = -1
	}
	e.vi.op, e.vi.count, e.vi.find, e.vi.textObj, e.vi.g = 0, 0, 0, 0, false
	if m != "insert" {
		e.sugg = nil
	} else {
		e.update()
	}
	e.setCursorShape()
	if e.cfg.OnKeymap != nil {
		e.cfg.OnKeymap(e.keymapName())
	}
	e.requestPrompt()
}

func (e *Editor) setCursorShape() {
	if e.keymap != "vi" {
		return
	}
	switch e.viMode {
	case "insert":
		e.write("\x1b[6 q")
	case "replace":
		e.write("\x1b[4 q")
	default:
		e.write("\x1b[2 q")
	}
}

func (e *Editor) resetCursorShape() {
	if e.keymap == "vi" {
		e.write("\x1b[0 q")
	}
}

func (e *Editor) visualRange() (int, int) {
	if e.keymap != "vi" || e.viMode != "visual" || e.vi.visualStart < 0 {
		return -1, -1
	}
	a, b := e.vi.visualStart, e.pos
	if a > b {
		a, b = b, a
	}
	if b >= len(e.buf) {
		b = len(e.buf) - 1
	}
	return a, b
}

// clampNormal keeps the cursor on a character in normal mode.
func (e *Editor) clampNormal() {
	if e.viMode == "insert" {
		return
	}
	if e.pos >= len(e.buf) && len(e.buf) > 0 {
		e.pos = len(e.buf) - 1
	}
	if e.pos < 0 {
		e.pos = 0
	}
}

// viKey handles a key in normal, visual or replace mode.
func (e *Editor) viKey(k Key) bool {
	v := &e.vi
	if e.viMode == "replace" {
		switch {
		case k.Code == KeyEscape:
			e.setViMode("default")
			return true
		case k.Code == KeyBackspace:
			if e.pos > 0 {
				e.pos--
			}
			return true
		}
		return false
	}
	name := k.Name()
	// user bindings for this mode take precedence
	if _, ok := e.lookup(e.viMode, name); ok {
		return false
	}
	if k.Code == KeyEscape || name == "ctrl-[" {
		if e.viMode == "visual" {
			e.setViMode("default")
		}
		v.op, v.count, v.find, v.textObj, v.g, v.replaceOne = 0, 0, 0, 0, false, false
		return true
	}
	if k.Code == KeyEnter {
		e.setViMode("insert")
		fnExecute(e, k)
		return true
	}
	switch name {
	case "ctrl-c":
		fnCancelCommandline(e, k)
		return true
	case "ctrl-r":
		fnRedo(e, k)
		e.clampNormal()
		return true
	case "ctrl-d":
		fnDeleteOrExit(e, k)
		return true
	case "ctrl-l":
		e.clearScreen()
		return true
	}
	r := k.Rune
	switch k.Code {
	case KeyLeft:
		r = 'h'
	case KeyRight:
		r = 'l'
	case KeyUp:
		r = 'k'
	case KeyDown:
		r = 'j'
	case KeyHome:
		r = '0'
	case KeyEnd:
		r = '$'
	case KeyBackspace:
		r = 'h'
	case KeyDelete:
		r = 'x'
	case KeyNone:
	default:
		return true
	}
	if k.Mod&(ModCtrl|ModAlt) != 0 {
		return false
	}

	// pending character arguments
	if v.replaceOne {
		v.replaceOne = false
		n := max(v.count, 1)
		v.count = 0
		if e.pos+n <= len(e.buf) {
			e.pushUndo()
			for i := 0; i < n; i++ {
				e.buf[e.pos+i] = r
			}
			e.pos += n - 1
			e.edited()
		}
		return true
	}
	if v.find != 0 {
		f := v.find
		v.find = 0
		v.lastFind, v.lastChar = f, r
		target, ok := e.findChar(f, r, max(v.count, 1), false)
		v.count = 0
		if ok {
			e.viApply(target, f == 'f' || f == 't')
		} else {
			v.op = 0
		}
		return true
	}
	if v.textObj != 0 {
		a, b, ok := e.textObject(v.textObj, r)
		v.textObj = 0
		if ok {
			e.viOperate(a, b)
		}
		v.op = 0
		return true
	}

	// counts
	if r >= '1' && r <= '9' || r == '0' && v.count > 0 {
		v.count = v.count*10 + int(r-'0')
		return true
	}
	count := max(v.count, 1)
	if v.op != 0 && v.opCount > 0 {
		// 2dw is d2w; 2d3w deletes six words
		count *= v.opCount
	}

	switch r {
	case 'g':
		if v.g {
			v.g = false
			v.count = 0
			e.viApply(0, false)
		} else {
			v.g = true
		}
		return true
	case 'G':
		v.count = 0
		e.viApply(max(len(e.buf)-1, 0), true)
		return true
	}
	v.g = false

	// operators
	if (r == 'd' || r == 'c' || r == 'y') && e.viMode == "default" {
		if v.op == r {
			// dd, cc, yy: whole line
			v.op = 0
			v.count = 0
			start, end := e.lineBounds()
			switch r {
			case 'd':
				e.kill.push(string(e.buf[start:end]))
				// the line goes with its line break
				if end < len(e.buf) {
					end++
				} else if start > 0 {
					start--
				}
				e.deleteRange(start, end)
				e.pos, _ = e.lineBounds()
				for e.pos < len(e.buf) && (e.buf[e.pos] == ' ' || e.buf[e.pos] == '\t') {
					e.pos++
				}
			case 'c':
				e.kill.push(string(e.buf[start:end]))
				e.deleteRange(start, end)
				e.setViMode("insert")
			case 'y':
				e.kill.push(string(e.buf[start:end]))
			}
			e.clampNormal()
			return true
		}
		v.op = r
		v.opCount = v.count
		v.count = 0
		return true
	}
	if (r == 'i' || r == 'a') && v.op != 0 {
		v.textObj = r
		return true
	}
	if e.viMode == "visual" {
		switch r {
		case 'd', 'x':
			a, b := e.visualRange()
			e.kill.push(string(e.buf[a : b+1]))
			e.deleteRange(a, b+1)
			e.setViMode("default")
			e.clampNormal()
			return true
		case 'c', 's':
			a, b := e.visualRange()
			e.kill.push(string(e.buf[a : b+1]))
			e.deleteRange(a, b+1)
			e.setViMode("insert")
			return true
		case 'y':
			a, b := e.visualRange()
			e.kill.push(string(e.buf[a : b+1]))
			e.pos = a
			e.setViMode("default")
			return true
		case '~':
			a, b := e.visualRange()
			e.pushUndo()
			for i := a; i <= b; i++ {
				e.buf[i] = toggleCase(e.buf[i])
			}
			e.pos = a
			e.edited()
			e.setViMode("default")
			return true
		case 'v':
			e.setViMode("default")
			return true
		}
	}

	// motions
	if target, inclusive, ok := e.viMotion(r, count); ok {
		if r == 'f' || r == 'F' || r == 't' || r == 'T' {
			// waiting for the character; viMotion kept the count
			return true
		}
		v.count = 0
		e.viApply(target, inclusive)
		return true
	}

	v.count = 0
	switch r {
	case 'i':
		e.setViMode("insert")
	case 'a':
		if e.pos < len(e.buf) {
			e.pos++
		}
		e.setViMode("insert")
	case 'I':
		fnBeginningOfLine(e, k)
		for e.pos < len(e.buf) && (e.buf[e.pos] == ' ' || e.buf[e.pos] == '\t') {
			e.pos++
		}
		e.setViMode("insert")
	case 'A':
		_, e.pos = e.lineBounds()
		e.setViMode("insert")
	case 'x':
		end := min(e.pos+count, len(e.buf))
		if end > e.pos {
			e.kill.push(string(e.buf[e.pos:end]))
			e.deleteRange(e.pos, end)
		}
		e.clampNormal()
	case 'X':
		start := max(e.pos-count, 0)
		if start < e.pos {
			e.kill.push(string(e.buf[start:e.pos]))
			e.deleteRange(start, e.pos)
		}
	case 's':
		end := min(e.pos+count, len(e.buf))
		e.deleteRange(e.pos, end)
		e.setViMode("insert")
	case 'S':
		start, end := e.lineBounds()
		e.deleteRange(start, end)
		e.setViMode("insert")
	case 'D':
		_, end := e.lineBounds()
		e.killTo(end)
		e.clampNormal()
	case 'C':
		_, end := e.lineBounds()
		e.killTo(end)
		e.setViMode("insert")
	case 'Y':
		start, end := e.lineBounds()
		e.kill.push(string(e.buf[start:end]))
	case 'p':
		s := e.kill.current()
		if s != "" {
			if e.pos < len(e.buf) {
				e.pos++
			}
			for i := 0; i < count; i++ {
				e.insert([]rune(s))
			}
			e.pos--
			e.clampNormal()
		}
	case 'P':
		s := e.kill.current()
		if s != "" {
			for i := 0; i < count; i++ {
				e.insert([]rune(s))
			}
			e.pos--
			e.clampNormal()
		}
	case 'u':
		for i := 0; i < count; i++ {
			fnUndo(e, k)
		}
		e.clampNormal()
	case 'r':
		e.vi.replaceOne = true
		e.vi.count = count
		if count == 1 {
			e.vi.count = 0
		}
	case 'R':
		e.setViMode("replace")
	case '~':
		for i := 0; i < count; i++ {
			fnToggleCaseChar(e, k)
		}
	case 'v':
		e.setViMode("visual")
	case 'k':
		for i := 0; i < count; i++ {
			fnUpOrSearch(e, k)
		}
		e.clampNormal()
	case 'j':
		for i := 0; i < count; i++ {
			fnDownOrSearch(e, k)
		}
		e.clampNormal()
	case '/', '?':
		fnHistoryPager(e, k)
	case '#':
		fnCommentLine(e, k)
	}
	return true
}

func toggleCase(r rune) rune {
	if unicode.IsUpper(r) {
		return unicode.ToLower(r)
	}
	return unicode.ToUpper(r)
}

// lineBounds returns the start and end (after the newline, if any) of
// the line the cursor is on.
func (e *Editor) lineBounds() (int, int) {
	start := e.pos
	if start > len(e.buf) {
		start = len(e.buf)
	}
	for start > 0 && e.buf[start-1] != '\n' {
		start--
	}
	end := e.pos
	for end < len(e.buf) && e.buf[end] != '\n' {
		end++
	}
	return start, end
}

// viMotion computes the target of a motion key.
func (e *Editor) viMotion(r rune, count int) (int, bool, bool) {
	p := e.pos
	switch r {
	case 'h':
		for i := 0; i < count && p > 0 && e.buf[p-1] != '\n'; i++ {
			p--
		}
		return p, false, true
	case 'l', ' ':
		for i := 0; i < count && p < len(e.buf); i++ {
			p++
		}
		if e.vi.op == 0 && p >= len(e.buf) {
			p = max(len(e.buf)-1, 0)
		}
		return p, false, true
	case 'w', 'W':
		big := r == 'W'
		if e.vi.op == 'c' && p < len(e.buf) && viClass(e.buf[p], big) != 0 {
			// cw on a word changes to its end, not over the space after
			// it; on a blank it works like dw
			return viChangeWordEnd(e.buf, p, count, big), true, true
		}
		for i := 0; i < count; i++ {
			p = viNextWord(e.buf, p, big)
		}
		return p, false, true
	case 'e':
		for i := 0; i < count; i++ {
			p = viWordEnd(e.buf, p, false)
		}
		return p, true, true
	case 'E':
		for i := 0; i < count; i++ {
			p = viWordEnd(e.buf, p, true)
		}
		return p, true, true
	case 'b':
		for i := 0; i < count; i++ {
			p = viPrevWord(e.buf, p, false)
		}
		return p, false, true
	case 'B':
		for i := 0; i < count; i++ {
			p = viPrevWord(e.buf, p, true)
		}
		return p, false, true
	case '0':
		s, _ := e.lineBounds()
		return s, false, true
	case '^':
		s, end := e.lineBounds()
		for s < end && (e.buf[s] == ' ' || e.buf[s] == '\t') {
			s++
		}
		return s, false, true
	case '$':
		_, end := e.lineBounds()
		if e.vi.op != 0 {
			return end, false, true
		}
		return max(end-1, 0), false, true
	case 'f', 'F', 't', 'T':
		e.vi.find = r
		e.vi.count = count
		return 0, false, true
	case ';', ',':
		if e.vi.lastFind == 0 {
			return 0, false, false
		}
		f := e.vi.lastFind
		if r == ',' {
			f = map[rune]rune{'f': 'F', 'F': 'f', 't': 'T', 'T': 't'}[f]
		}
		t, ok := e.findChar(f, e.vi.lastChar, count, true)
		if !ok {
			return p, false, true
		}
		return t, f == 'f' || f == 't', true
	}
	return 0, false, false
}

// findChar finds the target of f, F, t or T. repeat is set for ; and ,
// which skip a target right next to the cursor for t and T, so that
// repeating them doesn't get stuck.
func (e *Editor) findChar(f, c rune, count int, repeat bool) (int, bool) {
	p := e.pos
	found := 0
	switch f {
	case 'f', 't':
		for i := p + 1; i < len(e.buf); i++ {
			if repeat && f == 't' && i == p+1 && e.buf[i] == c && count == 1 {
				continue
			}
			if e.buf[i] == c {
				found++
				if found == count {
					if f == 't' {
						return i - 1, true
					}
					return i, true
				}
			}
		}
	case 'F', 'T':
		for i := p - 1; i >= 0; i-- {
			if repeat && f == 'T' && i == p-1 && e.buf[i] == c && count == 1 {
				continue
			}
			if e.buf[i] == c {
				found++
				if found == count {
					if f == 'T' {
						return i + 1, true
					}
					return i, true
				}
			}
		}
	}
	return 0, false
}

// viApply moves to target or, with a pending operator, operates on the
// text between the cursor and target.
func (e *Editor) viApply(target int, inclusive bool) {
	if e.vi.op == 0 {
		e.pos = target
		e.clampNormal()
		return
	}
	a, b := e.pos, target
	if a > b {
		a, b = b, a
	} else if inclusive {
		b++
	}
	if b > len(e.buf) {
		b = len(e.buf)
	}
	e.viOperate(a, b)
}

func (e *Editor) viOperate(a, b int) {
	op := e.vi.op
	e.vi.op = 0
	if a >= b {
		if op == 'c' {
			e.setViMode("insert")
		}
		return
	}
	text := string(e.buf[a:b])
	switch op {
	case 'd':
		e.kill.push(text)
		e.deleteRange(a, b)
		e.clampNormal()
	case 'c':
		e.kill.push(text)
		e.deleteRange(a, b)
		e.setViMode("insert")
	case 'y':
		e.kill.push(text)
		e.pos = a
	}
}

// textObject returns the range of iw, aw, i", a(, ...
func (e *Editor) textObject(kind, obj rune) (int, int, bool) {
	p := e.pos
	if p >= len(e.buf) {
		p = len(e.buf) - 1
	}
	if p < 0 {
		return 0, 0, false
	}
	switch obj {
	case 'w', 'W':
		in := isWordRune
		if obj == 'W' {
			in = isBigWordRune
		}
		a, b := p, p
		if in(e.buf[p]) {
			for a > 0 && in(e.buf[a-1]) {
				a--
			}
			for b < len(e.buf) && in(e.buf[b]) {
				b++
			}
		} else {
			for a > 0 && !in(e.buf[a-1]) && e.buf[a-1] != '\n' {
				a--
			}
			for b < len(e.buf) && !in(e.buf[b]) && e.buf[b] != '\n' {
				b++
			}
		}
		if kind == 'a' {
			for b < len(e.buf) && e.buf[b] == ' ' {
				b++
			}
		}
		return a, b, true
	case '"', '\'', '`':
		a := strings.LastIndex(string(e.buf[:p+1]), string(obj))
		if a < 0 {
			return 0, 0, false
		}
		ra := len([]rune(string(e.buf[:p+1])[:a]))
		rb := -1
		for i := ra + 1; i < len(e.buf); i++ {
			if e.buf[i] == obj {
				rb = i
				break
			}
		}
		if rb < 0 {
			return 0, 0, false
		}
		if kind == 'i' {
			return ra + 1, rb, true
		}
		return ra, rb + 1, true
	case '(', ')', 'b', '[', ']', '{', '}', 'B', '<', '>':
		open, close := map[rune][2]rune{'(': {'(', ')'}, ')': {'(', ')'}, 'b': {'(', ')'}, '[': {'[', ']'}, ']': {'[', ']'}, '{': {'{', '}'}, '}': {'{', '}'}, 'B': {'{', '}'}, '<': {'<', '>'}, '>': {'<', '>'}}[obj][0], map[rune][2]rune{'(': {'(', ')'}, ')': {'(', ')'}, 'b': {'(', ')'}, '[': {'[', ']'}, ']': {'[', ']'}, '{': {'{', '}'}, '}': {'{', '}'}, 'B': {'{', '}'}, '<': {'<', '>'}, '>': {'<', '>'}}[obj][1]
		depth := 0
		a := -1
		for i := p; i >= 0; i-- {
			if e.buf[i] == close && i != p {
				depth++
			} else if e.buf[i] == open {
				if depth == 0 {
					a = i
					break
				}
				depth--
			}
		}
		if a < 0 {
			return 0, 0, false
		}
		depth = 0
		b := -1
		for i := a + 1; i < len(e.buf); i++ {
			if e.buf[i] == open {
				depth++
			} else if e.buf[i] == close {
				if depth == 0 {
					b = i
					break
				}
				depth--
			}
		}
		if b < 0 {
			return 0, 0, false
		}
		if kind == 'i' {
			return a + 1, b, true
		}
		return a, b + 1, true
	}
	return 0, 0, false
}

func viClass(r rune, big bool) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case big || isWordRune(r):
		return 1
	}
	return 2
}

func viNextWord(buf []rune, p int, big bool) int {
	if p >= len(buf) {
		return p
	}
	c := viClass(buf[p], big)
	for p < len(buf) && viClass(buf[p], big) == c && c != 0 {
		p++
	}
	for p < len(buf) && viClass(buf[p], big) == 0 {
		p++
	}
	return p
}

func viWordEnd(buf []rune, p int, big bool) int {
	if p+1 >= len(buf) {
		return max(len(buf)-1, 0)
	}
	p++
	for p < len(buf) && viClass(buf[p], big) == 0 {
		p++
	}
	if p >= len(buf) {
		return len(buf) - 1
	}
	c := viClass(buf[p], big)
	for p+1 < len(buf) && viClass(buf[p+1], big) == c {
		p++
	}
	return p
}

// viChangeWordEnd is the last character cw changes from p (on a word):
// the end of that word, even when p is already on its last character,
// then the ends of the next count-1 words.
func viChangeWordEnd(buf []rune, p, count int, big bool) int {
	c := viClass(buf[p], big)
	for p+1 < len(buf) && viClass(buf[p+1], big) == c {
		p++
	}
	for i := 1; i < count; i++ {
		p = viWordEnd(buf, p, big)
	}
	return p
}

func viPrevWord(buf []rune, p int, big bool) int {
	if p <= 0 {
		return 0
	}
	p--
	for p > 0 && viClass(buf[p], big) == 0 {
		p--
	}
	c := viClass(buf[p], big)
	for p > 0 && viClass(buf[p-1], big) == c {
		p--
	}
	return p
}
