package syntax

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseOK(t *testing.T) {
	cases := []string{
		"echo hello world",
		"ls -la | grep foo | wc -l",
		"a && b || c",
		"FOO=bar BAZ=qux env",
		"x=1",
		"arr=(a b 'c d')",
		"echo $x ${y:-default} ${#z} ${a/b/c} ${a//b} ${a:1:2} ${!ref} ${arr[@]} ${arr[1]}",
		`echo "hello $USER, it's $(date +%H)" '$not' $'tab\there'`,
		"echo `pwd`",
		"if [ -f x ]; then echo yes; elif true; then :; else echo no; fi",
		"if test -f x; echo yes; else if true; echo maybe; else; echo no; end",
		"if test a; and test b; or test c\n  echo x\nend",
		"if a; b; then c; fi",
		"if\n  true\nthen\n  echo\nfi",
		"while # comment\n  false\ndo :; done",
		"if false; then :; elif\n  true; then :; fi",
		"while read line; do echo $line; done < file",
		"while true; echo; end",
		"until false; do break; done",
		"for i in 1 2 3; do echo $i; done",
		"for i in 1 2 3; echo $i; end",
		"for i; do echo $i; done",
		"for i\ndo echo $i; done",
		"for i # comment\nin 1 2\ndo echo $i; done",
		"for ((i=0; i<10; i++)); do echo $i; done",
		"case $x in a|b) echo ab;; *) echo other;; esac",
		"case $x in\n  (a) echo a ;;\n  *.go) echo go\nesac",
		"switch $x\n case a b\n  echo ab\n case '*'\n  echo other\nend",
		"function foo; echo $argv; end",
		"function foo -d 'desc' --on-variable PWD\n  echo hi\nend",
		"foo() { echo hi; }",
		"function bar { echo hi; }",
		"function bar() { echo hi; }",
		"begin; echo a; end",
		"{ echo a; echo b; } > out",
		"(cd /tmp && ls)",
		"echo (pwd)/x",
		"set -l x (math $i + 1)",
		"echo $argv[1] $argv[2..-1] $argv[(math $i + 1)..] \"$argv[$i]\"",
		"echo {a,b,c}{1..3}",
		"echo ~ ~/x ~root",
		"cat <<EOF\nhello $USER\nEOF\necho after",
		"cat <<'EOF'\n$not expanded\nEOF",
		"cat <<-EOF\n\tindented\n\tEOF",
		"echo foo 2>&1 >/dev/null &>log &>>log",
		"cmd <<< 'herestring'",
		"diff <(ls a) <(ls b)",
		"[[ -f x && $a == b* || $c =~ ^(a|b)$ ]]",
		"(( i++ ))",
		"echo $(( 1 + 2 * (3 - 1) ))",
		"! true",
		"not true; and echo x",
		"sleep 1 &",
		"sleep 1 & echo b",
		"a & b &! c & wait",
		"{ sleep 1 & }",
		"case x in x) a & b;; esac",
		"time ls",
		"echo a |& cat",
		"echo a &| cat",
		"echo a#b # comment",
		"# just a comment",
		"echo \\\n  continued",
		"echo \"multi\nline\"",
		"export PATH=\"$(go env GOPATH)/bin:$PATH\"",
		"status is-interactive; and set -gx GPG_TTY (tty)",
		"bind \\cl 'clear; commandline -f repaint'",
		"function $Z_CMD -d \"jump around\"\n    __z $argv\nend",
		"echo $x[0-9]",
		"echo ${x:-$(echo a b)}",
		"local a=1 b",
		"echo ${HOME}/x",
		"echo a; ; echo b",
	}
	for _, src := range cases {
		if _, err := Parse(src); err != nil {
			t.Errorf("Parse(%q): %v", src, err)
		}
	}
}

func TestParseIncomplete(t *testing.T) {
	cases := []string{
		"echo 'abc",
		"echo \"abc",
		"if true; then",
		"if true",
		"for x in a b",
		"function foo",
		"while true; echo",
		"echo $(ls",
		"echo (ls",
		"echo a |",
		"echo a &&",
		"cat <<EOF\nabc",
		"echo \\",
		"{ echo a;",
		"case x in",
		"switch x",
		"begin",
		"echo ${x",
		"sleep 1 & \\",
		"{ echo a; } \\",
		"(echo a) \\",
		"if true; then echo; fi \\",
		"for i in a; do echo; done \\",
		"[[ a \\",
		"echo a; \\",
		"if",
		"while true; do echo; done; until",
		"true; and \\",
		"case x in (a) echo",
	}
	for _, src := range cases {
		_, err := Parse(src)
		if err == nil {
			t.Errorf("Parse(%q): expected incomplete error", src)
			continue
		}
		if !IsIncomplete(err) {
			t.Errorf("Parse(%q): expected incomplete, got %v", src, err)
		}
		// Enter inserts a newline, which must not make it an error. (A
		// trailing backslash joins the lines instead.)
		if strings.HasSuffix(src, "\\") {
			continue
		}
		if _, err := Parse(src + "\n"); err != nil && !IsIncomplete(err) {
			t.Errorf("Parse(%q): %v, but without the newline it was incomplete", src+"\n", err)
		}
	}
}

// Every line of a valid program that ends before the program does must
// read as complete or incomplete, never as an error: that is what the line
// editor looks at when Enter is pressed at the end of it.
func TestParseLinePrefixes(t *testing.T) {
	progs := []string{
		"if true\nthen\n  echo a\nelif false\nthen\n  :\nelse\n  echo b\nfi",
		"for i in a b \\\n  c\ndo\n  echo $i\ndone",
		"case x in\n  (a)\n    echo a\n    ;;\n  b|c) echo bc;;\n  *)\n    echo other\nesac",
		"switch x\n  case a \\\n    b\n    echo ab\n  case '*'\n    echo other\nend",
		"f() {\n  echo a\n}",
		"function f\n  echo a\nend",
		"x=$(\n  echo a\n)",
		"x=$((\n  1 + 2\n))",
		"arr=(\n  a\n  b\n)",
		"echo a &&\n  echo b ||\n  echo c |\n  cat",
		"cat <<EOF\nbody\nEOF",
		"echo 'a\nb' \"c\nd\"",
		"sleep 1 & \\\n  echo b",
		"{ echo a; } \\\n  > /dev/null",
		"[[ a == a &&\n  b == b ]]",
		"while true\ndo\n  break\ndone",
		"begin\n  echo a\nend",
	}
	for _, prog := range progs {
		if _, err := Parse(prog); err != nil {
			t.Errorf("Parse(%q): %v", prog, err)
			continue
		}
		for i := range prog {
			if prog[i] != '\n' {
				continue
			}
			if _, err := Parse(prog[:i]); err != nil && !IsIncomplete(err) {
				t.Errorf("Parse(%q): %v (a prefix of %q)", prog[:i], err, prog)
			}
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := []string{
		"fi",
		"end",
		"echo a )",
		"if true; then echo; end",
		";;",
		// what is missing has to be on the same line, so Enter shouldn't
		// wait for another one
		"ls >",
		"cat <<",
		"function",
		"for",
		"case",
		"switch",
		"case x in (",
		"case x in a|",
		"case x in a",
		"!",
		"not",
		"not !",
		"true; and",
		"false; or # comment",
		"[[ a ==",
	}
	for _, src := range cases {
		_, err := Parse(src)
		if err == nil {
			t.Errorf("Parse(%q): expected error", src)
			continue
		}
		if IsIncomplete(err) {
			t.Errorf("Parse(%q): expected hard error, got incomplete: %v", src, err)
		}
	}
}

// An error inside a heredoc body, backquotes or a subscript can't be fixed
// by typing more lines once the construct is closed, and its position is in
// the source rather than in the cut out text.
func TestParseNestedErrors(t *testing.T) {
	cases := []struct {
		src       string
		line, col int
	}{
		{"echo a\ncat <<EOF\nx\n${y\nEOF\necho after", 4, 1},
		{"cat <<EOF\ncost: $(\nEOF", 2, 7},
		{"cat <<-EOF\n\t\t$(\n\tEOF", 2, 3},
		{"echo `echo $(`", 1, 6},
		{"echo `echo 'a`", 1, 6},
		{"x[$(]=1", 1, 3},
		{"echo ${a[`]}", 1, 10},
	}
	for _, c := range cases {
		_, err := Parse(c.src)
		if err == nil {
			t.Errorf("Parse(%q): expected an error", c.src)
			continue
		}
		if IsIncomplete(err) {
			t.Errorf("Parse(%q): %v: reported as incomplete", c.src, err)
		}
		line, col := LineCol(c.src, err.(*Error).Pos)
		if line != c.line || col != c.col {
			t.Errorf("Parse(%q): %v at %d:%d, want %d:%d", c.src, err, line, col, c.line, c.col)
		}
	}
}

func TestParseDeclArgs(t *testing.T) {
	f, err := Parse("local -r x=~/a arr+=(1 '2 3') y")
	if err != nil {
		t.Fatal(err)
	}
	call := f.Stmts[0].Cmd.(*CallExpr)
	d := call.DeclArgs
	if len(call.Args) != 5 || len(d) != 5 || d[0] != nil || d[1] != nil || d[4] != nil {
		t.Fatalf("got %d args, DeclArgs %v", len(call.Args), d)
	}
	if d[2].Name != "x" || len(d[2].Value.Parts) == 0 {
		t.Errorf("x=~/a: got %+v", d[2])
	} else if _, ok := d[2].Value.Parts[0].(*Tilde); !ok {
		t.Errorf("x=~/a: got %T, want *Tilde", d[2].Value.Parts[0])
	}
	if d[3].Name != "arr" || !d[3].IsArray || !d[3].Append || len(d[3].Array) != 2 {
		t.Errorf("arr+=(1 '2 3'): got %+v", d[3])
	}
	for _, src := range []string{"echo x=(pwd)", "export X=(tty)"} {
		f, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		if call := f.Stmts[0].Cmd.(*CallExpr); call.DeclArgs != nil {
			t.Errorf("%s: want no DeclArgs", src)
		}
	}
	f, err = ParseOpts("export X=~/a", Options{Fish: true})
	if err != nil {
		t.Fatal(err)
	}
	if call := f.Stmts[0].Cmd.(*CallExpr); call.DeclArgs != nil {
		t.Errorf("fish export X=~/a: want no DeclArgs")
	}
}

// bash doesn't brace-expand assignment values, but expands what is
// inside the braces.
func TestParseAssignBraces(t *testing.T) {
	f, err := Parse("x={$HOME,b}")
	if err != nil {
		t.Fatal(err)
	}
	parts := f.Stmts[0].Cmd.(*CallExpr).Assigns[0].Value.Parts
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want {, $HOME and ,b}", len(parts))
	}
	if pe, ok := parts[1].(*ParamExp); !ok || pe.Name != "HOME" {
		t.Errorf("got %T, want $HOME", parts[1])
	}
}

// fish drops the braces around {$var}, but not around {a}. sh keeps both.
func TestParseFishVarBraces(t *testing.T) {
	isBrace := func(w *Word) bool {
		_, ok := w.Parts[0].(*BraceExp)
		return ok
	}
	f, err := ParseOpts(`echo {$x}s {"$x"} {a} {'$x'} {(echo $x)}`, Options{Fish: true})
	if err != nil {
		t.Fatal(err)
	}
	args := f.Stmts[0].Cmd.(*CallExpr).Args
	for i, want := range []bool{true, true, false, false, false} {
		if got := isBrace(args[i+1]); got != want {
			t.Errorf("fish arg %d: brace expansion %v, want %v", i+1, got, want)
		}
	}
	f, err = Parse(`echo {$x}s`)
	if err != nil {
		t.Fatal(err)
	}
	if isBrace(f.Stmts[0].Cmd.(*CallExpr).Args[1]) {
		t.Errorf("sh: {$x}s should keep its braces")
	}
}

func TestParseFishIndirect(t *testing.T) {
	f, err := ParseOpts(`echo $$a[1]`, Options{Fish: true})
	if err != nil {
		t.Fatal(err)
	}
	pe, ok := f.Stmts[0].Cmd.(*CallExpr).Args[1].Parts[0].(*ParamExp)
	if !ok || pe.Name != "a" || !pe.Indirect || pe.Index == nil {
		t.Errorf("fish $$a[1]: got %+v", f.Stmts[0].Cmd.(*CallExpr).Args[1].Parts[0])
	}
	// in sh, $$ is the shell's pid
	f, err = Parse(`echo $$a`)
	if err != nil {
		t.Fatal(err)
	}
	if pe, ok := f.Stmts[0].Cmd.(*CallExpr).Args[1].Parts[0].(*ParamExp); !ok || pe.Name != "$" {
		t.Errorf("sh $$a: want $$ then a")
	}
}

func TestParseTildeInParamDefault(t *testing.T) {
	f, err := Parse("cd ${1:-~} ${x:=~root}")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range f.Stmts[0].Cmd.(*CallExpr).Args[1:] {
		pe := w.Parts[0].(*ParamExp)
		if _, ok := pe.Arg.Parts[0].(*Tilde); !ok {
			t.Errorf("${%s...}: got %T, want *Tilde", pe.Name, pe.Arg.Parts[0])
		}
	}
}

func TestParseRedirectFromProcSubst(t *testing.T) {
	f, err := Parse("while read l; do echo $l; done < <(ls)\ncat >(cat) 2> >(cat)")
	if err != nil {
		t.Fatal(err)
	}
	r := f.Stmts[0].Cmd.(*WhileClause).Redirs[0]
	if ps, ok := r.Target.Parts[0].(*ProcSubst); !ok || ps.Out {
		t.Errorf("< <(ls): got target %T", r.Target.Parts[0])
	}
	r = f.Stmts[1].Cmd.(*CallExpr).Redirs[0]
	if ps, ok := r.Target.Parts[0].(*ProcSubst); !ok || !ps.Out {
		t.Errorf("2> >(cat): got target %T", r.Target.Parts[0])
	}
}

func TestParseFunctionKeywordParens(t *testing.T) {
	for _, src := range []string{
		"function f() { echo; }",
		"function f(){\n  echo\n}",
		"function f ( )\n{ echo; }",
	} {
		f, err := Parse(src)
		if err != nil {
			t.Errorf("Parse(%q): %v", src, err)
			continue
		}
		fd := f.Stmts[0].Cmd.(*FuncDecl)
		if name, ok := fd.Name.IsUnquotedLit(); !ok || name != "f" || fd.Src != src {
			t.Errorf("Parse(%q): name %+v, src %q", src, fd.Name.Parts, fd.Src)
		}
		if _, ok := fd.BodyCmd.(*Block); !ok {
			t.Errorf("Parse(%q): body %T", src, fd.BodyCmd)
		}
	}
}

// The ]] of a bracket expression in a regex doesn't end the [[ test.
func TestParseRegexBrackets(t *testing.T) {
	for src, want := range map[string]string{
		`[[ $x =~ ^[[:digit:]]+$ ]]`:                "^[[:digit:]]+$",
		`[[ $x =~ [^[:space:]](.*[^[:space:]])? ]]`: "[^[:space:]](.*[^[:space:]])?",
		`[[ $x =~ a[]b] ]]`:                         "a[]b]",
		`[[ $x =~ a[ ]]`:                            "a[",
	} {
		f, err := Parse(src)
		if err != nil {
			t.Errorf("Parse(%q): %v", src, err)
			continue
		}
		rx := f.Stmts[0].Cmd.(*TestClause).Expr.(*TestBinary).Y.(*TestWord).W
		if got, _ := rx.LitString(); got != want {
			t.Errorf("Parse(%q): regex %q, want %q", src, got, want)
		}
	}
}

func TestParseTestPatternBrackets(t *testing.T) {
	for src, want := range map[string]string{
		`[[ $x == *[[:digit:]] ]]`: "*[[:digit:]]",
		`[[ $x == [[:alpha:]] ]]`:  "[[:alpha:]]",
		`[[ $x == [ab] ]]`:         "[ab]",
	} {
		f, err := Parse(src)
		if err != nil {
			t.Errorf("Parse(%q): %v", src, err)
			continue
		}
		pat := f.Stmts[0].Cmd.(*TestClause).Expr.(*TestBinary).Y.(*TestWord).W
		if got, _ := pat.LitString(); got != want {
			t.Errorf("Parse(%q): pattern %q, want %q", src, got, want)
		}
	}
}

// Like bash, ash only complains about a ${...} it can't make sense of, such
// as zsh's ${(f)x}, when it expands it, so a script can keep one in a branch
// that only runs under zsh.
func TestParseBadParamDeferred(t *testing.T) {
	src := "if [ -n \"$ZSH_VERSION\" ]; then\n  r=(${(f)\"$(cmd \"$x\")\"})\n  unset ${(M)${(k)parameters[@]}:#_x_*}\nfi\necho \"${x b}\""
	f, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pe := f.Stmts[1].Cmd.(*CallExpr).Args[1].Parts[0].(*DblQuoted).Parts[0].(*ParamExp)
	if text, _ := pe.Arg.LitString(); pe.Op != OpBad || text != "${x b}" {
		t.Errorf("got %+v", pe)
	}
	for _, src := range []string{"echo ${(f)x", "echo ${x b", "echo ${\n"} {
		if _, err := Parse(src); !IsIncomplete(err) {
			t.Errorf("Parse(%q): %v, want incomplete", src, err)
		}
	}
}

func TestParseANSICControl(t *testing.T) {
	f, err := Parse(`echo $'a\cAb' $'\c' x`)
	if err != nil {
		t.Fatal(err)
	}
	args := f.Stmts[0].Cmd.(*CallExpr).Args
	if len(args) != 4 {
		t.Fatalf("got %d args, want 4", len(args))
	}
	for i, want := range []string{"a\x01b", `\c`} {
		if s, _ := args[i+1].LitString(); s != want {
			t.Errorf("arg %d: got %q, want %q", i+1, s, want)
		}
	}
}

func TestParseBackgroundSeparates(t *testing.T) {
	f, err := Parse("server & sleep 1")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Stmts) != 2 || !f.Stmts[0].Background || f.Stmts[1].Background {
		t.Fatalf("got %d statements, want `server &` and `sleep 1`", len(f.Stmts))
	}
}

func TestFishIndexHeuristic(t *testing.T) {
	f, err := Parse(`echo "$x[0-9]" $y[1] $z[abc]`)
	if err != nil {
		t.Fatal(err)
	}
	args := f.Stmts[0].Cmd.(*CallExpr).Args
	dq := args[1].Parts[0].(*DblQuoted)
	if pe := dq.Parts[0].(*ParamExp); pe.Index != nil {
		t.Errorf("$x[0-9] should not be an index")
	}
	if pe := args[2].Parts[0].(*ParamExp); pe.Index == nil {
		t.Errorf("$y[1] should be an index")
	}
	if pe := args[3].Parts[0].(*ParamExp); pe.Index != nil {
		t.Errorf("$z[abc] should not be an index")
	}
}

func TestParseMultilineArith(t *testing.T) {
	f, err := Parse("x=$((\n  1 +\n  2\n))\n((\n  x++\n))")
	if err != nil {
		t.Fatal(err)
	}
	call := f.Stmts[0].Cmd.(*CallExpr)
	if _, ok := call.Assigns[0].Value.Parts[0].(*ArithExp); !ok {
		t.Errorf("$(( spanning lines: got %T, want *ArithExp", call.Assigns[0].Value.Parts[0])
	}
	if _, ok := f.Stmts[1].Cmd.(*ArithCmd); !ok {
		t.Errorf("(( spanning lines: got %T, want *ArithCmd", f.Stmts[1].Cmd)
	}
	// A subshell inside $( ) still isn't arithmetic.
	f, err = Parse("x=$((cd /tmp\n  ls) | cat)")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Stmts[0].Cmd.(*CallExpr).Assigns[0].Value.Parts[0].(*CmdSubst); !ok {
		t.Errorf("$((cd ...) | cat): want *CmdSubst")
	}
}

// The user's real fish configuration should parse.
func TestParseFishConfigs(t *testing.T) {
	home, _ := os.UserHomeDir()
	files, _ := filepath.Glob(filepath.Join(home, ".config/fish/functions/*.fish"))
	more, _ := filepath.Glob(filepath.Join(home, ".config/fish/conf.d/*.fish"))
	files = append(files, more...)
	files = append(files, filepath.Join(home, ".config/fish/config.fish"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if _, err := Parse(string(b)); err != nil {
			e := err.(*Error)
			line, col := LineCol(string(b), e.Pos)
			t.Errorf("%s:%d:%d: %v", f, line, col, err)
		}
	}
}
