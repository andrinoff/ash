package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGitReadOnly(t *testing.T) {
	yes := []string{
		"git for-each-ref --format=%(refname:strip=2) refs/heads/",
		"git tag --sort=-creatordate",
		"git branch --no-color",
		"git branch --list feat*",
		"git rev-parse --show-toplevel --is-bare-repository",
		"git -c core.quotePath= log --max-count=50",
		"git config -z --get-regexp alias\\..*",
		"git config get user.name",
		"git --version",
		"git --list-cmds=main,others,alias,nohelpers",
		"git remote",
		"git remote -v",
		"git stash list",
		"git --no-pager -C sub for-each-ref",
	}
	no := []string{
		"git status --porcelain",
		"git diff --name-only",
		"git ls-files --others",
		"git tag v1.0",
		"git tag -d v1.0",
		"git branch new",
		"git branch -D old",
		"git config user.name me",
		"git config --unset --get user.name",
		"git remote add origin x",
		"git stash",
		"git fetch",
		"git --git-dir=x log",
		"git symbolic-ref HEAD refs/heads/x",
		"git",
	}
	for _, l := range yes {
		if _, ok := gitReadOnly(strings.Fields(l)); !ok {
			t.Errorf("%s: not cached", l)
		}
	}
	for _, l := range no {
		if _, ok := gitReadOnly(strings.Fields(l)); ok {
			t.Errorf("%s: cached", l)
		}
	}
	if d, _ := gitReadOnly(strings.Fields("git -C sub for-each-ref")); d != "sub" {
		t.Errorf("-C dir: %q", d)
	}
}

// gitCompShell is a completion shell whose $PATH has a git that counts
// how often it runs, in a new repository with one commit on main.
func gitCompShell(t *testing.T) (sh *Shell, work string, runs func() int, gitc func(...string)) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	sh, work = newCompShell(t)
	bin := sh.Get("PATH")
	count := filepath.Join(t.TempDir(), "count")
	script := "#!/bin/sh\necho x >> '" + count + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sh.SetVar("PATH", []string{bin, "/usr/bin", "/bin"}, setOpts{scope: setGlobal, export: 1})
	env := append(os.Environ(), "HOME="+sh.Get("HOME"), "XDG_CONFIG_HOME="+sh.Get("XDG_CONFIG_HOME"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	gitc = func(args ...string) {
		t.Helper()
		cmd := exec.Command(real, args...)
		cmd.Dir, cmd.Env = work, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	gitc("init", "-q", "-b", "main")
	gitc("commit", "-q", "--allow-empty", "-m", "one")
	sh.SetVar("GIT_CONFIG_NOSYSTEM", []string{"1"}, setOpts{scope: setGlobal, export: 1})
	runs = func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
	run(t, sh, `complete -c br -f -a '(command git for-each-ref --format="%(refname:strip=2)" refs/heads/)'`)
	return sh, work, runs, gitc
}

func TestGitCompletionCache(t *testing.T) {
	sh, _, runs, gitc := gitCompShell(t)
	branches := func() []string {
		t.Helper()
		got := texts(sh.Complete("br ", 3))
		slices.Sort(got)
		return got
	}
	if got := branches(); !slices.Equal(got, []string{"main"}) {
		t.Fatalf("branches %q", got)
	}
	if runs() != 1 {
		t.Fatalf("git ran %d times", runs())
	}
	branches()
	branches()
	if runs() != 1 {
		t.Errorf("git ran %d times for three completions, want 1", runs())
	}

	// a new branch shows up at once
	gitc("branch", "feature")
	if got := branches(); !slices.Equal(got, []string{"feature", "main"}) {
		t.Errorf("after git branch: %q", got)
	}
	// and so does one inside a new folder of refs, and a deleted one goes
	gitc("branch", "fix/deep/one")
	gitc("branch", "-D", "feature")
	if got := branches(); !slices.Equal(got, []string{"fix/deep/one", "main"}) {
		t.Errorf("after nested branch and delete: %q", got)
	}
	gitc("pack-refs", "--all")
	gitc("branch", "-m", "fix/deep/one", "renamed")
	if got := branches(); !slices.Equal(got, []string{"main", "renamed"}) {
		t.Errorf("after pack-refs and rename: %q", got)
	}

	// outside a completion git always runs
	before := runs()
	run(t, sh, "command git rev-parse --git-dir >/dev/null")
	run(t, sh, "command git rev-parse --git-dir >/dev/null")
	if runs() != before+2 {
		t.Errorf("git outside completion ran %d times, want 2", runs()-before)
	}
}

func TestGitCompletionWarm(t *testing.T) {
	sh, _, runs, gitc := gitCompShell(t)
	texts(sh.Complete("br ", 3))
	gitc("branch", "warm")
	wait := func() {
		t.Helper()
		sh.WarmGitCompletions()
		for range 500 {
			c := &sh.shared.complete.git
			c.mu.Lock()
			busy := c.warming
			c.mu.Unlock()
			if !busy {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("warming never finished")
	}
	wait()
	before := runs()
	got := texts(sh.Complete("br ", 3))
	slices.Sort(got)
	if !slices.Equal(got, []string{"main", "warm"}) {
		t.Errorf("branches %q", got)
	}
	if runs() != before {
		t.Errorf("the first Tab after warming ran git %d times", runs()-before)
	}

	// a new shell warms what the last one used
	b, err := os.ReadFile(filepath.Join(sh.CacheDir(), "git-complete"))
	if err != nil || !strings.Contains(string(b), `"for-each-ref"`) {
		t.Fatalf("saved commands: %q %v", b, err)
	}
	sh2 := New()
	sh2.dir = sh.dir
	for _, v := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "PATH", "GIT_CONFIG_NOSYSTEM"} {
		sh2.SetVar(v, []string{sh.Get(v)}, setOpts{scope: setGlobal, export: 1})
	}
	sh2.Init()
	run(t, sh2, `complete -c br -f -a '(command git for-each-ref --format="%(refname:strip=2)" refs/heads/)'`)
	sh = sh2
	wait()
	before = runs()
	texts(sh2.Complete("br ", 3))
	if runs() != before {
		t.Errorf("a new shell's first Tab ran git %d times", runs()-before)
	}
}
