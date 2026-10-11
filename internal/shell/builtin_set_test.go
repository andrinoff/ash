package shell

import (
	"strings"
	"testing"
)

func TestSetLocalScopeQueries(t *testing.T) {
	// set -ql and set -el only looked at the innermost block.
	out, errOut, _ := runFish(t, `
function f
    set -l x 1
    if true
        set -ql x; and echo yes
        set -el x; echo $status
    end
    set -q x; echo $status
end
f
begin; set -l y 1; begin; set -ql y; echo $status; end; end
function g; set -ql x; echo $status; end
function h; set -l x 1; g; end
h
set -q; echo $status
`)
	if want := "yes\n0\n1\n0\n1\n255\n"; out != want {
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
}

func TestSetListsOnlyTheScopeAsked(t *testing.T) {
	out, errOut, _ := runFish(t, `
function f
    set -l a 1
    set -g gg 2
    echo local:; set -l
    echo names:; set -ln
end
f
`)
	if want := "local:\na 1\nargv\nnames:\na\nargv\n"; out != want { // as fish prints it
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
	out, _, _ = runFish(t, `set -g only_global 1; set -g | grep -c '^only_global 1$'; set -n | grep -cx PATH`)
	if out != "1\n1\n" {
		t.Fatalf("set -g / set -n: got %q", out)
	}
	if strings.Contains(out, "PATH ") {
		t.Fatalf("set -n printed values: %q", out)
	}
}
