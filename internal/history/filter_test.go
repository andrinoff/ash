package history

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

// scoped returns a history of commands run in several directories, some
// of which failed.
func scoped(t *testing.T) *Store {
	t.Helper()
	s := Open("")
	for i, e := range []Entry{
		{Cmd: "git status", Dir: "/src/ash"},
		{Cmd: "go test ./...", Dir: "/src/ash", Status: 1},
		{Cmd: "go vet ./internal/history", Dir: "/src/ash/internal/history"},
		{Cmd: "git stash", Dir: "/src/ash2"},
		{Cmd: "make test", Dir: "/src/web", Status: 2},
		{Cmd: "git push", Dir: "/src/web"},
		{Cmd: "ls", Dir: ""},
		{Cmd: "gst", Dir: "/"},
		{Cmd: "sudo git stash", Dir: "/src/ash/docs", Status: 130},
		{Cmd: "go build -o bin/ash .", Dir: "/src/ash"},
	} {
		e.When = time.Unix(1700000000+int64(i), 0)
		if err := s.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func cmds(ms []Match) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Cmd)
	}
	return out
}

func TestFilters(t *testing.T) {
	s := scoped(t)
	tests := []struct {
		name string
		keep func(Entry) bool
		want []string
	}{
		{"InDir", InDir("/src/ash"), []string{"go build -o bin/ash .", "go test ./...", "git status"}},
		{"InDir elsewhere", InDir("/nowhere"), nil},
		// Not /src/ash2, which only starts with the same letters.
		{"Under", Under("/src/ash"), []string{
			"go build -o bin/ash .", "sudo git stash", "go vet ./internal/history", "go test ./...", "git status",
		}},
		{"Under with a slash", Under("/src/ash/"), []string{
			"go build -o bin/ash .", "sudo git stash", "go vet ./internal/history", "go test ./...", "git status",
		}},
		{"Under a leaf", Under("/src/ash/docs"), []string{"sudo git stash"}},
		// Everything that ran somewhere known.
		{"Under /", Under("/"), []string{
			"go build -o bin/ash .", "sudo git stash", "gst", "git push", "make test", "git stash",
			"go vet ./internal/history", "go test ./...", "git status",
		}},
		{"Failed", Failed, []string{"sudo git stash", "make test", "go test ./..."}},
	}
	for _, tt := range tests {
		equal(t, tt.name, cmds(s.FuzzyFilter("", 0, tt.keep)), tt.want)
	}
}

// TestFuzzyFilterRanksLikeFuzzy checks that FuzzyFilter returns what
// Fuzzy does, less the entries keep turns down: the same matches, scores,
// positions and order.
func TestFuzzyFilterRanksLikeFuzzy(t *testing.T) {
	s := scoped(t)
	filters := []struct {
		name string
		keep func(Entry) bool
	}{
		{"nil", nil},
		{"everything", func(Entry) bool { return true }},
		{"nothing", func(Entry) bool { return false }},
		{"InDir", InDir("/src/ash")},
		{"Under", Under("/src/ash")},
		{"Failed", Failed},
	}
	for _, f := range filters {
		for _, q := range []string{"", "g", "gst", "go", "git st", "test", "ash", "zzz", "GIT"} {
			all := s.Fuzzy(q, 0)
			for _, limit := range []int{0, -1, 1, 2, 3, 100} {
				var want []Match
				for _, m := range all {
					if f.keep == nil || f.keep(m.Entry) {
						want = append(want, m)
					}
				}
				if limit > 0 && len(want) > limit {
					want = want[:limit]
				}
				got := s.FuzzyFilter(q, limit, f.keep)
				if (len(got) != 0 || len(want) != 0) && !reflect.DeepEqual(got, want) {
					t.Errorf("FuzzyFilter(%q, %d, %s) = %+v, want %+v", q, limit, f.name, got, want)
				}
			}
		}
	}
}

func TestFuzzyFilterRanking(t *testing.T) {
	s := scoped(t)
	// The scores TestFuzzyRanking expects. "gst" (96) and "git stash" (91,
	// newer than "git status") would come first, but they ran in / and in
	// /src/ash2.
	got := s.FuzzyFilter("gst", 0, Under("/src/ash"))
	want := []struct {
		cmd   string
		score int
		pos   []int
	}{
		{"git status", 91, []int{0, 4, 5}},
		{"sudo git stash", 83, []int{5, 9, 10}},
		{"go test ./...", 74, []int{0, 5, 6}},
		{"go vet ./internal/history", 59, []int{0, 20, 21}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %q", cmds(got))
	}
	for i, w := range want {
		if got[i].Cmd != w.cmd || got[i].Score != w.score || !reflect.DeepEqual(got[i].Positions, w.pos) {
			t.Errorf("match %d = %q %d %v, want %q %d %v", i, got[i].Cmd, got[i].Score, got[i].Positions, w.cmd, w.score, w.pos)
		}
	}
	m := s.FuzzyFilter("gst", 1, Failed)
	if len(m) != 1 || m[0].Cmd != "sudo git stash" || m[0].Status != 130 || m[0].Dir != "/src/ash/docs" || m[0].When.Unix() != 1700000008 {
		t.Errorf("FuzzyFilter(gst, 1, Failed) = %+v", m)
	}
}

func TestFuzzyFilterEmptyQuery(t *testing.T) {
	s := scoped(t)
	equal(t, "limit 2", cmds(s.FuzzyFilter("", 2, Under("/src"))), []string{"go build -o bin/ash .", "sudo git stash"})
	equal(t, "limit 1", cmds(s.FuzzyFilter("", 1, Failed)), []string{"sudo git stash"})
	if got := s.FuzzyFilter("", 0, func(Entry) bool { return false }); len(got) != 0 {
		t.Errorf("a filter that keeps nothing found %q", cmds(got))
	}
}

// TestFuzzyFilterAllocs checks that the entries FuzzyFilter passes over,
// whether keep turns them down or they don't match, cost no allocations.
func TestFuzzyFilterAllocs(t *testing.T) {
	allocs := func(others int) float64 {
		s := Open("")
		when := time.Unix(1700000000, 0)
		for i := 0; i < others; i++ {
			s.Add(Entry{Cmd: fmt.Sprintf("git status %d", i), Dir: "/elsewhere", When: when})
			s.Add(Entry{Cmd: fmt.Sprintf("ls -la %d", i), Dir: "/here", When: when})
		}
		for _, c := range []string{"git status", "git stash", "gist"} {
			s.Add(Entry{Cmd: c, Dir: "/here", When: when})
		}
		keep := InDir("/here")
		if got := s.FuzzyFilter("gst", 10, keep); len(got) != 3 {
			t.Fatalf("got %q", cmds(got))
		}
		return testing.AllocsPerRun(20, func() { s.FuzzyFilter("gst", 10, keep) })
	}
	if few, many := allocs(10), allocs(2000); few != many {
		t.Errorf("FuzzyFilter allocates %v times next to 20 other entries and %v times next to 4000", few, many)
	}
}

func TestFuzzyFilterConcurrent(t *testing.T) {
	s := Open("")
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				dir := fmt.Sprintf("/d%d", g)
				s.Add(Entry{Cmd: fmt.Sprintf("git commit -m %d-%d", g, i), Dir: dir, Status: i % 2})
				for _, m := range s.FuzzyFilter("gcm", 5, InDir(dir)) {
					if m.Dir != dir {
						t.Errorf("searching %s found %q from %s", dir, m.Cmd, m.Dir)
					}
				}
				s.FuzzyFilter("", 5, Failed)
			}
		}()
	}
	wg.Wait()
	if n := len(s.FuzzyFilter("", 0, Failed)); n != 400 {
		t.Errorf("%d failed commands, want 400", n)
	}
}

// BenchmarkFuzzyFilter measures the history search narrowed to a
// directory, a repository and failed commands over 100k entries, keeping
// the best 50, next to the search over everything.
func BenchmarkFuzzyFilter(b *testing.B) {
	s := loaded(b, 100_000, 100_000)
	// Entry i ran in benchDirs[i%5] and failed if i%7 == 0.
	scopes := []struct {
		name string
		keep func(Entry) bool
	}{
		{"all", nil},
		{"dir", InDir(benchDirs[1])},    // a fifth of the entries
		{"repo", Under("/home/me/src")}, // two fifths
		{"failed", Failed},              // a seventh
	}
	for _, sc := range scopes {
		for _, q := range []string{"", "g", "gst", "docker up", "zzzz"} {
			name := q
			if q == "" {
				name = "empty"
			}
			b.Run(sc.name+"/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					s.FuzzyFilter(q, 50, sc.keep)
				}
			})
		}
	}
}
