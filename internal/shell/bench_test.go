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
