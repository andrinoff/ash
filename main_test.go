package main

import (
	"os"
	"path/filepath"
	"testing"
)

// runArgs runs ash with the given arguments, with stdin away from any
// terminal the tests run on.
func runArgs(t *testing.T, args ...string) int {
	t.Helper()
	return runArgv(t, append([]string{"ash"}, args...)...)
}

// runArgv is runArgs with argv[0].
func runArgv(t *testing.T, argv ...string) int {
	t.Helper()
	return runStdin(t, os.DevNull, argv...)
}

// runStdin is runArgv with stdin read from a file.
func runStdin(t *testing.T, stdin string, argv ...string) int {
	t.Helper()
	in, err := os.Open(stdin)
	if err != nil {
		t.Fatal(err)
	}
	savedArgs, savedStdin := os.Args, os.Stdin
	os.Args, os.Stdin = argv, in
	defer func() {
		os.Args, os.Stdin = savedArgs, savedStdin
		in.Close()
	}()
	return run()
}

func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	return home
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ash -ic CMD is an interactive shell: it reads the config (and the
// universal variables) before running CMD, as tools that ask the login
// shell for its environment (`$SHELL -ilc env`) expect. Plain -c doesn't.
func TestInteractiveCommandReadsConfig(t *testing.T) {
	home := isolateHome(t)
	dir := filepath.Join(home, ".config", "ash")
	writeFile(t, filepath.Join(dir, "conf.d", "a.ash"), "set -g order a\n")
	writeFile(t, filepath.Join(dir, "config.ash"), "set -g order $order config\n")
	writeFile(t, filepath.Join(dir, "variables"), "- uvar\x1efrom-universal\n")
	out := filepath.Join(home, "out")

	if st := runArgs(t, "-ic", "echo $order $uvar > "+out); st != 0 {
		t.Fatalf("status %d", st)
	}
	if got, want := readFile(t, out), "a config from-universal\n"; got != want {
		t.Errorf("ash -ic: got %q, want %q", got, want)
	}

	if st := runArgs(t, "-c", "echo $order. > "+out); st != 0 {
		t.Fatalf("status %d", st)
	}
	if got, want := readFile(t, out), ".\n"; got != want {
		t.Errorf("ash -c read the config: got %q, want %q", got, want)
	}
}

// A login shell is recognized by the "-" that starts argv[0], which can
// also be a path: `exec -l /usr/local/bin/ash` gives "-/usr/local/bin/ash".
func TestLoginFromArgv0(t *testing.T) {
	home := isolateHome(t)
	out := filepath.Join(home, "out")
	for argv0, want := range map[string]string{
		"-ash":                "login\n",
		"-/usr/local/bin/ash": "login\n",
		"/usr/local/bin/ash":  "not\n",
		"ash":                 "not\n",
	} {
		runArgv(t, argv0, "-c", "if status is-login; echo login; else; echo not; end > "+out)
		if got := readFile(t, out); got != want {
			t.Errorf("argv[0] %q: got %q, want %q", argv0, got, want)
		}
	}
}

// -s reads the commands from stdin and makes all the arguments
// positional parameters, as in `curl -fsSL URL | sh -s -- --flag`.
func TestStdinWithArgs(t *testing.T) {
	home := isolateHome(t)
	out := filepath.Join(home, "out")
	script := filepath.Join(home, "script")
	writeFile(t, script, "echo $# $1 $2 > "+out+"\n")
	for _, args := range [][]string{
		{"-s", "p", "--flag"},
		{"-s", "--", "p", "--flag"},
		{"-s", "-", "p", "--flag"},
		{"-", "p", "--flag"},
	} {
		if st := runStdin(t, script, append([]string{"ash"}, args...)...); st != 0 {
			t.Errorf("%q: status %d", args, st)
			continue
		}
		if got, want := readFile(t, out), "2 p --flag\n"; got != want {
			t.Errorf("%q: got %q, want %q", args, got, want)
		}
	}
}
