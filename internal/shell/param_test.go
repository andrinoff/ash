package shell

import "testing"

// Expected values come from fish 4.8.
func TestFishSliceDirection(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{`set x a; echo "[$x[2..-1]]" "[$x[2..]]"`, "[] []\n"},
		{`set x a b c; echo "[$x[4..]]" "[$x[1..-4]]" "[$x[..-4]]"`, "[] [] []\n"},
		{`set x a b c; echo $x[2..-1] / $x[-1..1] / $x[1..-2] / $x[3..1] / $x[5..1]`, "b c / c b a / a b / c b a / c b a\n"},
		{`set x a b c; echo "[$x[-3..2]]" "[$x[-1..4]]" "[$x[2..-3]]"`, "[] [] []\n"},
		{`set x a b c; echo $x[-4..-2] / $x[-1..-4] / $x[-6..] / $x[..2] / $x[-2..1]`, "a b / c b a / a b c / a b / b a\n"},
		{`set x a b c d e; echo "[$x[-5..2]]" $x[-4..2] / $x[3..-3]`, "[] b / c\n"},
		{`function f; echo "[$argv[2..-1]]"; end; f one; f one two three`, "[]\n[two three]\n"},
	} {
		out, errOut, _ := runFish(t, c.src)
		if out != c.want {
			t.Errorf("%s: got %q (stderr %q), want %q", c.src, out, errOut, c.want)
		}
	}
}

func TestFatalExpansionExitsScript(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{`unset x; : ${x:?oops}; echo after`, ""},
		{`x=; y=${x:?}; echo after`, ""},
		{`unset x; for i in ${x?oops}; do :; done; echo after`, ""},
		{`unset x; f() { : ${x:?oops}; echo inner; }; f; echo after`, ""},
		{`set -u; x=$undefined; echo after`, ""},
		{`set -u; case $undefined in *) ;; esac; echo after`, ""},
		{`unset x; (: ${x:?oops}; echo inner); echo after $?`, "after 1\n"},
		{`unset x; y=$(: ${x:?oops}); echo after $?`, "after 1\n"},
		{`x=1; : ${x:?oops}; set -u; echo ${undefined:-ok} after`, "ok after\n"},
	} {
		out, errOut, st := runScript(t, c.src)
		if out != c.want || c.want == "" && st != 1 {
			t.Errorf("%s: got %q status %d (stderr %q), want %q", c.src, out, st, errOut, c.want)
		}
	}
}

func TestPathListSubstringAndQuote(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{`MYPATH=/usr/bin:/bin; echo ${MYPATH:0:12}`, "/usr/bin:/bi\n"},
		{`MYPATH=/usr/bin:/bin; echo "${MYPATH:4}" ${MYPATH: -4}`, "/bin:/bin /bin\n"},
		{`MYPATH="/a b:/c"; eval x=${MYPATH@Q}; echo "$x"`, "/a b:/c\n"},
		{`MYPATH=/a:/b; args "${MYPATH[@]:1}" $MYPATH`, "</b></a></b>\n"},
	} {
		out, errOut, _ := runScript(t, argsFn+c.src)
		if out != c.want {
			t.Errorf("%s: got %q (stderr %q), want %q", c.src, out, errOut, c.want)
		}
	}
}

func TestStarJoinsAfterOperator(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{`set -- a 'b c'; args "$*" "$@"`, "<a b c><a><b c>\n"},
		{`set -- abc def; args "${*#?}" ${*#?}`, "<bc ef><bc><ef>\n"},
		{`set -- abc def; args "${*//?/X}"`, "<XXX XXX>\n"},
		{`set -- abc def; args "${*^}"`, "<Abc Def>\n"},
		{`set -- abc def ghi; args "${*:2}" ${*:2}`, "<def ghi><def><ghi>\n"},
		{`set -- abc def ghi; IFS=:; args "${*:2}"`, "<def:ghi>\n"},
		{`set -- a b c; args "${#*}" "${#@}" ${#*}`, "<3><3><3>\n"},
		{`set -- a b; IFS=; args "$*"`, "<ab>\n"},
		{`set -- a b; IFS=→; args "$*"`, "<a→b>\n"},
		{`a=(x 'y z' w); args "${#a[*]}" "${#a[@]}"`, "<3><3>\n"},
		{`a=(x 'y z' w); IFS=:; args "${a[*]}"`, "<x:y z:w>\n"},
		{`a=(abc def); args "${a[*]#?}" "${a[@]#?}"`, "<bc ef><bc><ef>\n"},
		{`a=(abc def ghi); args "${a[*]:1}" ${a[*]:1}`, "<def ghi><def><ghi>\n"},
		{`a=(x y); IFS=-; args "${!a[*]}" "${!a[@]}"`, "<0-1><0><1>\n"},
		{`a=(x y); ref='a[*]'; IFS=-; args "${!ref}"`, "<x-y>\n"},
	} {
		out, errOut, _ := runScript(t, argsFn+c.src)
		if out != c.want {
			t.Errorf("%s: got %q (stderr %q), want %q", c.src, out, errOut, c.want)
		}
	}
}
