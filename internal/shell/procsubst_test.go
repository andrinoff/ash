package shell

import "testing"

func TestProcSubst(t *testing.T) {
	out, errOut, st := runScript(t, `cat <(echo a) <(echo b)`)
	if out != "a\nb\n" || st != 0 {
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
