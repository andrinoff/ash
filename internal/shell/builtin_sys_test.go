package shell

import "testing"

func TestGetoptsResetMidCluster(t *testing.T) {
	// The first call stops inside "-ab"; resetting OPTIND must not
	// reuse the old position (this used to panic with an index out of
	// range on the shorter "-a").
	out, errOut, st := runScript(t, `
f() { OPTIND=1; while getopts "ab" o; do echo "f $o"; case $o in a) return;; esac; done; }
f -ab
f -a
f -cd 2>/dev/null
set -- -ab -c
OPTIND=1
getopts abc o; echo "$o $OPTIND"
OPTIND=2
getopts abc o; echo "$o $OPTIND"
`)
	if want := "f a\nf a\nf ?\nf ?\na 1\nc 3\n"; out != want || st != 0 {
		t.Fatalf("got %q (stderr %q) status %d, want %q", out, errOut, st, want)
	}
}

func TestGetoptsSetsOptindAtEnd(t *testing.T) {
	out, errOut, st := runScript(t, `
echo "start $OPTIND"
set -- file1
while getopts v o; do :; done
shift $((OPTIND-1)); echo "$? $# $1 $OPTIND"
set -- -v -- rest
OPTIND=1
while getopts v o; do echo "opt $o"; done
echo "after -- $OPTIND [$o]"
OPTIND=0
set -- -v
getopts v o; echo "zero: $o $OPTIND"
`)
	want := "start 1\n0 1 file1 1\nopt v\nafter -- 3 [?]\nzero: v 2\n"
	if out != want || st != 0 {
		t.Fatalf("got %q (stderr %q) status %d, want %q", out, errOut, st, want)
	}
}

func TestGetoptsOptarg(t *testing.T) {
	out, _, _ := runScript(t, `
set -- -x -a -b val
while getopts ":ab:" opt; do echo "opt=$opt arg=${OPTARG-unset}"; done
set -- -b
OPTIND=1
getopts ":b:" opt; echo "opt=$opt arg=${OPTARG-unset}"
`)
	want := "opt=? arg=x\nopt=a arg=unset\nopt=b arg=val\nopt=: arg=b\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}
