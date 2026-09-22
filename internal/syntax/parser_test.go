package syntax

import (
	"os"
	"path/filepath"
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
		{"echo a\ncat <<EOF\nx\n${y\nEOF\necho after", 4, 4},
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
