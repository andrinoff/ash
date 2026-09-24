package shell

import "testing"

func TestProcSubst(t *testing.T) {
	out, errOut, st := runScript(t, `cat <(echo a) <(echo b)`)
	if out != "a\nb\n" || st != 0 {
		t.Fatalf("got %q %q %d", out, errOut, st)
	}
}

func TestRedirectFromProcSubst(t *testing.T) {
	out, errOut, st := runScript(t, `n=0; while read l; do n=$((n+1)); echo "got $l"; done < <(echo a; echo b); echo $n`)
	if out != "got a\ngot b\n2\n" || st != 0 {
		t.Fatalf("got %q %q %d", out, errOut, st)
	}
}

func TestPathListParamOps(t *testing.T) {
	out, _, _ := runScript(t, `PATH=/a/bin:/b/bin:/c/bin; echo ${PATH%%:*} ${PATH#*:} ${PATH//bin/x}`)
	if out != "/a/bin /b/bin:/c/bin /a/x:/b/x:/c/x\n" {
		t.Fatalf("got %q", out)
	}
}

func TestBracketIsNotAGlob(t *testing.T) {
	if isGlobPattern("[") || isGlobPattern("]") || isGlobPattern("a[b") {
		t.Fatal("lone brackets must not glob")
	}
	if !isGlobPattern("[ab]") || !isGlobPattern("x*") || !isGlobPattern("[]a]") {
		t.Fatal("real patterns must glob")
	}
}

func TestReadKeepsWhitespaceWithEmptyIFS(t *testing.T) {
	out, _, _ := runScriptIn(t, `IFS= read -r line; echo "[$line]"; read -r b; echo "[$b]"`, "  indented  \n  trimmed  \n")
	if out != "[  indented  ]\n[trimmed]\n" {
		t.Fatalf("got %q", out)
	}
}

func TestFishSubstLines(t *testing.T) {
	out, _, _ := runScript(t, `count (echo); count (printf ''); count (printf 'a\n\nb\n')`)
	if out != "1\n0\n3\n" {
		t.Fatalf("got %q", out)
	}
}
