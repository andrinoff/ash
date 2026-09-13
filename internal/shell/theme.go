package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Style is a text style for highlighting and set_color.
type Style struct {
	FG, BG                       Color
	Bold, Dim, Italic, Underline bool
	Reverse                      bool
	Set                          bool // style was specified (even if empty)
}

// Color is a terminal color: none, a 16-color index, a 256-color index or
// 24-bit RGB.
type Color struct {
	Kind    uint8 // 0 none, 1 basic(0-15), 2 rgb, 3 reset/normal
	Index   uint8
	R, G, B uint8
}

var namedColors = map[string]uint8{
	"black": 0, "red": 1, "green": 2, "yellow": 3, "blue": 4, "magenta": 5, "cyan": 6, "white": 7,
	"brblack": 8, "brred": 9, "brgreen": 10, "bryellow": 11, "brblue": 12, "brmagenta": 13, "brcyan": 14, "brwhite": 15,
	"grey": 8, "gray": 8, "purple": 5, "brpurple": 13, "brgrey": 7, "brgray": 7,
}

func parseColor(s string) (Color, bool) {
	s = strings.ToLower(strings.TrimPrefix(s, "#"))
	if s == "normal" || s == "reset" || s == "default" {
		return Color{Kind: 3}, true
	}
	if i, ok := namedColors[s]; ok {
		return Color{Kind: 1, Index: i}, true
	}
	if len(s) == 3 || len(s) == 6 {
		if len(s) == 3 {
			s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
		}
		if v, err := strconv.ParseUint(s, 16, 32); err == nil {
			return Color{Kind: 2, R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}, true
		}
	}
	return Color{}, false
}

// ParseStyle parses fish-style color specs like "ffffff --bold" or
// "brblue -u --background=333".
func ParseStyle(words []string) Style {
	st := Style{Set: true}
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "--bold" || w == "-o":
			st.Bold = true
		case w == "--dim" || w == "-d":
			st.Dim = true
		case w == "--italics" || w == "-i" || w == "--italic":
			st.Italic = true
		case w == "--underline" || w == "-u" || strings.HasPrefix(w, "--underline="):
			st.Underline = true
		case w == "--reverse" || w == "-r":
			st.Reverse = true
		case strings.HasPrefix(w, "--background="):
			if c, ok := parseColor(strings.TrimPrefix(w, "--background=")); ok {
				st.BG = c
			}
		case w == "--background" || w == "-b":
			if i+1 < len(words) {
				i++
				if c, ok := parseColor(words[i]); ok {
					st.BG = c
				}
			}
		case strings.HasPrefix(w, "-b") && len(w) > 2:
			if c, ok := parseColor(w[2:]); ok {
				st.BG = c
			}
		case strings.HasPrefix(w, "--theme="), strings.HasPrefix(w, "--underline-color="):
		case strings.HasPrefix(w, "-"):
		default:
			if st.FG.Kind == 0 {
				if c, ok := parseColor(w); ok {
					st.FG = c
				}
			}
		}
	}
	return st
}

var trueColor = detectTrueColor()

func detectTrueColor() bool {
	ct := strings.ToLower(os.Getenv("COLORTERM"))
	if ct == "truecolor" || ct == "24bit" {
		return true
	}
	switch os.Getenv("TERM_PROGRAM") {
	case "Apple_Terminal":
		return false
	}
	term := os.Getenv("TERM")
	if strings.Contains(term, "256color") || strings.Contains(term, "kitty") || strings.Contains(term, "ghostty") || strings.Contains(term, "wezterm") || strings.Contains(term, "alacritty") || strings.Contains(term, "foot") {
		return true
	}
	return term != "" && term != "linux" && term != "dumb"
}

// SGR returns the escape sequence that starts this style.
func (s Style) SGR() string {
	var parts []string
	if s.Bold {
		parts = append(parts, "1")
	}
	if s.Dim {
		parts = append(parts, "2")
	}
	if s.Italic {
		parts = append(parts, "3")
	}
	if s.Underline {
		parts = append(parts, "4")
	}
	if s.Reverse {
		parts = append(parts, "7")
	}
	parts = append(parts, s.FG.code(false)...)
	parts = append(parts, s.BG.code(true)...)
	if len(parts) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

func (c Color) code(bg bool) []string {
	switch c.Kind {
	case 1:
		base := 30
		if bg {
			base = 40
		}
		if c.Index >= 8 {
			base += 60
			return []string{strconv.Itoa(base + int(c.Index-8))}
		}
		return []string{strconv.Itoa(base + int(c.Index))}
	case 2:
		if trueColor {
			p := "38"
			if bg {
				p = "48"
			}
			return []string{p, "2", strconv.Itoa(int(c.R)), strconv.Itoa(int(c.G)), strconv.Itoa(int(c.B))}
		}
		p := "38"
		if bg {
			p = "48"
		}
		return []string{p, "5", strconv.Itoa(rgbTo256(c.R, c.G, c.B))}
	case 3:
		if bg {
			return []string{"49"}
		}
		return []string{"39"}
	}
	return nil
}

func rgbTo256(r, g, b uint8) int {
	if r == g && g == b {
		if r < 8 {
			return 16
		}
		if r > 248 {
			return 231
		}
		return 232 + int((float64(r)-8)/247*24)
	}
	q := func(v uint8) int { return int(float64(v)/255*5 + 0.5) }
	return 16 + 36*q(r) + 6*q(g) + q(b)
}

// Theme maps highlight roles (command, param, ...) to styles.
type Theme struct {
	mu     sync.RWMutex
	styles map[string]Style
}

// DefaultTheme is ash's built-in palette: calm, readable on dark and light.
func DefaultTheme() *Theme {
	t := &Theme{styles: map[string]Style{}}
	def := map[string]string{
		"normal":                      "normal",
		"command":                     "brblue",
		"keyword":                     "magenta",
		"builtin":                     "brblue",
		"function":                    "brblue",
		"param":                       "normal",
		"option":                      "cyan",
		"quote":                       "yellow",
		"redirection":                 "cyan --bold",
		"operator":                    "brcyan",
		"end":                         "green",
		"escape":                      "brcyan",
		"comment":                     "brblack --italics",
		"error":                       "red --underline",
		"autosuggestion":              "brblack",
		"valid_path":                  "--underline",
		"variable":                    "brcyan",
		"cancel":                      "--reverse",
		"selection":                   "white --bold --background=brblack",
		"search_match":                "--bold --background=brblack",
		"history_current":             "--bold",
		"pager_prefix":                "--bold --underline",
		"pager_completion":            "normal",
		"pager_description":           "brblack --italics",
		"pager_progress":              "brblack",
		"pager_selected_background":   "--reverse",
		"pager_selected_prefix":       "--bold --underline",
		"pager_selected_completion":   "--bold",
		"pager_selected_description":  "--italics",
		"pager_secondary_completion":  "normal",
		"pager_secondary_description": "brblack --italics",
	}
	for k, v := range def {
		t.styles[k] = ParseStyle(strings.Fields(v))
	}
	return t
}

// Style returns the style for a role, honoring $ash_color_<role> and
// $fish_color_<role> / $fish_pager_color_<role> variables.
func (sh *Shell) Style(role string) Style {
	for _, name := range styleVarNames(role) {
		if v, ok := sh.getVar(name); ok && len(v) > 0 {
			var words []string
			for _, x := range v {
				words = append(words, strings.Fields(x)...)
			}
			return ParseStyle(words)
		}
	}
	t := sh.shared.theme
	t.mu.RLock()
	defer t.mu.RUnlock()
	if s, ok := t.styles[role]; ok {
		return s
	}
	return Style{}
}

func styleVarNames(role string) []string {
	if strings.HasPrefix(role, "pager_") {
		r := strings.TrimPrefix(role, "pager_")
		return []string{"ash_pager_color_" + r, "fish_pager_color_" + r}
	}
	return []string{"ash_color_" + role, "fish_color_" + role}
}

func init() {
	builtins["set_color"] = bSetColor
	builtins["theme"] = bTheme
	builtinHelp["set_color"] = "print the escape sequence for a color"
	builtinHelp["theme"] = "load or list highlight themes"
}

func bSetColor(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	if len(a) > 0 && (a[0] == "-c" || a[0] == "--print-colors") {
		for _, n := range []string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white", "brblack", "brred", "brgreen", "bryellow", "brblue", "brmagenta", "brcyan", "brwhite", "normal"} {
			st := ParseStyle([]string{n})
			fmt.Fprintf(io.Out, "%s%s\x1b[0m\n", st.SGR(), n)
		}
		return 0
	}
	if len(a) == 0 {
		fmt.Fprintln(io.Err, "set_color: expected a color")
		return 2
	}
	st := ParseStyle(a)
	out := ""
	for _, w := range a {
		if w == "normal" || w == "reset" {
			out = "\x1b[0m"
		}
	}
	if st.FG.Kind == 3 {
		st.FG = Color{}
	}
	out += st.SGR()
	io.Out.WriteString(out)
	return 0
}

// themeDirs lists directories searched for .theme files.
func (sh *Shell) themeDirs() []string {
	home := sh.HomeDir()
	return []string{
		filepath.Join(sh.ConfigDir(), "themes"),
		filepath.Join(home, ".config", "fish", "themes"),
	}
}

func bTheme(sh *Shell, io *IO, args []string) int {
	if len(args) < 2 || args[1] == "list" {
		seen := map[string]bool{}
		for _, d := range sh.themeDirs() {
			ents, _ := os.ReadDir(d)
			for _, e := range ents {
				if n := strings.TrimSuffix(e.Name(), ".theme"); n != e.Name() && !seen[n] {
					seen[n] = true
					fmt.Fprintln(io.Out, n)
				}
			}
		}
		fmt.Fprintln(io.Out, "default")
		return 0
	}
	name := args[len(args)-1]
	if name == "default" {
		t := DefaultTheme()
		sh.shared.theme.mu.Lock()
		sh.shared.theme.styles = t.styles
		sh.shared.theme.mu.Unlock()
		return 0
	}
	path := name
	if !strings.ContainsRune(name, '/') {
		path = ""
		for _, d := range sh.themeDirs() {
			p := filepath.Join(d, name+".theme")
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}
	if path == "" {
		fmt.Fprintf(io.Err, "theme: no theme named '%s'\n", name)
		return 1
	}
	b, err := os.ReadFile(sh.absPath(path))
	if err != nil {
		fmt.Fprintf(io.Err, "theme: %s\n", errText(err))
		return 1
	}
	if err := sh.loadTheme(string(b)); err != nil {
		fmt.Fprintf(io.Err, "theme: %v\n", err)
		return 1
	}
	return 0
}

// loadTheme reads a fish .theme file: lines of "fish_color_x value...".
func (sh *Shell) loadTheme(src string) error {
	t := sh.shared.theme
	section := ""
	dark := true
	if v := os.Getenv("ASH_THEME_MODE"); v == "light" {
		dark = false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		if section == "light" && dark || section == "dark" && !dark {
			continue
		}
		fields := strings.Fields(line)
		name := fields[0]
		var role string
		switch {
		case strings.HasPrefix(name, "fish_pager_color_"):
			role = "pager_" + strings.TrimPrefix(name, "fish_pager_color_")
		case strings.HasPrefix(name, "fish_color_"):
			role = strings.TrimPrefix(name, "fish_color_")
		case strings.HasPrefix(name, "ash_pager_color_"):
			role = "pager_" + strings.TrimPrefix(name, "ash_pager_color_")
		case strings.HasPrefix(name, "ash_color_"):
			role = strings.TrimPrefix(name, "ash_color_")
		default:
			continue
		}
		t.styles[role] = ParseStyle(fields[1:])
	}
	return nil
}
