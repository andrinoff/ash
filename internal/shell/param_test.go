package shell

import "testing"

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
