package shell

import "testing"

// args prints each argument in brackets, so tests can see word splitting.
const argsFn = "args() { for a in \"$@\"; do echo -n \"<$a>\"; done; echo; }; "

func TestUnquotedCmdSubstDropsTrailingNewlines(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{`IFS=:; args $(echo a:b::c)`, "<a><b><><c>\n"},
		{`IFS=:; args $(echo a:; echo)`, "<a>\n"},
		{`case foo in $(echo foo)) echo yes;; *) echo no;; esac`, "yes\n"},
		{`x=abc; echo ${x#$(echo a)}`, "bc\n"},
		{`x=abc; echo ${x/$(echo b)/X}`, "aXc\n"},
		{`[[ abc == $(echo abc) ]] && echo yes`, "yes\n"},
		{`[[ abc =~ $(echo b) ]] && echo yes`, "yes\n"},
		{`args $(echo " a  b ")`, "<a><b>\n"},
	} {
		out, errOut, _ := runScript(t, argsFn+c.src)
		if out != c.want {
			t.Errorf("%s: got %q (stderr %q), want %q", c.src, out, errOut, c.want)
		}
	}
}
