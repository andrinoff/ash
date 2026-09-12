package history

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFuzzyMatch(t *testing.T) {
	// Scores worked out by hand: 16 per rune, word starts after a space
	// +10 (doubled for the first rune), camelCase humps +7, +8 for starting
	// the command, runs of matches keep the bonus of their first rune (at
	// least +4), and a gap costs 3 plus 1 per rune after the first.
	tests := []struct {
		q, text string
		ok      bool
		score   int
		pos     []int
	}{
		{"gst", "git status", true, 91, []int{0, 4, 5}},
		{"gco", "git checkout main", true, 75, []int{0, 4, 9}},
		{"doc", "docker compose up -d", true, 96, []int{0, 1, 2}},
		// The first "a" and "b" are a worse match than the last two.
		{"ab", "xa_b ab", true, 62, []int{5, 6}},
		{"fb", "fooBar", true, 63, []int{0, 3}},
		// A long gap costs no more than the match had scored before it.
		{"ui", "curl -fsSL https://example.com/install.sh", true, 25, []int{1, 31}},
		{"FB", "fooBar", false, 0, nil},
		{"FB", "FooBar", true, 63, []int{0, 3}},
		// Positions count runes, not bytes.
		{"e", "grüße", true, 16, []int{4}},
		{"ü", "GRÜSSE", true, 16, []int{2}},
		// The Kelvin sign lower-cases to an ASCII k.
		{"k", "Kelvin", true, 44, []int{0}},
		{"zz", "git status", false, 0, nil},
		{"gits", "git", false, 0, nil},
	}
	for _, tt := range tests {
		m := newFuzzyMatcher(tt.q)
		score, pos, ok := m.match(tt.text, true)
		if ok != tt.ok || score != tt.score || !reflect.DeepEqual(pos, tt.pos) {
			t.Errorf("match(%q, %q) = %d %v %v, want %d %v %v", tt.q, tt.text, score, pos, ok, tt.score, tt.pos, tt.ok)
		}
		if score2, _, _ := m.match(tt.text, false); score2 != score {
			t.Errorf("match(%q, %q) scores %d without positions, %d with", tt.q, tt.text, score2, score)
		}
	}
}

func TestFuzzyPositionsMatchQuery(t *testing.T) {
	cmds := []string{
		"git commit --amend --no-edit",
		"kubectl get pods -n kube-system",
		"find . -name '*.go' | xargs grep -n TODO",
		"for f in *.jpg\n  convert $f ${f%.jpg}.png\nend",
		"echo 'naïve café' | iconv -f utf-8",
		"cd ~/src/github.com/andrinoff/ash",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaab",
	}
	queries := []string{"g", "gc", "gcm", "kgp", "ks", "fnx", "todo", "cvt", "cafe", "café", "ïv", "ash", "src/ash", "ab", "aab", "a b", "TODO", "-n"}
	for _, q := range queries {
		m := newFuzzyMatcher(q)
		qr := []rune(q)
		for _, c := range cmds {
			_, pos, ok := m.match(c, true)
			if !ok {
				if isSubsequence(qr, []rune(c), m.fold) {
					t.Errorf("%q doesn't match %q", q, c)
				}
				continue
			}
			runes := []rune(c)
			if len(pos) != len(qr) {
				t.Fatalf("match(%q, %q): %d positions", q, c, len(pos))
			}
			for i, p := range pos {
				if p < 0 || p >= len(runes) || i > 0 && p <= pos[i-1] {
					t.Fatalf("match(%q, %q): bad positions %v", q, c, pos)
				}
				r, want := runes[p], qr[i]
				if m.fold {
					r, want = foldRune(r), foldRune(want)
				}
				if r != want {
					t.Errorf("match(%q, %q): position %d is %q, want %q", q, c, p, runes[p], qr[i])
				}
			}
		}
	}
}

func isSubsequence(q, text []rune, fold bool) bool {
	i := 0
	for _, r := range text {
		if i < len(q) && (r == q[i] || fold && foldRune(r) == foldRune(q[i])) {
			i++
		}
	}
	return i == len(q)
}

func TestFuzzyRanking(t *testing.T) {
	s := Open("")
	add(t, s, "git status", "digit", "sudo git stash", "go test ./...", "git stash")
	all := []string{"git stash", "git status", "sudo git stash", "go test ./..."}
	scores := []int{91, 91, 83, 74}
	for _, limit := range []int{0, -1, 1, 2, 3, 4, 10} {
		want := all
		if limit > 0 && limit < len(all) {
			want = all[:limit]
		}
		got := s.Fuzzy("gst", limit)
		var cmds []string
		for i, m := range got {
			cmds = append(cmds, m.Cmd)
			if m.Score != scores[i] {
				t.Errorf("limit %d: %q scored %d, want %d", limit, m.Cmd, m.Score, scores[i])
			}
		}
		equal(t, fmt.Sprintf("Fuzzy(gst, %d)", limit), cmds, want)
	}
	m := s.Fuzzy("gst", 1)[0]
	if !reflect.DeepEqual(m.Positions, []int{0, 4, 5}) || m.When.Unix() != 1700000004 {
		t.Errorf("got %+v", m)
	}
	if got := s.Fuzzy("xyz", 0); len(got) != 0 {
		t.Errorf("Fuzzy(xyz) = %+v", got)
	}
}

// TestFuzzyLikeFzf checks rankings against fzf 0.74's
// (fzf --filter=Q --tiebreak=index, given the commands newest first). Every
// command starts with "> " so that the prefix bonus, which fzf doesn't
// have, never applies.
func TestFuzzyLikeFzf(t *testing.T) {
	newestFirst := []string{
		"> make build test",
		"> cmake --build target",
		"> ember test",
		"> xmb_t mbt",
		"> sudo make bt",
		"> docker compose up -d",
		"> docker ps -a",
		"> kubectl get pods -n kube-system",
		"> kubectl describe pod web-0",
		"> cd ~/src/andrinoff/ash",
		"> cd /tmp",
		"> ls -la",
		"> find . -name '*.go' | xargs grep -n TODO",
		"> grep -rn FIXME src",
		"> go test ./...",
		"> go build -o bin/ash .",
		"> go vet ./internal/history",
		"> npm run build",
		"> npm install --save-dev typescript",
		"> cargo build --release",
		"> cargo test -- --nocapture",
		"> python3 -m venv .venv",
		"> source .venv/bin/activate.fish",
		"> brew upgrade",
		"> brew install fzf zoxide starship",
		"> vim ~/.config/fish/config.fish",
		"> nvim internal/history/fuzzy.go",
		"> ssh deploy@example.com",
		"> scp build.tar.gz deploy@example.com:/srv",
		"> tar -xzf build.tar.gz",
		"> curl -fsSL https://example.com/install.sh",
		"> echo $PATH | tr : \\n",
		"> export EDITOR=nvim",
		"> mise use node@22",
		"> zoxide query src",
		"> starship explain",
		"> eza -la --tree",
		"> bat README.md",
		"> fd -e go history",
		"> rg -n Suggest internal",
	}
	s := Open("")
	for i := len(newestFirst) - 1; i >= 0; i-- {
		add(t, s, newestFirst[i])
	}
	tests := []struct {
		q    string
		want []string
	}{
		{"mbt", []string{"> xmb_t mbt", "> sudo make bt", "> make build test", "> ember test", "> cmake --build target"}},
		{"ui", []string{
			"> make build test", "> cmake --build target", "> go build -o bin/ash .", "> npm run build",
			"> cargo build --release", "> scp build.tar.gz deploy@example.com:/srv", "> tar -xzf build.tar.gz",
			"> rg -n Suggest internal", "> curl -fsSL https://example.com/install.sh",
			"> kubectl describe pod web-0", "> source .venv/bin/activate.fish",
		}},
		{"kgp", []string{"> kubectl get pods -n kube-system"}},
		{"gotst", []string{"> go test ./...", "> go vet ./internal/history", "> cargo test -- --nocapture"}},
		{"bld", []string{
			"> make build test", "> go build -o bin/ash .", "> npm run build", "> cargo build --release",
			"> scp build.tar.gz deploy@example.com:/srv", "> tar -xzf build.tar.gz", "> cmake --build target",
			"> kubectl describe pod web-0", "> brew install fzf zoxide starship", "> kubectl get pods -n kube-system",
		}},
		{"hist", []string{"> fd -e go history", "> go vet ./internal/history", "> nvim internal/history/fuzzy.go", "> curl -fsSL https://example.com/install.sh"}},
		{"Rgs", nil},
	}
	for _, tt := range tests {
		var got []string
		for _, m := range s.Fuzzy(tt.q, 0) {
			got = append(got, m.Cmd)
		}
		equal(t, fmt.Sprintf("Fuzzy(%q)", tt.q), got, tt.want)
	}
}

func TestFuzzySmartCase(t *testing.T) {
	s := Open("")
	add(t, s, "Makefile edit", "make build")
	var got []string
	for _, m := range s.Fuzzy("make", 0) {
		got = append(got, m.Cmd)
	}
	equal(t, "Fuzzy(make)", got, []string{"make build", "Makefile edit"})
	got = nil
	for _, m := range s.Fuzzy("Make", 0) {
		got = append(got, m.Cmd)
	}
	equal(t, "Fuzzy(Make)", got, []string{"Makefile edit"})
}

func TestFuzzyEmptyQuery(t *testing.T) {
	s := Open("")
	add(t, s, "a", "b", "c")
	got := s.Fuzzy("", 2)
	if len(got) != 2 || got[0].Cmd != "c" || got[1].Cmd != "b" || got[0].Score != 0 || got[0].Positions != nil {
		t.Errorf("Fuzzy(\"\", 2) = %+v", got)
	}
	if got := s.Fuzzy("", 0); len(got) != 3 {
		t.Errorf("Fuzzy(\"\", 0) returned %d entries", len(got))
	}
}

func TestFuzzyLongCommand(t *testing.T) {
	// Too big to search fully: scored greedily.
	long := strings.Repeat("ab", 70000)
	tests := []struct {
		q, text string
		score   int
		pos     []int
	}{
		{"ab", long, 70, []int{0, 1}},
		// The "a" right before the "c" beats the one at the start: a
		// gap of 139999 would cost all its 44.
		{"ac", long + "c", 29, []int{139998, 140000}},
	}
	for _, tt := range tests {
		s := Open("")
		if err := s.Add(Entry{Cmd: tt.text, When: time.Unix(1, 0)}); err != nil {
			t.Fatal(err)
		}
		got := s.Fuzzy(tt.q, 1)
		if len(got) != 1 {
			t.Errorf("Fuzzy(%q): %d matches, want 1", tt.q, len(got))
			continue
		}
		if got[0].Score != tt.score || !reflect.DeepEqual(got[0].Positions, tt.pos) {
			t.Errorf("Fuzzy(%q): score %d, positions %v; want %d %v", tt.q, got[0].Score, got[0].Positions, tt.score, tt.pos)
		}
	}
}

// TestFuzzyLongQuery searches with queries longer than an int16 run of
// consecutive matches, as when a long command line is the query.
func TestFuzzyLongQuery(t *testing.T) {
	for _, n := range []int{32767, 32768, 40000} {
		q := strings.Repeat("a", n)
		for _, text := range []string{q, "x " + q + " y"} {
			m := newFuzzyMatcher(q)
			score, pos, ok := m.match(text, true)
			if !ok || len(pos) != n {
				t.Fatalf("%d runes: ok %v, %d positions", n, ok, len(pos))
			}
			first := strings.Index(text, "a")
			for i, p := range pos {
				if p != first+i {
					t.Fatalf("%d runes: position %d is %d, want %d", n, i, p, first+i)
				}
			}
			if score2, _, _ := m.match(text, false); score2 != score {
				t.Errorf("%d runes: scores %d without positions, %d with", n, score2, score)
			}
		}
	}
}

func TestClassOf(t *testing.T) {
	tests := []struct {
		r    rune
		want charClass
	}{
		{'a', charLower}, {'Z', charUpper}, {'7', charNumber}, {' ', charWhite}, {'\t', charWhite},
		{'/', charDelimiter}, {':', charDelimiter}, {'|', charDelimiter}, {'-', charNonWord}, {'.', charNonWord},
		{'é', charLower}, {'Ж', charUpper}, {'٣', charNumber}, {'の', charLetter}, {'　', charWhite}, {'→', charNonWord},
	}
	for _, tt := range tests {
		if got := classOf(tt.r); got != tt.want {
			t.Errorf("classOf(%q) = %d, want %d", tt.r, got, tt.want)
		}
	}
}
