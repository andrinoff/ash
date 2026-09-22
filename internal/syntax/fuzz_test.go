package syntax

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// FuzzParse checks that no input makes the parser panic or hang, and that
// error positions point into the source.
//
//	go test -fuzz=FuzzParse -fuzztime=120s ./internal/syntax
func FuzzParse(f *testing.F) {
	seeds := []string{
		"echo hello world",
		"ls -la | grep foo | wc -l",
		"a && b || c; d & e &! f",
		"FOO=bar BAZ=qux env",
		"arr=(a b 'c d') a[1+2]=x b+=y",
		"echo $x ${y:-default} ${#z} ${a/b/c} ${a//b} ${a:1:2} ${!ref} ${arr[@]} ${arr[1]}",
		"echo ${x@Q} ${!pre*} ${!arr[@]} ${x^^} ${x,} ${x#*/} ${x%%.*} ${x/#a/b} ${x/%a/b}",
		`echo "hello $USER, it's $(date +%H)" '$not' $'tab\there' $"x"`,
		"echo `pwd` \"`echo \\`x\\``\"",
		"if [ -f x ]; then echo yes; elif true; then :; else echo no; fi",
		"if test -f x; echo yes; else if true; echo maybe; else; echo no; end",
		"if test a; and test b; or test c\n  echo x\nend",
		"while read line; do echo $line; done < file",
		"until false; do break; done",
		"for i in 1 2 3; do echo $i; done; for i; do :; done",
		"for i in 1 2 3; echo $i; end",
		"for ((i=0; i<10; i++)); do echo $i; done",
		"case $x in a|b) echo ab;; (c) echo c;& *) echo other;;& esac",
		"switch $x\n case a b\n  echo ab\n case '*'\n  echo other\nend",
		"function foo -d 'desc' --on-variable PWD\n  echo hi\nend",
		"foo() { echo hi; }\nfunction bar() ( echo hi )",
		"begin; echo a; end > out; { echo a; echo b; } 2>&1",
		"(cd /tmp && ls) | (cat)",
		"echo (pwd)/x (seq 3)[1] $argv[2..-1] $argv[(math $i + 1)..]",
		"echo {a,b,c}{1..3} {a..z..2} ~ ~/x ~root x=~/a:~/b",
		"cat <<EOF\nhello $USER\nEOF\necho after",
		"cat <<'EOF' <<-\"X\"\n$not expanded\nEOF\n\tx\n\tX",
		"echo foo 2>&1 >/dev/null &>log &>>log 3<>f 4>|g 5>?h <&- >&2",
		"cmd <<< 'herestring'; diff <(ls a) >(cat)",
		"[[ -f x && $a == b* || ! $c =~ ^(a|b)$ ]] && [[ ( a < b ) ]]",
		"(( i++ )); echo $(( 1 + 2 * (3 - 1) )) $((x[1]))",
		"! true; not true; and echo x; or echo y",
		"time -p ls; time",
		"echo a |& cat; echo a &| cat; echo a 2>| cat",
		"echo a#b # comment\n# just a comment",
		"echo \\\n  continued \\\n  # comment\n  more",
		"x=$(cat <<EOF\nfoo\nEOF\n)",
		"echo \"$(echo \"nested 'quotes' $(echo deep)\")\"",
		"echo 'abc",
		"if true; then",
		"echo ${x",
		"case x in",
		")",
		"fi",
		"echo $'\\x41\\u00e9\\U0001F600\\cA\\101'",
		"echo \\x41 \\u00e9 \\cA",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			for _, opts := range []Options{{}, {Fish: true}, {Lenient: true}} {
				f, err := ParseOpts(src, opts)
				if err == nil {
					if path := findNil(reflect.ValueOf(f), "File"); path != "" {
						t.Errorf("ParseOpts(%q, %+v): nil node at %s", src, opts, path)
					}
					continue
				}
				e, ok := err.(*Error)
				if !ok {
					t.Errorf("ParseOpts(%q, %+v): error of type %T", src, opts, err)
					continue
				}
				if e.Pos < 0 || e.Pos > len(src) {
					t.Errorf("ParseOpts(%q, %+v): error position %d out of range: %v", src, opts, e.Pos, e)
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("Parse(%q) did not finish", src)
		}
	})
}

// findNil returns the path to a nil element of a slice, a nil Stmt.Cmd or
// a nil operand of a BinaryCmd, which the interpreter can't run.
func findNil(v reflect.Value, path string) string {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return ""
		}
		return findNil(v.Elem(), path)
	case reflect.Struct:
		if v.CanAddr() {
			switch n := v.Addr().Interface().(type) {
			case *Stmt:
				if n.Cmd == nil {
					return path + ".Cmd"
				}
			case *BinaryCmd:
				if n.X == nil || n.Y == nil {
					return path + ".X/Y"
				}
			}
		}
		for i := 0; i < v.NumField(); i++ {
			name := v.Type().Field(i).Name
			if name == "DeclArgs" {
				// nil for the arguments that aren't assignments
				for j := 0; j < v.Field(i).Len(); j++ {
					if p := findNil(v.Field(i).Index(j), fmt.Sprintf("%s.DeclArgs[%d]", path, j)); p != "" {
						return p
					}
				}
				continue
			}
			if p := findNil(v.Field(i), path+"."+name); p != "" {
				return p
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			e := v.Index(i)
			p := fmt.Sprintf("%s[%d]", path, i)
			if (e.Kind() == reflect.Ptr || e.Kind() == reflect.Interface) && e.IsNil() {
				return p
			}
			if p := findNil(e, p); p != "" {
				return p
			}
		}
	}
	return ""
}
