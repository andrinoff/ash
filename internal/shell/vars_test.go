package shell

import (
	"strings"
	"testing"
)

// Environment entries whose names aren't valid variable names are
// passed on to children, as bash and fish do.
func TestForeignEnvPassedToChildren(t *testing.T) {
	t.Setenv("ash.test-var", "dotted")
	t.Setenv("ASH_TEST_PLAIN", "plain")
	out, errOut, _ := runScript(t, `/usr/bin/env`)
	for _, want := range []string{"ash.test-var=dotted", "ASH_TEST_PLAIN=plain"} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("child environment lacks %s (stderr %q)", want, errOut)
		}
	}
}
