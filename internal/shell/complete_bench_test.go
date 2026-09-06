package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// bigDir makes a directory with many files.
func bigDir(t testing.TB, dir string, n int) {
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%04d.txt", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n/20; i++ {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("dir%03d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func realPathShell(t testing.TB) (*Shell, string) {
	sh := New()
	dir := t.TempDir()
	sh.dir = dir
	for _, v := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		sh.SetVar(v, []string{t.TempDir()}, setOpts{scope: setGlobal, export: 1})
	}
	sh.Init()
	return sh, dir
}

func TestCompleteSpeed(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	sh, dir := realPathShell(t)
	bigDir(t, dir, 2000)
	cases := []string{"cat file1", "cat ", "cat fl99", "gi", "ls -l dir0", "echo $PA"}
	for _, line := range cases {
		sh.Complete(line, len(line)) // warm up caches
		const n = 20
		start := time.Now()
		for i := 0; i < n; i++ {
			sh.Complete(line, len(line))
		}
		avg := time.Since(start) / n
		t.Logf("%-12q %v", line, avg)
		// generous bound: catch pathological slowness, not noise
		if avg > 50*time.Millisecond {
			t.Errorf("%q takes %v", line, avg)
		}
	}
}

func BenchmarkCompleteFiles(b *testing.B) {
	sh, dir := realPathShell(b)
	bigDir(b, dir, 2000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sh.Complete("cat file1", 9)
	}
}

func BenchmarkCompleteFilesCold(b *testing.B) {
	sh, dir := realPathShell(b)
	bigDir(b, dir, 2000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sh.shared.complete.dirs = dirCache{}
		sh.Complete("cat file1", 9)
	}
}

func BenchmarkCompleteCommands(b *testing.B) {
	sh, _ := realPathShell(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sh.Complete("gi", 2)
	}
}

func BenchmarkCompleteEmptyCommand(b *testing.B) {
	sh, _ := realPathShell(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sh.Complete("", 0)
	}
}
