package editor

// Candidate is one completion offered to the user.
type Candidate struct {
	Text        string // text that replaces the token (already escaped)
	Display     string // label shown in the pager; Text if empty
	Description string
	NoSpace     bool   // don't add a space after inserting (dirs, "--opt=")
	Kind        string // "file", "dir", "command", "option", "variable", ...
}

// Label is what the pager shows for a candidate.
func (c Candidate) Label() string {
	if c.Display != "" {
		return c.Display
	}
	return c.Text
}

// Completion is the answer to a completion request: the candidates and
// the byte range of the buffer they replace.
type Completion struct {
	Start, End int
	Items      []Candidate
}
