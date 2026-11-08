package shell

import "testing"

func BenchmarkWhileLoop(b *testing.B) {
	for i := 0; i < b.N; i++ {
		runScriptB(b, `i=0; while [ $i -lt 2000 ]; do i=$((i+1)); done`)
	}
}

func BenchmarkFishLoop(b *testing.B) {
	for i := 0; i < b.N; i++ {
		runScriptB(b, `for i in (seq 1000); set x $i; end`)
	}
}

func runScriptB(b *testing.B, src string) {
	sh := New()
	sh.Init()
	io := &IO{In: &Stream{}, Out: &Stream{W: &lockedBuffer{}}, Err: &Stream{W: &lockedBuffer{}}}
	sh.RunString(src, "bench", io)
}

func BenchmarkFishMathSubst(b *testing.B) {
	for i := 0; i < b.N; i++ {
		runScriptB(b, `set i 0; while test $i -lt 2000; set i (math $i + 1); end`)
	}
}

func BenchmarkFishStringPipe(b *testing.B) {
	for i := 0; i < b.N; i++ {
		runScriptB(b, `set p /usr/local/share/doc/README.md; for i in (seq 200); set n (string length -- $p $p | string join +); end`)
	}
}
