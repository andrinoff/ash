// Package tui holds ash's full-screen pickers: choose, the directory
// picker and the history browser. They are Bubble Tea programs that take
// plain data and return the user's choice; the package knows nothing about
// the shell.
//
// Pickers draw on the controlling terminal (/dev/tty) rather than on
// stdin and stdout, so they work in pipelines like "ls | choose", and they
// leave the terminal exactly as they found it (see run).
package tui

import (
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// ErrCanceled is returned when the user dismisses a picker.
var ErrCanceled = errors.New("canceled")

// ErrNoTTY is returned when there is no terminal to draw on.
var ErrNoTTY = errors.New("no terminal")

// ErrNotForeground is returned when the process is not in the terminal's
// foreground process group, where reading the terminal would stop it.
var ErrNotForeground = errors.New("not in the terminal's foreground process group")

// InterruptedError is returned when a signal ended a picker.
type InterruptedError struct{ Signal os.Signal }

func (e *InterruptedError) Error() string { return "interrupted by " + e.Signal.String() }

// ChooseOptions configures a picker.
type ChooseOptions struct {
	Header string // text above the query; may span several lines
	Multi  bool   // let tab, shift-tab and space mark several items
	Height int    // lines to use; 0 (or more than the terminal has) means all
	Limit  int    // most items that can be marked, 0 for no limit; above 1 implies Multi
	Query  string // initial query

	Prompt string   // query prompt, "❯ " by default
	Styles *Styles  // nil means DefaultStyles()
	Env    []string // environment for terminal detection (TERM, NO_COLOR, HOME); nil means os.Environ()
}

// HistoryItem is a command for BrowseHistory.
type HistoryItem struct {
	Cmd    string
	When   time.Time // zero when unknown
	Dir    string    // working directory, "" when unknown
	Status int       // exit status; above 0 marks a failure, below 0 means unknown
}

// Styles is the look of a picker.
type Styles struct {
	Prompt       lipgloss.Style // the query prompt
	Header       lipgloss.Style // the header lines
	Counter      lipgloss.Style // the matches/total counter
	Pointer      lipgloss.Style // the bar left of the current row
	Marker       lipgloss.Style // the dot left of marked rows
	Text         lipgloss.Style // item text
	Match        lipgloss.Style // matched characters
	Current      lipgloss.Style // text of the current row
	CurrentMatch lipgloss.Style // matched characters in the current row
	Dim          lipgloss.Style // parent directories, working directories, times
	Strong       lipgloss.Style // the last component of a directory
	Failed       lipgloss.Style // the mark of a failed command
}

// DefaultStyles is a monochrome look built from text attributes alone, so
// it suits any colour scheme and is unaffected by NO_COLOR.
func DefaultStyles() Styles {
	s := lipgloss.NewStyle()
	return Styles{
		Prompt:       s.Bold(true),
		Header:       s.Faint(true),
		Counter:      s.Faint(true),
		Pointer:      s.Bold(true),
		Marker:       s.Bold(true),
		Text:         s,
		Match:        s.Bold(true).Underline(true),
		Current:      s.Bold(true).Reverse(true),
		CurrentMatch: s.Bold(true).Underline(true).Reverse(true),
		Dim:          s.Faint(true),
		Strong:       s.Bold(true),
		Failed:       s.Bold(true),
	}
}

// Choose lets the user pick from items by typing to filter them. It
// returns the chosen items in their original order: one, or with Multi
// the marked ones (the current one if none is marked).
func Choose(items []string, opts ChooseOptions) ([]string, error) {
	its := make([]item, len(items))
	for i, s := range items {
		its[i] = item{text: s, value: s}
	}
	m := newModel(kindChoose, its, opts)
	if err := run(m, opts); err != nil {
		return nil, err
	}
	out := make([]string, len(m.chosen))
	for i, c := range m.chosen {
		out[i] = items[c]
	}
	return out, nil
}

// PickDir lets the user pick one of dirs, shown with ~ for $HOME, the
// parent path dimmed and the last component emphasized.
func PickDir(dirs []string, opts ChooseOptions) (string, error) {
	home := homeDir(opts.Env)
	its := make([]item, len(dirs))
	for i, d := range dirs {
		text := tildePath(d, home)
		its[i] = item{text: text, value: d, base: lastComponent(text)}
	}
	opts.Multi, opts.Limit = false, 0
	m := newModel(kindDir, its, opts)
	if err := run(m, opts); err != nil {
		return "", err
	}
	return its[m.chosen[0]].value, nil
}

// BrowseHistory lets the user search past commands, newest first, and
// returns the chosen one. Entries are taken to be newest first unless all
// of them have a time. Repeated commands are shown once, at their latest
// use.
func BrowseHistory(entries []HistoryItem, opts ChooseOptions) (string, error) {
	opts.Multi, opts.Limit = false, 0
	m := newModel(kindHistory, historyItems(entries, time.Now(), homeDir(opts.Env)), opts)
	if err := run(m, opts); err != nil {
		return "", err
	}
	return m.items[m.chosen[0]].value, nil
}

func historyItems(entries []HistoryItem, now time.Time, home string) []item {
	timed := len(entries) > 0
	for i := range entries {
		if entries[i].When.IsZero() {
			timed = false
			break
		}
	}
	if timed {
		entries = slices.Clone(entries)
		slices.SortStableFunc(entries, func(a, b HistoryItem) int {
			return b.When.Compare(a.When)
		})
	}
	seen := make(map[string]struct{}, len(entries))
	its := make([]item, 0, len(entries))
	for _, e := range entries {
		if strings.TrimSpace(e.Cmd) == "" {
			continue
		}
		if _, dup := seen[e.Cmd]; dup {
			continue
		}
		seen[e.Cmd] = struct{}{}
		its = append(its, item{
			text:   e.Cmd,
			value:  e.Cmd,
			when:   relTime(now, e.When),
			dir:    tildePath(e.Dir, home),
			failed: e.Status > 0,
		})
	}
	return its
}

// relTime formats how long ago t was: "42s", "3m", "2h", "5d", "3w",
// "4mo", "2y". It returns "" for the zero time.
func relTime(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := max(now.Sub(t), 0)
	const day = 24 * time.Hour
	n, unit := 0, ""
	switch {
	case d < time.Minute:
		n, unit = int(d/time.Second), "s"
	case d < time.Hour:
		n, unit = int(d/time.Minute), "m"
	case d < day:
		n, unit = int(d/time.Hour), "h"
	case d < 7*day:
		n, unit = int(d/day), "d"
	case d < 30*day:
		n, unit = int(d/(7*day)), "w"
	case d < 365*day:
		n, unit = int(d/(30*day)), "mo"
	default:
		n, unit = int(d/(365*day)), "y"
	}
	return strconv.Itoa(n) + unit
}

func homeDir(env []string) string {
	if env == nil {
		return os.Getenv("HOME")
	}
	return lookupEnv(env, "HOME")
}

// tildePath abbreviates $HOME to ~.
func tildePath(p, home string) string {
	home = strings.TrimSuffix(home, "/")
	if home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+"/"); ok {
		return "~/" + rest
	}
	return p
}

// lastComponent is the rune index where the last component of a path
// starts ("~/src/ash" → 6, "/" → 0).
func lastComponent(p string) int {
	trimmed := strings.TrimRight(p, "/")
	i := strings.LastIndexByte(trimmed, '/')
	if i < 0 {
		return 0
	}
	return utf8.RuneCountInString(p[:i+1])
}
