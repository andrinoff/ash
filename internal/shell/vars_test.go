package shell

import (
	"strings"
	"testing"
)

func TestAssignSecondsAndRandom(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{`SECONDS=100; echo $SECONDS`, "100\n"},
		{`sleep 0; SECONDS=0; echo $SECONDS`, "0\n"},
		{`RANDOM=7; a=$RANDOM$RANDOM; RANDOM=7; b=$RANDOM$RANDOM; [ "$a" = "$b" ] && echo same`, "same\n"},
	} {
		out, errOut, _ := runScript(t, c.src)
		if out != c.want || errOut != "" {
			t.Errorf("%s: got %q (stderr %q), want %q", c.src, out, errOut, c.want)
		}
	}
}

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
