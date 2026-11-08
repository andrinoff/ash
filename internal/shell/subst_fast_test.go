package shell

import (
	"testing"

	"github.com/andrinoff/ash/internal/syntax"
)

// Substitutions of pure builtins skip the subshell; what they print and
// what they leave behind must be what the subshell gave.
func TestSubstFastPathSameAsSubshell(t *testing.T) {
	scripts := []string{
		// values and status
		`set i 3; set i (math $i + 1); echo $i $status`,
		`set x (math 1 / 0); echo "[$x]" $status`,
		`set x (false); echo $status; set x (true); echo $status`,
		`x=$(printf '%s-%s\n' a b); echo "$x" $?`,
		`x=$(echo "a  b"); echo "[$x]"; y=$(echo); echo "[$y]"`,
		`set l (string split , a,b,,c); count $l; printf '<%s>' $l; echo`,
		`set l (echo -n); count $l`,
		`set l (printf 'a\n\nb\n\n'); count $l; printf '<%s>' $l; echo`,
		`x=$(printf 'a\n\n\n'); echo "[$x]"`,
		`for w in $(echo a b "c d"); do echo "<$w>"; done`,
		`set -l v x y; echo (string join - $v[2] $v)`,
		`echo (contains b a b c; echo $status) (count (string split '' abc))`,
		// $_ and the last command's status around it
		`echo one two; x=$(echo three); echo "$_"`,
		`false; x=$(true) ; echo $?`,
		`set x (string match -q nope yes); echo $status; and echo no; or echo yes`,
		// what can hide a builtin
		`function math; echo from-function; end; echo (math 1 + 1)`,
		`math() { echo fn; }; echo "$(math 2 + 2)"`,
		`alias echo='printf "<%s>"'; x=$(echo hi); echo; echo "$x"`,
		`printf -v v %s set; echo "${v-unset}"; x=$(printf -v w %s set); echo "${w-unset}"`,
		// expansions with side effects stay in a subshell
		`x=$(echo ${a:=set}); echo "[$x] [${a-unset}]"`,
		`i=1; x=$(echo $((i+=1))); echo "$x $i"`,
		`x=$(echo ${nope:?gone}); echo "after [$x] $?"`,
		`set -u; x=$(echo $nope); echo "after [$x] $?"`,
		`set -e; x=$(false); echo not-reached`,
		`set -e; x=$(true); echo reached`,
		// nested
		`set x (math (math 1 + 2) '*' (string length abcd)); echo $x`,
		`x=$(echo $(echo in) out); echo "$x"`,
		// stdin
		`echo piped | read -l a; echo (string upper -- $a)`,
		// pipelines of pure builtins
		`set n (string length -- abc de | string join +); echo $n $pipestatus`,
		`echo a b | string upper | string split ' '; echo $pipestatus`,
		`string match -q x y | string join , a b; echo $status $pipestatus`,
		`printf 'x\ny\n' | count; echo $pipestatus`,
		`set -o pipefail; false | true; echo $?; string match -q x y | echo hi; echo $?`,
		`echo one two | string split ' ' | string join -; echo "$_"`,
		`! echo a | string match -q a; echo $?`,
		`function upper; string upper; end; echo a | upper`,
		`echo a | math 1 + 1 | string repeat -n 2`,
		`string upper | string join -`,
		`set -u; echo $nope | string upper; echo after $?`,
		`echo a 2>&1 | string upper`,
	}
	for _, src := range scripts {
		noSubstFastPath = true
		wantOut, wantErr, wantSt := runScript(t, src)
		noSubstFastPath = false
		gotOut, gotErr, gotSt := runScript(t, src)
		if gotOut != wantOut || gotErr != wantErr || gotSt != wantSt {
			t.Errorf("%s\n fast path: %q %q %d\n subshell:  %q %q %d", src, gotOut, gotErr, gotSt, wantOut, wantErr, wantSt)
		}
	}
}

func TestSubstFastPathTakesOnlyPureCalls(t *testing.T) {
	for src, want := range map[string]bool{
		`(math $i + 1)`:           true,
		`$(printf %s "$x")`:       true,
		`$(printf -v x %s y)`:     false,
		`$(printf "$fmt" y)`:      false,
		`(string length -- $p)`:   true,
		`(set x 1)`:               false,
		`(math 1 >&2)`:            false,
		`(math 1; math 2)`:        false,
		`(math 1 &)`:              false,
		`$(x=1 echo hi)`:          false,
		`$(echo ${x:=1})`:         false,
		`$(echo $((i++)))`:        false,
		`$(echo ${a[i++]})`:       false,
		`$(echo ${a[@]} ${#b})`:   true,
		`(echo $l[(math 1 + 1)])`: true,
		`(cd /)`:                  false,
		`(read x)`:                false,
	} {
		f, err := syntax.Parse("echo " + src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		call := f.Stmts[0].Cmd.(*syntax.CallExpr)
		cs, ok := call.Args[1].Parts[0].(*syntax.CmdSubst)
		if !ok {
			t.Fatalf("%s: no substitution (%T)", src, call.Args[1].Parts[0])
		}
		if got := pureCall(cs.Stmts) != nil; got != want {
			t.Errorf("%s: fast path %v, want %v", src, got, want)
		}
	}
}
