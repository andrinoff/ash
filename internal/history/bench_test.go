package history

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var benchDirs = []string{"/home/me", "/home/me/src/ash", "/home/me/src/web", "/tmp", "/home/me/notes"}

// genCommands returns n plausible commands, distinct unless unique is
// smaller than n.
func genCommands(n, unique int) []string {
	verbs := []string{
		"git status", "git commit -m", "go test", "go build -o bin/ash", "make",
		"docker compose up", "kubectl get pods -n", "cd ~/src/project", "npm run",
		"cargo build --release", "ls -la", "nvim", "rg -n", "fd -e go", "ssh deploy@host",
		"curl -fsSL https://example.com/api/v", "echo", "brew install", "python3 -m", "tar -xzf",
	}
	args := []string{"./...", "main.go", "README.md", "--verbose", "kube-system", "build", "test", "release", "src/lib", "internal/history"}
	out := make([]string, n)
	for i := range out {
		k := i % unique
		out[i] = fmt.Sprintf("%s %s %d", verbs[k%len(verbs)], args[(k/len(verbs))%len(args)], k)
	}
	return out
}

// writeHistory writes a history file of n entries with the given number of
// distinct commands.
func writeHistory(tb testing.TB, n, unique int) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "history")
	buf := []byte(header)
	for i, cmd := range genCommands(n, unique) {
		it := item{cmd: cmd, dir: benchDirs[i%len(benchDirs)], when: 1700000000 + int64(i), dur: int64(i % 5000)}
		if i%7 == 0 {
			it.status = 1
		}
		buf = appendRecord(buf, &it)
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		tb.Fatal(err)
	}
	return path
}

func loaded(tb testing.TB, n, unique int) *Store {
	tb.Helper()
	s := Open(writeHistory(tb, n, unique))
	if err := s.Err(); err != nil {
		tb.Fatal(err)
	}
	if s.Len() != unique {
		tb.Fatalf("Len = %d, want %d", s.Len(), unique)
	}
	return s
}

// BenchmarkOpen measures reading a history file of 100k entries.
func BenchmarkOpen(b *testing.B) {
	for _, bc := range []struct {
		name      string
		n, unique int
	}{
		{"100k", 100_000, 100_000},
		{"100k_20k_distinct", 100_000, 20_000},
	} {
		b.Run(bc.name, func(b *testing.B) {
			path := writeHistory(b, bc.n, bc.unique)
			if fi, err := os.Stat(path); err == nil {
				b.SetBytes(fi.Size())
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s := Open(path)
				s.Wait()
				if s.Len() != bc.unique {
					b.Fatalf("Len = %d", s.Len())
				}
			}
		})
	}
}

// BenchmarkSuggest measures autosuggestions over 100k entries.
func BenchmarkSuggest(b *testing.B) {
	s := loaded(b, 100_000, 100_000)
	// Entry i runs verb i%20 in benchDirs[i%5]; "rg -n" is verb 12, so it
	// always ran in benchDirs[2].
	for _, bc := range []struct{ name, prefix, dir, want string }{
		{"found_at_once", "rg -n", benchDirs[2], "rg -n internal/history 99992"},
		// These scan everything, 10% of the entries matching the prefix in
		// the first. 99981 is the newest git command, but it failed.
		{"no_dir_match", "git", "/elsewhere", "git status internal/history 99980"},
		{"long_prefix", "kubectl get pods -n kube", "/elsewhere", "kubectl get pods -n kube-system 99886"},
		{"no_match", "zzz", "/home/me", ""},
	} {
		if got := s.Suggest(bc.prefix, bc.dir); got != bc.want {
			b.Fatalf("Suggest(%q, %q) = %q, want %q", bc.prefix, bc.dir, got, bc.want)
		}
		b.Run(bc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				s.Suggest(bc.prefix, bc.dir)
			}
		})
	}
}

// BenchmarkFuzzy measures the interactive history search over 100k
// entries, keeping the best 50.
func BenchmarkFuzzy(b *testing.B) {
	s := loaded(b, 100_000, 100_000)
	for _, q := range []string{"g", "gst", "kgpkube", "docker up", "zzzz"} {
		b.Run(q, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				s.Fuzzy(q, 50)
			}
		})
	}
}

// BenchmarkSearch measures history search over 100k entries.
func BenchmarkSearch(b *testing.B) {
	s := loaded(b, 100_000, 100_000)
	for _, bc := range []struct {
		name, q string
		prefix  bool
	}{
		{"substring", "kube", false},
		{"prefix", "git", true},
		{"case_sensitive", "README", false},
	} {
		b.Run(bc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				s.Search(bc.q, bc.prefix)
			}
		})
	}
}

// BenchmarkAdd measures adding a command, including the write to the file.
func BenchmarkAdd(b *testing.B) {
	s := loaded(b, 100_000, 100_000)
	cmds := genCommands(1000, 1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Add(Entry{Cmd: cmds[i%len(cmds)], When: time.Unix(1800000000, 0), Dir: "/tmp"}); err != nil {
			b.Fatal(err)
		}
	}
}
