// Package plugin runs ash plugins: long-running programs, written in any
// language, that talk to the shell in JSON lines over their standard
// input and output. ash sends them events and requests; they answer
// requests and send actions back whenever they like. docs/plugins.md
// describes the protocol.
//
// The package doesn't know about the interpreter. The shell gives it
// what it needs (paths, environment, a wake-up callback) and applies the
// actions itself, on its own goroutine, when it is safe to.
package plugin

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Events a plugin can subscribe to in its hello.
const (
	EventPrompt   = "prompt"
	EventPreExec  = "preexec"
	EventPostExec = "postexec"
	EventCd       = "cd"
	EventExit     = "exit"
)

// eventNames lists the events in the order of their bits.
var eventNames = [...]string{EventPrompt, EventPreExec, EventPostExec, EventCd, EventExit}

// eventBit returns the bit of an event, or 0 for an unknown name.
func eventBit(name string) uint8 {
	for i, n := range eventNames {
		if n == name {
			return 1 << i
		}
	}
	return 0
}

func eventIndex(bit uint8) int {
	for i := range eventNames {
		if bit == 1<<i {
			return i
		}
	}
	return -1
}

// Hello is the first line a plugin writes: what it wants to hear about
// and what it adds to the shell.
type Hello struct {
	Name     string    `json:"name"`
	Events   []string  `json:"events,omitempty"`
	Commands []Command `json:"commands,omitempty"`
	Complete []string  `json:"complete,omitempty"`
}

// Command is a command a plugin adds to the shell.
type Command struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Candidate is one completion a plugin offers.
type Candidate struct {
	Text        string `json:"text"`
	Description string `json:"description,omitempty"`
}

// ActionKind says what an Action does.
type ActionKind uint8

const (
	// ActSet sets the global variable Name to Values (exported if Export).
	ActSet ActionKind = iota + 1
	// ActUnset erases the global variable Name.
	ActUnset
	// ActSegment sets the prompt segment Name (the variable
	// ash_plugin_NAME) to Text, or erases it if Text is empty.
	ActSegment
	// ActCd changes the shell's directory to Text.
	ActCd
	// ActBuffer puts Text on the command line.
	ActBuffer
	// ActMessage shows Text above the prompt.
	ActMessage
)

// Action is something a plugin asked the shell to do. Messages that ash
// itself generates about a plugin (it crashed, say) are ActMessage too.
type Action struct {
	Plugin string // the plugin it came from
	Kind   ActionKind
	Name   string   // ActSet, ActUnset, ActSegment
	Values []string // ActSet
	Export bool     // ActSet
	Text   string   // ActSegment, ActCd, ActBuffer, ActMessage

	crash bool // ash's report that the plugin stopped
}

// inbound is any line a plugin writes. The keys that are present say what
// it is; one line may hold a hello, a reply and several actions.
type inbound struct {
	Hello *Hello `json:"hello"`

	// A reply to request ID.
	ID         *uint64     `json:"id"`
	Stdout     string      `json:"stdout"`
	Stderr     string      `json:"stderr"`
	Status     int         `json:"status"`
	Candidates []Candidate `json:"candidates"`

	// Actions.
	Set     *setAction     `json:"set"`
	Unset   *stringList    `json:"unset"`
	Segment *segmentAction `json:"segment"`
	Cd      *string        `json:"cd"`
	Buffer  *string        `json:"buffer"`
	Message *string        `json:"message"`
}

type setAction struct {
	Name   string     `json:"name"`
	Value  stringList `json:"value"`
	Export bool       `json:"export"`
}

type segmentAction struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// stringList is a list of strings that may also be written as a single
// string.
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*l = stringList{s}
		return nil
	}
	var ss []string
	if err := json.Unmarshal(b, &ss); err != nil {
		return err
	}
	*l = ss
	return nil
}

// parseLine decodes one line a plugin wrote.
func parseLine(line []byte) (*inbound, error) {
	var m inbound
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// empty reports whether the line held nothing ash understands.
func (m *inbound) empty() bool {
	return m.Hello == nil && m.ID == nil && m.Set == nil && m.Unset == nil &&
		m.Segment == nil && m.Cd == nil && m.Buffer == nil && m.Message == nil
}

// actions returns the actions in m in the order they are applied: set,
// unset, segment, cd, buffer, message.
func (m *inbound) actions(plugin string) []Action {
	var out []Action
	if m.Set != nil {
		vals := []string(m.Set.Value)
		if vals == nil {
			vals = []string{}
		}
		out = append(out, Action{Plugin: plugin, Kind: ActSet, Name: m.Set.Name, Values: vals, Export: m.Set.Export})
	}
	if m.Unset != nil {
		for _, n := range *m.Unset {
			out = append(out, Action{Plugin: plugin, Kind: ActUnset, Name: n})
		}
	}
	if m.Segment != nil {
		out = append(out, Action{Plugin: plugin, Kind: ActSegment, Name: m.Segment.Name, Text: m.Segment.Text})
	}
	if m.Cd != nil {
		out = append(out, Action{Plugin: plugin, Kind: ActCd, Text: *m.Cd})
	}
	if m.Buffer != nil {
		out = append(out, Action{Plugin: plugin, Kind: ActBuffer, Text: *m.Buffer})
	}
	if m.Message != nil {
		out = append(out, Action{Plugin: plugin, Kind: ActMessage, Text: *m.Message})
	}
	return out
}

// Lines ash writes.

type promptEvent struct {
	Event      string `json:"event"`
	Cwd        string `json:"cwd"`
	Status     int    `json:"status"`
	Pipestatus []int  `json:"pipestatus"`
	DurationMs int64  `json:"duration_ms"`
}

type preexecEvent struct {
	Event string `json:"event"`
	Line  string `json:"line"`
	Cwd   string `json:"cwd"`
}

type postexecEvent struct {
	Event      string `json:"event"`
	Line       string `json:"line"`
	Status     int    `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Cwd        string `json:"cwd"`
}

type cdEvent struct {
	Event string `json:"event"`
	From  string `json:"from"`
	To    string `json:"to"`
}

type exitEvent struct {
	Event string `json:"event"`
}

type runRequest struct {
	ID      uint64   `json:"id"`
	Request string   `json:"request"`
	Name    string   `json:"name"`
	Args    []string `json:"args"`
	Cwd     string   `json:"cwd"`
	Stdin   *string  `json:"stdin,omitempty"`
}

type completeRequest struct {
	ID      uint64 `json:"id"`
	Request string `json:"request"`
	Name    string `json:"name"`
	Line    string `json:"line"`
	Cursor  int    `json:"cursor"`
	Cwd     string `json:"cwd"`
}

type cancelRequest struct {
	ID      uint64 `json:"id"`
	Request string `json:"request"`
}

// encode renders v as one line of JSON, newline included. HTML characters
// are left alone, so "a && b" stays readable.
func encode(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// Only unsupported types fail, and the types above are fine.
		panic("plugin: " + err.Error())
	}
	return b.Bytes()
}

// ValidName reports whether s can name a plugin. The name is also the
// name of its log file, so it is kept to letters, digits, '_', '-' and
// '.', and can't start with '-' or '.'.
func ValidName(s string) bool {
	if s == "" || s[0] == '-' || s[0] == '.' || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// validCommand reports whether s can name a command a plugin adds: a word
// that needs no quoting on the command line.
func validCommand(s string) bool {
	if s == "" || s[0] == '-' || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c == 0x7f || strings.IndexByte("/\\'\"`$&|;<>()[]{}*?~#!=%", c) >= 0 {
			return false
		}
	}
	return true
}

// validSegment reports whether s can name a prompt segment: the variable
// ash_plugin_NAME must be a valid name.
func validSegment(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
