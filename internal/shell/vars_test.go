package shell

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestPpidUidEuid(t *testing.T) {
	want := fmt.Sprintf("%d %d %d\n", os.Getppid(), os.Getuid(), os.Geteuid())
	out, errOut, _ := runScript(t, `echo $PPID $UID $EUID`)
	if out != want {
		t.Errorf("got %q (stderr %q), want %q", out, errOut, want)
	}
	if _, errOut, st := runScript(t, `PPID=1`); st == 0 || errOut == "" {
		t.Errorf("PPID=1 succeeded")
	}
	// Setting EUID must not make a script believe it runs as root.
	if out, errOut, _ := runScript(t, `EUID=0; echo $EUID`); out != fmt.Sprintf("%d\n", os.Geteuid()) {
		t.Errorf("EUID=0; echo $EUID: got %q (stderr %q)", out, errOut)
	}
	// fish configs export UID for docker; that must keep working.
	out, errOut, st := runFish(t, `set -gx UID 4242; /usr/bin/env | grep '^UID='; echo $UID`)
	if want := fmt.Sprintf("UID=4242\n%d\n", os.Getuid()); out != want || errOut != "" || st != 0 {
		t.Errorf("set -gx UID: got %q status %d (stderr %q), want %q", out, st, errOut, want)
	}
}

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
