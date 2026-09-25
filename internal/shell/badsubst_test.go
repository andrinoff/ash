package shell

import (
	"strings"
	"testing"
)

// A ${...} ash can't make sense of is an error when it is expanded, not
// when the script is parsed (scripts keep zsh's ${(f)x} in zsh-only code).
func TestBadSubstitutionAtRuntime(t *testing.T) {
	out, errOut, _ := runScript(t, "if false; then echo ${(f)x}; fi; echo before; echo ${x b}; echo status $?")
	if out != "before\nstatus 1\n" || !strings.Contains(errOut, "${x b}: bad substitution") {
		t.Fatalf("got %q %q", out, errOut)
	}
}
