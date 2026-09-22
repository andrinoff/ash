package shell

import "testing"

// local, declare, export and friends take assignments, as in bash.
func TestDeclarationAssignments(t *testing.T) {
	cases := []struct{ src, want string }{
		// an array, not a command substitution that runs `a`
		{`f() { local arr=(a "b c"); echo ${#arr[@]} "${arr[1]}"; }; f`, "2 b c\n"},
		{`f() { declare -a d=(1 2 3); local -r r=(x y); echo ${d[2]} ${r[1]}; }; f`, "3 y\n"},
		{`readonly R=(1 2); echo ${R[1]}; readonly R=(3); echo ${R[0]}`, "2\n1\n"},
		{`arr=(a); declare arr+=(b); echo ${arr[@]}`, "a b\n"},
		// no word splitting
		{`f() { local s=$(echo a  b) t; echo "[$s] [$t]"; }; f`, "[a b] []\n"},
		{`export E=$(echo c d); echo "[$E]"`, "[c d]\n"},
		// a path list stays whole
		{`PATH=/usr/bin:/bin; export PATH=$PATH:/opt/x; echo $PATH`, "/usr/bin /bin /opt/x\n"},
		// ~ after = and :
		{`HOME=/h; export X=~/a:~/b; f() { local y=~; echo $y; }; f; echo $X`, "/h\n/h/a:/h/b\n"},
		// export X=(cmd) is fish's command substitution
		{`export X=(echo a b); echo $X`, "a b\n"},
	}
	for _, c := range cases {
		out, errOut, _ := runScript(t, c.src)
		if out != c.want {
			t.Errorf("%s\ngot %q (stderr %q), want %q", c.src, out, errOut, c.want)
		}
	}
}
