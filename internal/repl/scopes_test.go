package repl

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/shell"
)

// tempTree returns a new temporary directory with symlinks resolved, and
// skips the test if a repository contains it.
func tempTree(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if root := findRepo(base); root != "" {
		t.Skipf("the temporary directory is in a repository at %s", root)
	}
	return base
}

func mkdirs(tb testing.TB, base string, dirs ...string) {
	tb.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			tb.Fatal(err)
		}
	}
}

// newTestREPL returns a REPL with a temporary HOME, XDG directories and
// history, started in HOME.
func newScopesREPL(t *testing.T) *REPL {
	t.Helper()
	home := tempTree(t)
	t.Chdir(home) // cd changes the process directory; this puts it back
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	sh := shell.New()
	sh.Init()
	r := &REPL{sh: sh, hist: openHistory(sh)}
	sh.History = r.hist
	return r
}

func (r *REPL) cd(t *testing.T, dir string) {
	t.Helper()
	if st := r.sh.RunString("cd "+shell.Quote(dir), "", r.sh.StdIO()); st != 0 || r.sh.Dir() != dir {
		t.Fatalf("cd %s: status %d, in %s", dir, st, r.sh.Dir())
	}
}

func TestFindRepo(t *testing.T) {
	base := tempTree(t)
	mkdirs(t, base, "plain/deep", "repo/.git", "repo/a/b", "repo/sub/x", "repo/dangling")
	repo := filepath.Join(base, "repo")
	if err := os.WriteFile(filepath.Join(repo, "sub", ".git"), []byte("gitdir: ../.git/modules/sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "nowhere"), filepath.Join(repo, "dangling", ".git")); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ dir, want string }{
		{repo, repo},
		{filepath.Join(repo, "a"), repo},
		{filepath.Join(repo, "a", "b"), repo},
		{filepath.Join(repo, "a", "b") + "/", repo},
		// A directory that's gone is still in the repository.
		{filepath.Join(repo, "a", "gone"), repo},
		// Submodules and worktrees have a .git file.
		{filepath.Join(repo, "sub"), filepath.Join(repo, "sub")},
		{filepath.Join(repo, "sub", "x"), filepath.Join(repo, "sub")},
		// A .git that leads nowhere doesn't count.
		{filepath.Join(repo, "dangling"), repo},
		{filepath.Join(base, "plain", "deep"), ""},
		{base, ""},
		{"relative/path", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := findRepo(tt.dir); got != tt.want {
			t.Errorf("findRepo(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

func TestRepoCache(t *testing.T) {
	base := tempTree(t)
	dir := filepath.Join(base, "proj")
	mkdirs(t, base, "proj")
	var c repoCache
	if got := c.root(dir); got != "" {
		t.Fatalf("root(%q) = %q before git init", dir, got)
	}
	mkdirs(t, base, "proj/.git")
	if got := c.root(dir); got != "" {
		t.Errorf("root(%q) = %q: the answer wasn't remembered", dir, got)
	}
	c.forget()
	if got := c.root(dir); got != dir {
		t.Errorf("root(%q) = %q after forget, want %q", dir, got, dir)
	}
	// It stays small however many directories it sees.
	for i := 0; i < 200; i++ {
		if got := c.root(filepath.Join(dir, strconv.Itoa(i))); got != dir {
			t.Fatalf("root of subdirectory %d = %q", i, got)
		}
	}
	if n := len(c.roots); n > 64 {
		t.Errorf("the cache holds %d directories", n)
	}
}

func TestSearchScopes(t *testing.T) {
	r := newScopesREPL(t)
	base := r.sh.HomeDir()
	mkdirs(t, base, "plain", "repo/.git", "repo/sub")
	inRepo := []string{"all", "directory", "repository", "failed"}
	outside := []string{"all", "directory", "failed"}
	tests := []struct {
		dir, start string // the directory, and $ash_history_scope
		want       []string
	}{
		{"repo/sub", "", inRepo},
		{"repo", "", inRepo},
		{"plain", "", outside},
		{"repo", "directory", []string{"directory", "repository", "failed", "all"}},
		{"repo", "failed", []string{"failed", "all", "directory", "repository"}},
		{"plain", "failed", []string{"failed", "all", "directory"}},
		// No repository here, so it starts at the top.
		{"plain", "repository", outside},
		{"repo", "all", inRepo},
		{"repo", "everywhere", inRepo},
	}
	for _, tt := range tests {
		r.cd(t, filepath.Join(base, tt.dir))
		r.sh.SetString("ash_history_scope", tt.start)
		if got := r.searchScopes(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("in %s with ash_history_scope=%q: scopes %q, want %q", tt.dir, tt.start, got, tt.want)
		}
	}
}

// TestRepoForgottenAfterCommand checks that a repository made by a command
// is found at the next prompt.
func TestRepoForgottenAfterCommand(t *testing.T) {
	r := newScopesREPL(t)
	dir := filepath.Join(r.sh.HomeDir(), "proj")
	mkdirs(t, r.sh.HomeDir(), "proj")
	r.cd(t, dir)
	if slices.Contains(r.searchScopes(), editor.ScopeRepository) {
		t.Fatal("a repository scope before git init")
	}
	mkdirs(t, dir, ".git")
	if slices.Contains(r.searchScopes(), editor.ScopeRepository) {
		t.Error("the directory was looked at again without a command in between")
	}
	r.runLine("true")
	if !slices.Contains(r.searchScopes(), editor.ScopeRepository) {
		t.Error("no repository scope after a command")
	}
}

func TestSearchHistoryScopes(t *testing.T) {
	r := newScopesREPL(t)
	home := r.sh.HomeDir()
	repo := filepath.Join(home, "src", "ash")
	editorDir := filepath.Join(repo, "internal", "editor")
	notes := filepath.Join(home, "notes")
	mkdirs(t, home, "src/ash/.git", "src/ash/internal/editor", "notes")
	now := time.Now()
	for _, e := range []struct {
		cmd, dir string
		status   int
	}{
		{"git status", repo, 0},
		{"go test ./...", repo, 1},
		{"go vet ./internal/editor", editorDir, 0},
		{"vim todo.md", notes, 0},
		{"make", notes, 2},
	} {
		r.hist.Add(e.cmd, e.dir, now, e.status, 0)
	}
	rows := func(rs []editor.SearchResult) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Text+" | "+r.Info)
		}
		return out
	}

	r.cd(t, editorDir)
	tests := []struct {
		scope, q string
		want     []string
	}{
		{"all", "", []string{
			"make | ✗ now  ~/notes",
			"vim todo.md | now  ~/notes",
			"go vet ./internal/editor | now  ~/src/ash/internal/editor",
			"go test ./... | ✗ now  ~/src/ash",
			"git status | now  ~/src/ash",
		}},
		// Every command here ran here, so the directory goes without saying.
		{"directory", "", []string{"go vet ./internal/editor | now"}},
		// Directories show from the top of the repository.
		{"repository", "", []string{
			"go vet ./internal/editor | now  ash/internal/editor",
			"go test ./... | ✗ now  ash",
			"git status | now  ash",
		}},
		{"repository", "gstat", []string{"git status | now  ash"}},
		{"failed", "", []string{"make | ✗ now  ~/notes", "go test ./... | ✗ now  ~/src/ash"}},
		{"failed", "mk", []string{"make | ✗ now  ~/notes"}},
	}
	for _, tt := range tests {
		if got := rows(r.searchHistory(tt.q, tt.scope, 10)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("searching %q in %s: got %q, want %q", tt.q, tt.scope, got, tt.want)
		}
	}
	if got := r.searchHistory("gstat", "repository", 10); len(got) != 1 || !reflect.DeepEqual(got[0].Positions, []int{0, 4, 5, 6, 7}) {
		t.Errorf("gstat in the repository: %+v", got)
	}

	// The repository scope finds nothing outside a repository, should it
	// be asked.
	r.cd(t, notes)
	if got := r.searchHistory("", "repository", 10); len(got) != 0 {
		t.Errorf("repository scope outside a repository: %q", rows(got))
	}
	if got := rows(r.searchHistory("", "directory", 10)); !reflect.DeepEqual(got, []string{"make | ✗ now", "vim todo.md | now"}) {
		t.Errorf("directory scope in %s: %q", notes, got)
	}
}

// TestHistoryHere runs history --here and --failed on ash's own history.
func TestHistoryHere(t *testing.T) {
	r := newScopesREPL(t)
	home := r.sh.HomeDir()
	mkdirs(t, home, "a", "b")
	now := time.Now()
	r.hist.Add("ls", filepath.Join(home, "a"), now, 0, 0)
	r.hist.Add("make", filepath.Join(home, "b"), now, 2, 0)
	r.hist.Add("make test", filepath.Join(home, "a"), now, 1, 0)
	r.hist.Add("git status", filepath.Join(home, "a"), now, 0, 0)
	r.cd(t, filepath.Join(home, "a"))
	for _, tt := range []struct{ args, want string }{
		{"--here", "git status\nmake test\nls\n"},
		{"--failed", "make test\nmake\n"},
		{"--here --failed", "make test\n"},
		{"--here -n 1", "git status\n"},
		{"--failed mak", "make test\nmake\n"},
	} {
		var out, errs strings.Builder
		io := &shell.IO{In: &shell.Stream{R: strings.NewReader("")}, Out: &shell.Stream{W: &out}, Err: &shell.Stream{W: &errs}}
		if st := r.sh.RunString("history "+tt.args, "", io); st != 0 || out.String() != tt.want || errs.Len() > 0 {
			t.Errorf("history %s: %q (stderr %q, status %d), want %q", tt.args, out.String(), errs.String(), st, tt.want)
		}
	}
}

// BenchmarkRepoRoot measures finding the repository of a directory ten
// levels below its root, looking from a directory in no repository, and
// asking the cache again.
func BenchmarkRepoRoot(b *testing.B) {
	base, err := filepath.EvalSymlinks(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	mkdirs(b, base, "repo/.git", "repo/a/b/c/d/e/f/g/h/i/j", "plain")
	deep := filepath.Join(base, "repo/a/b/c/d/e/f/g/h/i/j")
	b.Run("walk", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			findRepo(deep)
		}
	})
	b.Run("no_repo", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			findRepo(filepath.Join(base, "plain"))
		}
	})
	b.Run("cached", func(b *testing.B) {
		var c repoCache
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			c.root(deep)
		}
	})
}

func TestRepoDir(t *testing.T) {
	tests := []struct{ dir, root, want string }{
		{"/src/ash", "/src/ash", "ash"},
		{"/src/ash/internal/editor", "/src/ash", "ash/internal/editor"},
		{"/src/ash/a/very/deep/directory/tree/below", "/src/ash", "…/below"},
		{"/etc", "/", "/etc"},
	}
	for _, tt := range tests {
		if got := repoDir(tt.dir, tt.root); got != tt.want {
			t.Errorf("repoDir(%q, %q) = %q, want %q", tt.dir, tt.root, got, tt.want)
		}
	}
}
