package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fishShell is newCompShell with fish in $PATH. Fish's files are cached
// in the shell's temporary XDG_CACHE_HOME.
func fishShell(t *testing.T) *Shell {
	t.Helper()
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is not installed")
	}
	sh, _ := newCompShell(t)
	sh.SetVar("PATH", []string{sh.Get("PATH"), filepath.Dir(fish), "/usr/bin", "/bin"}, setOpts{scope: setGlobal, export: 1})
	return sh
}

func TestFishLibrary(t *testing.T) {
	sh := fishShell(t)
	c := sh.shared.complete
	p, src := c.fish.completion(sh, "ls")
	if src == nil {
		t.Skip("this fish has no built-in completions (fish 3?)")
	}
	if !strings.Contains(string(src), "complete") {
		t.Errorf("completions/ls.fish: %.80q", src)
	}
	if !strings.HasPrefix(p, filepath.Join(sh.CacheDir(), "fish-")) {
		t.Errorf("cached at %s, want under %s", p, sh.CacheDir())
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != string(src) {
		t.Errorf("cache file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sh.CacheDir(), "fish-version")); err != nil {
		t.Errorf("the fish version is not remembered: %v", err)
	}
	if p, src := c.fish.completion(sh, "ash-no-such-command"); src != nil {
		t.Errorf("got a completion file %s for a command fish doesn't know", p)
	}

	c.fish.ensureFunctions(sh)
	fn := sh.Function("__fish_seen_subcommand_from")
	if fn == nil {
		t.Fatal("__fish_seen_subcommand_from is not autoloadable")
	}
	if !strings.HasPrefix(fn.File, filepath.Join(sh.CacheDir(), "fish-")) {
		t.Errorf("loaded from %s", fn.File)
	}
	// fish's other functions would shadow ash's own commands
	for _, name := range []string{"ls", "cd", "history", "fish_prompt"} {
		if fn := sh.Function(name); fn != nil {
			t.Errorf("fish's %s function is used (from %s)", name, fn.File)
		}
	}

	// a completion file from fish, used through the engine (macOS's ls
	// only has short options)
	cmp := sh.Complete("ls -", 4)
	if it := find(cmp, "-l"); it == nil || it.Description == "" || len(cmp.Items) < 5 {
		t.Errorf("ls -: %q", texts(cmp))
	}
}

func TestPrepareCompletions(t *testing.T) {
	sh := fishShell(t)
	sh.PrepareCompletions()
	// completion waits for the work started in the background
	sh.shared.complete.fish.ensureFunctions(sh)
	if sh.Function("__fish_use_subcommand") == nil {
		t.Fatal("fish's helpers are not available")
	}
	// without fish, nothing happens
	sh2, _ := newCompShell(t)
	sh2.PrepareCompletions()
	if sh2.Function("__fish_use_subcommand") != nil {
		t.Fatal("helpers without fish")
	}
}

func TestCompleteGitFromFish(t *testing.T) {
	sh := fishShell(t)
	if out, _, _ := runScript(t, "string length abc"); out != "3\n" {
		t.Skip("the string builtin is not implemented in this build")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	sh.SetVar("PATH", append([]string{filepath.Dir(git)}, sh.GetList("PATH")...), setOpts{scope: setGlobal, export: 1})
	c := sh.Complete("git che", 7)
	it := find(c, "checkout")
	if it == nil {
		t.Fatalf("git che: %q", texts(c))
	}
	if it.Description == "" {
		t.Errorf("checkout has no description")
	}
	if c := sh.Complete("git checkout --", 15); len(c.Items) == 0 {
		t.Errorf("git checkout --: no options")
	}
}
