package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
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

// ashBin is the ash binary built for these tests.
var ashBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ash-main-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ashBin = filepath.Join(dir, "ash")
	build := exec.Command("go", "build", "-o", ashBin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building ash:", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// ashHome makes a home directory for a test, with ~/.config/ash holding
// the given files.
func ashHome(t *testing.T, config map[string]string) string {
	t.Helper()
	home := t.TempDir()
	for name, body := range config {
		p := filepath.Join(home, ".config", "ash", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func ashEnv(home string, extra ...string) []string {
	return append([]string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"PATH=/usr/bin:/bin",
		"TERM=dumb",
	}, extra...)
}

// runAsh runs ash with HOME and the XDG directories in home and stdin
// from in. It returns stdout, stderr and the exit status.
func runAsh(t *testing.T, home, in string, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(ashBin, args...)
	cmd.Dir = home
	cmd.Env = ashEnv(home, env...)
	cmd.Stdin = strings.NewReader(in)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	status := 0
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), status
}

// hasLines checks that text has a line matching each pattern, in order.
func hasLines(t *testing.T, text string, patterns ...string) {
	t.Helper()
	lines := strings.Split(text, "\n")
	i := 0
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		for i < len(lines) && !re.MatchString(lines[i]) {
			i++
		}
		if i == len(lines) {
			t.Errorf("no line matching %q (in order) in:\n%s", p, text)
			return
		}
		i++
	}
}

const ms = `\d+\.\d\dms`

func TestProfileCommand(t *testing.T) {
	home := ashHome(t, nil)
	out, errs, st := runAsh(t, home, "", nil, "--profile", "-c", "sleep 0.02\nfor i in 1 2; true; end\necho hi")
	if out != "hi\n" || st != 0 {
		t.Fatalf("stdout %q status %d", out, st)
	}
	hasLines(t, errs,
		`^ash: profile, slowest first$`,
		`^  where +self +total  statement$`,
		`^  -c:1 +`+ms+` +`+ms+`  sleep 0\.02$`,
		`^  -c:[23] `,
		`^  -c:[23] `,
		`^ash: exit at `+ms+`$`,
	)
	// without --profile there's no report
	if _, errs, _ := runAsh(t, home, "", nil, "-c", "true"); errs != "" {
		t.Errorf("stderr without --profile: %q", errs)
	}
}

func TestProfileScript(t *testing.T) {
	home := ashHome(t, nil)
	script := filepath.Join(home, "build.ash")
	os.WriteFile(script, []byte("#!/usr/bin/env ash\nset -l x 1\nsleep 0.01\n"), 0o755)
	_, errs, st := runAsh(t, home, "", nil, "--profile", script)
	if st != 0 {
		t.Fatalf("status %d: %s", st, errs)
	}
	hasLines(t, errs,
		`^  ~/build\.ash:3 +`+ms+` +`+ms+`  sleep 0\.01$`,
		`^  ~/build\.ash:2 `,
		`^  ~/build\.ash +`+ms+` +`+ms+`  \(whole file\)$`,
		`^ash: exit at `,
	)
}

func TestProfileEnvironment(t *testing.T) {
	home := ashHome(t, nil)
	// ASH_PROFILE turns it on, and is not passed on
	out, errs, _ := runAsh(t, home, "", []string{"ASH_PROFILE=1"}, "-c", "env | grep -c ASH_PROFILE; ash_inner=1 true")
	if out != "0\n" {
		t.Errorf("ASH_PROFILE leaked to a child: %q", out)
	}
	if !strings.Contains(errs, "ash: exit at ") {
		t.Errorf("no report with ASH_PROFILE=1: %q", errs)
	}
	if _, errs, _ := runAsh(t, home, "", []string{"ASH_PROFILE=0"}, "-c", "true"); errs != "" {
		t.Errorf("ASH_PROFILE=0 printed %q", errs)
	}
	if _, errs, _ := runAsh(t, home, "", []string{"ASH_PROFILE="}, "-c", "true"); errs != "" {
		t.Errorf("ASH_PROFILE= printed %q", errs)
	}
}

func TestProfileConfig(t *testing.T) {
	home := ashHome(t, map[string]string{
		"conf.d/env.ash": "set -gx EDITOR vi\n",
		"config.ash": `set -g x 1
if status is-interactive
    sleep 0.02
    source ~/.config/ash/more.ash
end
`,
		"more.ash": "set -g y 2\n",
	})
	// interactive, without a terminal: the report comes before the prompt
	_, errs, st := runAsh(t, home, "exit\n", nil, "--profile", "-i")
	if st != 0 {
		t.Fatalf("status %d: %s", st, errs)
	}
	hasLines(t, errs,
		`^ash: profile, slowest first$`,
		`^  config\.ash:3 +`+ms+` +`+ms+`  sleep 0\.02$`,
		`^  config\.ash +`+ms+` +`+ms+`  \(whole file\)$`,
		`^ash: config took `+ms+`, first prompt at `+ms+`$`,
	)
	for _, want := range []string{"more.ash:1", "conf.d/env.ash:1", "config.ash:4", "more.ash  "} {
		if !strings.Contains(errs, want) {
			t.Errorf("no %q in\n%s", want, errs)
		}
	}
	if strings.Count(errs, "ash: profile") != 1 {
		t.Errorf("reported more than once:\n%s", errs)
	}

	// ash -i -c reads the config too
	out, errs, _ := runAsh(t, home, "", nil, "--profile", "-i", "-c", "echo $y")
	if out != "2\n" {
		t.Errorf("-i -c output %q", out)
	}
	hasLines(t, errs, `^  config\.ash:3 `, `^ash: config took `+ms+`, exit at `+ms+`$`)

	// -N: nothing to break down
	_, errs, _ = runAsh(t, home, "exit\n", nil, "--profile", "-N", "-i")
	if !regexp.MustCompile(`^ash: first prompt at ` + ms + `\n$`).MatchString(errs) {
		t.Errorf("-N report: %q", errs)
	}
}

func TestProfileConfigExits(t *testing.T) {
	home := ashHome(t, map[string]string{"config.ash": "set -g x 1\nexit 3\n"})
	_, errs, st := runAsh(t, home, "", nil, "--profile", "-i")
	if st != 3 {
		t.Errorf("status %d", st)
	}
	hasLines(t, errs, `^  config\.ash:1 `, `^ash: config took `+ms+`, exit at `+ms+`$`)

	home = ashHome(t, map[string]string{"config.ash": "set -g x 1\nexec /bin/echo replaced\n"})
	out, errs, _ := runAsh(t, home, "", nil, "--profile", "-i")
	if out != "replaced\n" {
		t.Errorf("exec output %q", out)
	}
	hasLines(t, errs, `^  config\.ash:1 `, `^ash: exec at `+ms+`$`)
}

// TestProfileTerminal runs ash on a pseudo-terminal (through script(1)),
// where the report goes above the first prompt once it is drawn.
func TestProfileTerminal(t *testing.T) {
	var script []string
	switch runtime.GOOS {
	case "darwin", "freebsd", "netbsd", "openbsd":
		script = []string{"script", "-q", "/dev/null", ashBin, "--profile"}
	case "linux":
		script = []string{"script", "-q", "-e", "-c", ashBin + " --profile", "/dev/null"}
	default:
		t.Skip("no script(1) here")
	}
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("no script(1)")
	}
	home := ashHome(t, map[string]string{"config.ash": "set -g x 1\nsleep 0.01\n"})
	for _, typeAhead := range []bool{false, true} {
		cmd := exec.Command(script[0], script[1:]...)
		cmd.Dir = home
		cmd.Env = ashEnv(home, "TERM=xterm-256color")
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var out bytes.Buffer
		cmd.Stdout = writerFunc(func(p []byte) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			return out.Write(p)
		})
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		output := func() string {
			mu.Lock()
			defer mu.Unlock()
			return out.String()
		}
		if typeAhead {
			// typed before the shell is ready: the line can end before the
			// report runs, which must still come out once
			in.Write([]byte("echo typed\r"))
		}
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(output(), "first prompt at") && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		in.Write([]byte("exit\r"))
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-done
		}
		got := output()
		if !strings.Contains(got, "❯") {
			// script(1) versions differ; without a prompt there was no
			// terminal session to look at
			t.Skipf("no prompt through script(1): %q", got)
		}
		if strings.Count(got, "ash: profile, slowest first") != 1 || strings.Count(got, "first prompt at") != 1 {
			t.Errorf("typeAhead=%v: want one report, got %q", typeAhead, got)
		}
		if !regexp.MustCompile(`config\.ash:2 +` + ms + ` +` + ms + `  sleep 0\.01`).MatchString(got) {
			t.Errorf("typeAhead=%v: no row for the sleep in %q", typeAhead, got)
		}
		if !typeAhead {
			// drawn above the prompt: the report comes after a prompt was
			// drawn, and the prompt is drawn again after it
			first := strings.Index(got, "❯")
			report := strings.Index(got, "ash: profile")
			if first < 0 || report < first || !strings.Contains(got[report:], "❯") {
				t.Errorf("the report should follow the first prompt and be followed by it: %q", got)
			}
		}
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
