package shell

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeCobra is a stand-in for a cobra program. It answers __complete the
// way cobra does and records the arguments it was given.
const fakeCobra = `#!/bin/sh
[ "$1" = __complete ] || exit 3
shift
printf '%s\n' "$@" > "$0.args"
case "$1" in
pr)    printf 'checkout\tCheck out a pull request\ncreate\tCreate a pull request\n:4\n' ;;
files) printf ':0\n' ;;
nomatch) printf 'zzz\n:0\n' ;;
broken) printf 'oops\n:1\n' ;;
dirs)  printf ':16\n' ;;
help)  printf '_activeHelp_ some help\nhelpful\n:4\n' ;;
fail)  exit 1 ;;
plain) echo 'not cobra' ;;
*)     printf 'pr\tManage pull requests\nrepo\tManage repositories\n:4\n' ;;
esac
`

func setupFakeCobra(t *testing.T) (*Shell, string, string) {
	t.Helper()
	sh, work := newCompShell(t)
	bin := sh.Get("PATH")
	prog := filepath.Join(bin, "fakecobra")
	if err := os.WriteFile(prog, []byte(fakeCobra), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := isCobra
	isCobra = func(path string) bool { return path == prog }
	t.Cleanup(func() { isCobra = orig })
	return sh, work, prog
}

func TestCompleteCobra(t *testing.T) {
	sh, work, prog := setupFakeCobra(t)
	touch(t, work, "file1", "file2")

	c := sh.Complete("fakecobra ", 10)
	if got := texts(c); !reflect.DeepEqual(got, []string{"pr", "repo"}) {
		t.Fatalf("subcommands: got %q", got)
	}
	if it := find(c, "pr"); it == nil || it.Description != "Manage pull requests" {
		t.Errorf("description: %+v", it)
	}

	checkCompletions(t, sh, []compCase{
		{"fakecobra pr c", []string{"checkout", "create"}},
		{"fakecobra pr che", []string{"checkout"}},
		// nothing to offer and files are fine: files
		{"fakecobra files f", []string{"file1", "file2"}},
		{"fakecobra nomatch f", []string{"file1", "file2"}},
		// an error, or a directive ash doesn't do yet: files
		{"fakecobra broken f", []string{"file1", "file2"}},
		{"fakecobra dirs f", []string{"file1", "file2"}},
		// a failing program or one that doesn't speak cobra: files
		{"fakecobra fail f", []string{"file1", "file2"}},
		{"fakecobra plain f", []string{"file1", "file2"}},
		{"fakecobra help ", []string{"helpful"}},
	})

	// The program gets the finished words and the word being completed,
	// unquoted.
	sh.Complete(`fakecobra pr 'my branch' "che`, 32)
	b, err := os.ReadFile(prog + ".args")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), "pr\nmy branch\nche\n"; got != want {
		t.Errorf("arguments: got %q, want %q", got, want)
	}

	// An alias for the program completes like the program.
	run(t, sh, "alias fc fakecobra")
	checkCompletions(t, sh, []compCase{{"fc pr cr", []string{"create"}}})
}

func TestCompleteCobraInsteadOfItsScript(t *testing.T) {
	sh, _, _ := setupFakeCobra(t)
	dir := filepath.Join(sh.ConfigDir(), "completions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// What cobra generates, cut down; ash should ask the program itself.
	script := `function __fakecobra_perform_completion
    set -l requestComp "$args[1] __complete $args[2..-1] $lastArg"
end
complete -c fakecobra -f -a 'from-the-script'
`
	if err := os.WriteFile(filepath.Join(dir, "fakecobra.fish"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	checkCompletions(t, sh, []compCase{{"fakecobra pr c", []string{"checkout", "create"}}})
}

func TestCompleteCobraHandWrittenWins(t *testing.T) {
	sh, _, _ := setupFakeCobra(t)
	dir := filepath.Join(sh.ConfigDir(), "completions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fakecobra.fish"), []byte("complete -c fakecobra -f -a by-hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkCompletions(t, sh, []compCase{{"fakecobra ", []string{"by-hand"}}})
}

func TestCompleteCobraOnlyForCobra(t *testing.T) {
	sh, work := newCompShell(t)
	touch(t, work, "file1")
	prog := filepath.Join(sh.Get("PATH"), "notcobra")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\necho ran >> \"$0.ran\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkCompletions(t, sh, []compCase{{"notcobra f", []string{"file1"}}})
	if _, err := os.Stat(prog + ".ran"); err == nil {
		t.Error("a program that isn't cobra was run to complete it")
	}
}

func TestCobraDirective(t *testing.T) {
	tests := []struct {
		items []string
		d     int
		cur   string
		want  []string
		files bool
	}{
		{[]string{"a", "b"}, cobraNoFiles, "", []string{"a", "b"}, false},
		// with files allowed, candidates must match what was typed
		{[]string{"apple", "berry"}, 0, "a", []string{"apple"}, false},
		{[]string{"berry"}, 0, "a", nil, true},
		{nil, cobraNoFiles, "a", nil, false},
		// no space after the only candidate
		{[]string{"key\tdesc"}, cobraNoSpace | cobraNoFiles, "k", []string{"key", "key."}, false},
		{[]string{"key="}, cobraNoSpace | cobraNoFiles, "k", []string{"key="}, false},
		{[]string{"x"}, cobraError, "", nil, true},
		{[]string{"yaml"}, cobraFileExt, "", nil, true},
	}
	for _, tt := range tests {
		got, files := cobraDirective(tt.items, tt.d, tt.cur)
		if !reflect.DeepEqual(got, tt.want) || files != tt.files {
			t.Errorf("cobraDirective(%q, %d, %q) = %q, %v; want %q, %v", tt.items, tt.d, tt.cur, got, files, tt.want, tt.files)
		}
	}
}

func TestGoModInfo(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	info := goModInfo(exe)
	// this test binary uses Lip Gloss
	if !bytes.Contains(info, []byte("\tcharm.land/lipgloss/v2\t")) {
		t.Fatalf("no module information found in %s:\n%.300s", exe, info)
	}
	if strings.Contains(string(info), "spf13/cobra") {
		t.Error("ash isn't built with cobra")
	}

	dir := t.TempDir()
	for name, content := range map[string]string{
		"script": "#!/bin/sh\necho hi\n",
		"empty":  "",
		"java":   "\xca\xfe\xba\xbe\x00\x00\x00\x34" + strings.Repeat("\x00", 100),
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		if info := goModInfo(p); info != nil {
			t.Errorf("%s: got module information %q", name, info)
		}
	}
	if goModInfo(filepath.Join(dir, "missing")) != nil {
		t.Error("a missing file has module information")
	}
}
