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
