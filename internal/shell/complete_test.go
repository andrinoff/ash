package shell

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/andrinoff/ash/internal/editor"
)

// newCompShell returns a shell for completion tests. Its home, XDG
// directories and $PATH are temporary (so fish is not found and nothing
// of the user's is read), and it works in an empty directory, which is
// returned.
func newCompShell(t *testing.T) (*Shell, string) {
	t.Helper()
	tmp := t.TempDir()
	sh := New()
	for _, kv := range [][2]string{
		{"HOME", "home"},
		{"XDG_CONFIG_HOME", "config"},
		{"XDG_DATA_HOME", "data"},
		{"XDG_CACHE_HOME", "cache"},
		{"XDG_DATA_DIRS", "datadirs"},
		{"PATH", "bin"},
	} {
		dir := filepath.Join(tmp, kv[1])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		sh.SetVar(kv[0], []string{dir}, setOpts{scope: setGlobal, export: 1})
	}
	sh.UnsetVar("CDPATH", setGlobal)
	work := filepath.Join(tmp, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	sh.dir = work
	sh.Init()
	return sh, work
}

// run runs fish-syntax code in sh and fails the test on errors.
func run(t *testing.T, sh *Shell, src string) string {
	t.Helper()
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	io := &IO{In: &Stream{R: strings.NewReader("")}, Out: &Stream{W: out}, Err: &Stream{W: errb}}
	if st := sh.RunString(src, "test.fish", io); st != 0 || errb.String() != "" {
		t.Fatalf("%s: status %d, stderr %q", src, st, errb.String())
	}
	return out.String()
}

// touch creates files (names ending in / are directories).
func touch(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(dir, n)
		var err error
		if strings.HasSuffix(n, "/") {
			err = os.MkdirAll(p, 0o755)
		} else {
			if err = os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
				err = os.WriteFile(p, nil, 0o644)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func texts(c editor.Completion) []string {
	var out []string
	for _, it := range c.Items {
		out = append(out, it.Text)
	}
	return out
}

func find(c editor.Completion, text string) *editor.Candidate {
	for i := range c.Items {
		if c.Items[i].Text == text {
			return &c.Items[i]
		}
	}
	return nil
}

type compCase struct {
	line string
	want []string
}

func checkCompletions(t *testing.T, sh *Shell, tests []compCase) {
	t.Helper()
	for _, tt := range tests {
		c := sh.Complete(tt.line, len(tt.line))
		if got := texts(c); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("complete %q:\n got %q\nwant %q", tt.line, got, tt.want)
		}
		if c.End != len(tt.line) {
			t.Errorf("complete %q: End = %d, want %d", tt.line, c.End, len(tt.line))
		}
	}
}

func TestCompleteFiles(t *testing.T) {
	sh, dir := newCompShell(t)
	touch(t, dir, "my file.txt", "my folder/", ".hidden", "Makefile", "main.go", "a$b.txt",
		"it's.txt", "dir/inner.txt", "dir/sub/", "Doc/", "café.txt", "-dash")
	touch(t, sh.HomeDir(), "notes.txt")
	checkCompletions(t, sh, []compCase{
		{"cat ", []string{`a\$b.txt`, "café.txt", "dir/", "Doc/", `it\'s.txt`, "main.go", "Makefile", `my\ file.txt`, `my\ folder/`, "-dash"}},
		{"cat my", []string{`my\ file.txt`, `my\ folder/`}},
		{`cat my\ f`, []string{`my\ file.txt`, `my\ folder/`}},
		{`cat my\`, []string{`my\ file.txt`, `my\ folder/`}},
		{"cat 'my f", []string{"'my file.txt'", "'my folder/"}},
		{`cat "my f`, []string{`"my file.txt"`, `"my folder/`}},
		{`cat "my f"`, []string{`"my file.txt"`, `"my folder/"`}},
		{"cat 'it", []string{`'it'\''s.txt'`}},
		{`cat "a`, []string{`"a\$b.txt"`}},
		{"cat .", []string{".hidden"}},
		{"cat ..", []string{"../"}},
		{"cat dir/", []string{"dir/inner.txt", "dir/sub/"}},
		{"cat dir/i", []string{"dir/inner.txt"}},
		{"cat 'dir/i", []string{"'dir/inner.txt'"}},
		{"cat doc", []string{"Doc/"}},                     // case-insensitive prefix
		{"cat MAKE", []string{"Makefile"}},                // case-insensitive prefix
		{"cat ile", []string{"Makefile", `my\ file.txt`}}, // substring
		{"cat mfi", []string{`my\ file.txt`}},             // subsequence
		{"cat caf", []string{"café.txt"}},
		{"cat it", []string{`it\'s.txt`}},
		{"cat -", []string{"-dash"}},
		{"cat ~/", []string{"~/notes.txt"}},
		{"cat ~/no", []string{"~/notes.txt"}},
		{"cat $HOME/no", []string{"$HOME/notes.txt"}},
		{`cat "$HOME/no`, []string{`"$HOME/notes.txt"`}},
		{"cat --file=my", []string{`--file=my\ file.txt`, `--file=my\ folder/`}},
		{"cat x=d", []string{"x=dir/"}},
		{"cat d/s", []string{"dir/sub/"}}, // directories matched loosely
		{"cat *.g", []string{"main.go"}},
		{"echo hi > ma", []string{"main.go"}},
		{"echo hi >ma", []string{"main.go"}},
		{"cat nothing-like-this", nil},
	})
	c := sh.Complete("cat dir/", 8)
	if it := find(c, "dir/sub/"); it == nil || !it.NoSpace || it.Kind != "dir" || it.Label() != "sub/" {
		t.Errorf("dir candidate: %+v", it)
	}
	if it := find(c, "dir/inner.txt"); it == nil || it.NoSpace || it.Kind != "file" || it.Label() != "inner.txt" {
		t.Errorf("file candidate: %+v", it)
	}
	if c.Start != 4 {
		t.Errorf("Start = %d, want 4", c.Start)
	}
	// a closing quote after the cursor is replaced too
	line := `cat "my f" | wc`
	c = sh.Complete(line, strings.Index(line, `f"`)+1)
	if got := texts(c); !reflect.DeepEqual(got, []string{`"my file.txt"`, `"my folder/"`}) || c.End != strings.Index(line, `f"`)+2 {
		t.Errorf("before closing quote: got %q, End %d", got, c.End)
	}
}

func TestCompleteCD(t *testing.T) {
	sh, dir := newCompShell(t)
	touch(t, dir, "src/", "share/", "setup.py")
	cdp := filepath.Join(sh.HomeDir(), "projects")
	touch(t, cdp, "ash/", "sample/")
	sh.SetVar("CDPATH", []string{cdp}, setOpts{scope: setGlobal})
	checkCompletions(t, sh, []compCase{
		{"cd s", []string{"sample/", "share/", "src/"}},
		{"cd a", []string{"ash/"}},
		{"cd ./s", []string{"./share/", "./src/"}},
	})
}

func TestCompleteCommands(t *testing.T) {
	sh, dir := newCompShell(t)
	bin := sh.Get("PATH")
	for _, n := range []string{"ashtest-tool", "ashtest-other"} {
		if err := os.WriteFile(filepath.Join(bin, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	touch(t, bin, "ashtest-data")
	touch(t, dir, "sub/", "data.txt", ".config/")
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, sh, `
function ashtest-func -d 'my function'
end
function _ashtest-hidden
end
alias ashtest-al='ls -l'
abbr -a ashtest-ab 'git status'
`)
	checkCompletions(t, sh, []compCase{
		{"ashtest-", []string{"ashtest-ab", "ashtest-al", "ashtest-func", "ashtest-other", "ashtest-tool"}},
		{"echo hi; ashtest-t", []string{"ashtest-tool"}},
		{"sudo ashtest-t", nil}, // no completions for sudo: files, and none match
		{"command ashtest-t", []string{"ashtest-tool"}},
		{"if ashtest-t", []string{"ashtest-tool"}},
		{"echo (ashtest-t", []string{"ashtest-tool"}},
		{"_ashtest", []string{"_ashtest-hidden"}},
		{"./", []string{"./run.sh", "./sub/"}},
		{"./r", []string{"./run.sh"}},
		{".con", []string{".config/"}}, // implicit cd
		{"FOO=bar ashtest-o", []string{"ashtest-other"}},
		{"whil", []string{"while"}},
	})
	c := sh.Complete("ashtest-", 8)
	want := map[string][2]string{
		"ashtest-ab":    {"abbr", "Abbreviation: git status"},
		"ashtest-al":    {"alias", "ls -l"},
		"ashtest-func":  {"function", "my function"},
		"ashtest-other": {"command", ""},
	}
	for text, w := range want {
		it := find(c, text)
		if it == nil || it.Kind != w[0] || it.Description != w[1] {
			t.Errorf("%s: got %+v, want kind %q description %q", text, it, w[0], w[1])
		}
	}
	c = sh.Complete("complet", 7)
	if it := find(c, "complete"); it == nil || it.Kind != "builtin" || it.Description != BuiltinDescription("complete") {
		t.Errorf("builtin: %+v", c.Items)
	}
}

func TestCompleteVariables(t *testing.T) {
	sh, _ := newCompShell(t)
	run(t, sh, "set -g ashtestvar one two; set -g ashtestvar2 x")
	checkCompletions(t, sh, []compCase{
		{"echo $ashtestv", []string{"$ashtestvar", "$ashtestvar2"}},
		{"echo $ashtestvar2", []string{"$ashtestvar2"}},
		{"echo ${ashtestvar", []string{"${ashtestvar}", "${ashtestvar2}"}},
		{`echo "$ashtestvar2`, []string{`"$ashtestvar2"`}},
		{"echo foo$ashtestvar2", []string{"foo$ashtestvar2"}},
		{"echo $ASHTESTVAR2", []string{"$ashtestvar2"}},
		{"echo '$ashtestv", nil},
	})
	c := sh.Complete("echo $ashtestvar", 16)
	if it := find(c, "$ashtestvar"); it == nil || it.Description != "one two" || it.Kind != "variable" || it.Label() != "$ashtestvar" {
		t.Errorf("variable: %+v", it)
	}
	// complete -C prints the whole token, $ included
	for _, tt := range []struct{ src, want string }{
		{`complete -C 'echo $ashtestvar2'`, "$ashtestvar2\tx\n"},
		{`complete -C 'echo foo$ashtestvar2'`, "foo$ashtestvar2\tx\n"},
		{`complete -C 'echo "$ashtestvar2'`, "$ashtestvar2\tx\n"},
		{`complete -C 'echo ${ashtestvar2'`, "${ashtestvar2}\tx\n"},
		{`complete --escape -C 'echo $ashtestvar2'`, "$ashtestvar2\tx\n"},
	} {
		if got := run(t, sh, tt.src); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.src, got, tt.want)
		}
	}
}

func TestCompleteSpecs(t *testing.T) {
	sh, dir := newCompShell(t)
	touch(t, dir, "out.txt", "start.log")
	run(t, sh, `
complete -c ashtest -s h -l help -d 'Show help'
complete -c ashtest -s v -l verbose -d 'Be verbose'
complete -c ashtest -l color -d 'When to color' -x -a 'always never auto'
complete -c ashtest -s o -r -d 'Output file'
complete -c ashtest -l level -a '1 2 3' -d Level
complete -c ashtest -f -a 'start stop' -d 'Action'
complete -c ashtest -o old -d 'Old style'
`)
	checkCompletions(t, sh, []compCase{
		{"ashtest ", []string{"start", "stop"}},
		{"ashtest st", []string{"start", "stop"}},
		{"ashtest -", []string{"-h", "-o", "-old", "-v", "--color", "--help", "--level", "--level=", "--verbose"}},
		{"ashtest --c", []string{"--color"}},
		{"ashtest --he", []string{"--help"}},
		{"ashtest --HE", []string{"--help"}},
		{"ashtest --olor", []string{"--color"}},
		{"ashtest --color ", []string{"always", "auto", "never"}},
		{"ashtest --color a", []string{"always", "auto"}},
		{"ashtest --color=a", []string{"--color=always", "--color=auto"}},
		{"ashtest --level=", []string{"--level=1", "--level=2", "--level=3"}},
		{"ashtest --level ", []string{"start", "stop"}}, // optional argument: only with =
		{"ashtest -v", []string{"-vh", "-vo"}},
		{"ashtest -vh", []string{"-vho"}},
		{"ashtest -o ", []string{"out.txt", "start.log"}},
		{"ashtest -o st", []string{"start.log"}},
		{"ashtest -vo ", []string{"out.txt", "start.log"}},
		{"ashtest -ol", nil}, // as in fish: -o takes "l" as its argument
		{"ashtest -- -", nil},
		{"ashtest -- s", []string{"start", "stop"}},
	})
	run(t, sh, `
complete -c ashtestold -o name -d Name
complete -c ashtestold -o type -x -a 'f d' -d Type
`)
	checkCompletions(t, sh, []compCase{
		{"ashtestold -na", []string{"-name"}},
		{"ashtestold -", []string{"-name", "-type"}},
		{"ashtestold -type ", []string{"d", "f"}},
	})
	c := sh.Complete("ashtest -", 9)
	if it := find(c, "-o"); it == nil || !it.NoSpace || it.Description != "Output file" || it.Kind != "option" {
		t.Errorf("-o: %+v", it)
	}
	if it := find(c, "--level="); it == nil || !it.NoSpace {
		t.Errorf("--level=: %+v", it)
	}
	if it := find(c, "--help"); it == nil || it.NoSpace || it.Description != "Show help" {
		t.Errorf("--help: %+v", it)
	}
	c = sh.Complete("ashtest s", 9)
	if it := find(c, "start"); it == nil || it.Description != "Action" || it.Kind != "arg" {
		t.Errorf("start: %+v", it)
	}
}

func TestCompleteArgsQuoting(t *testing.T) {
	sh, _ := newCompShell(t)
	run(t, sh, `complete -c ashtest -f -a '"with space" "it\'s" plain \\$dollar dir/ key='`)
	checkCompletions(t, sh, []compCase{
		{"ashtest w", []string{`with\ space`}},
		{"ashtest 'w", []string{"'with space'"}},
		{`ashtest "w`, []string{`"with space"`}},
		{"ashtest i", []string{`it\'s`}},
		{"ashtest 'i", []string{`'it'\''s'`}},
		{`ashtest \$`, []string{`\$dollar`}},
		{"ashtest d", []string{"dir/"}},
		{"ashtest k", []string{"key="}},
	})
	c := sh.Complete("ashtest ", 8)
	for text, noSpace := range map[string]bool{"dir/": true, "key=": true, "plain": false} {
		if it := find(c, text); it == nil || it.NoSpace != noSpace {
			t.Errorf("%s: %+v, want NoSpace %v", text, it, noSpace)
		}
	}
}

func TestCompleteCommandSubstitution(t *testing.T) {
	sh, _ := newCompShell(t)
	run(t, sh, `
function __ashtest_items
    echo alpha
    echo beta\tthe second
end
complete -c ashtest -f -a '(echo one; echo two\tsecond)'
complete -c ashtest2 -f -a '(__ashtest_items)' -d 'Item'
complete -c ashtest3 -f -k -a 'zeta alpha mu'
complete -c ashtest3 -f -k -a 'beta'
`)
	checkCompletions(t, sh, []compCase{
		{"ashtest ", []string{"one", "two"}},
		{"ashtest2 ", []string{"alpha", "beta"}},
		{"ashtest3 ", []string{"beta", "zeta", "alpha", "mu"}},
	})
	c := sh.Complete("ashtest ", 8)
	if it := find(c, "two"); it == nil || it.Description != "second" {
		t.Errorf("two: %+v", it)
	}
	c = sh.Complete("ashtest2 ", 9)
	if it := find(c, "alpha"); it == nil || it.Description != "Item" {
		t.Errorf("alpha: %+v", it)
	}
	if it := find(c, "beta"); it == nil || it.Description != "the second" {
		t.Errorf("beta: %+v", it)
	}
}

func TestCompleteConditions(t *testing.T) {
	sh, _ := newCompShell(t)
	run(t, sh, `
complete -c ashtest -f -n 'test (count (commandline -opc)) -eq 1' -a 'sub1 sub2'
complete -c ashtest -f -n 'contains -- sub1 (commandline -opc)' -a 'x1 x2'
complete -c ashtest -f -n 'test (commandline -ct) = z' -a 'zz'
complete -c ashtest -f -n 'false' -a 'never'
complete -c ashtest -f -n 'echo noise; echo error >&2; exit 0' -a 'loud'
`)
	checkCompletions(t, sh, []compCase{
		{"ashtest ", []string{"loud", "sub1", "sub2"}},
		{"ashtest s", []string{"sub1", "sub2"}},
		{"ashtest sub1 ", []string{"loud", "x1", "x2"}},
		{"ashtest sub1 x", []string{"x1", "x2"}},
		{"ashtest sub2 ", []string{"loud"}},
		{"ashtest z", []string{"zz"}},
		{"echo (ashtest sub1 x", []string{"x1", "x2"}},
	})
	// conditions don't leak state into the shell
	if sh.Status() != 0 {
		t.Errorf("status = %d", sh.Status())
	}
	if exit, _ := sh.ShouldExit(); exit {
		t.Error("exit in a condition made the shell exit")
	}
}

func TestCompleteCommandline(t *testing.T) {
	sh, _ := newCompShell(t)
	run(t, sh, `complete -c ashtest -f -a '(set -l t (commandline -opc); echo opc=$t[-1]; echo n=(count $t); echo ct=(commandline -ct); echo C=(commandline -C))'`)
	c := sh.Complete("echo x; ashtest a ", 18)
	want := []string{"C=10", "ct=", "n=2", "opc=a"}
	if g := texts(c); !reflect.DeepEqual(g, want) {
		t.Errorf("got %q, want %q", g, want)
	}
	// outside of completion, commandline reads the editor as before
	out, _, _ := runScript(t, "commandline -opc; echo $status")
	if out != "1\n" {
		t.Errorf("commandline outside completion: %q", out)
	}
}

func TestCompleteWraps(t *testing.T) {
	sh, _ := newCompShell(t)
	run(t, sh, `
complete -c ashtest -l help -d Help
complete -c ashtest -f -n '__fish_ashtest_cond' -a 'only-if'
complete -c ashtest -f -n 'contains -- -v (commandline -opc)' -a 'verbose-arg'
complete -c ashtestw -w ashtest
function ashtestf --wraps ashtest
end
alias ashtestal='ashtest -v'
`)
	// a wrapper that is only autoloadable (saved with funcsave): like
	// fish, completion loads it to learn what it wraps
	fdir := filepath.Join(sh.ConfigDir(), "functions")
	if err := os.MkdirAll(fdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fdir, "ashtestlazyf.fish"), []byte("function ashtestlazyf --wraps 'ashtest -v'\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkCompletions(t, sh, []compCase{
		{"ashtestw --he", []string{"--help"}},
		{"ashtestf --he", []string{"--help"}},
		{"ashtestal --he", []string{"--help"}},
		{"ashtestal ", []string{"verbose-arg"}},
		{"ashtestw ", nil},
		{"ashtestlazyf --he", []string{"--help"}},
		{"ashtestlazyf ", []string{"verbose-arg"}},
	})
}

func TestCompleteBuiltinOutput(t *testing.T) {
	sh, dir := newCompShell(t)
	touch(t, dir, "my file.txt", "my folder/")
	tests := []struct{ src, want string }{
		{`complete -c ashtest -l color -d 'When to color' -x -a 'always never'; complete -C 'ashtest --col'`, "--color\tWhen to color\n"},
		{`complete -C 'ashtest --color '`, "always\tWhen to color\nnever\tWhen to color\n"},
		{`complete -C'ashtest --color n'`, "never\tWhen to color\n"},
		{`complete --do-complete='ashtest --color n'`, "never\tWhen to color\n"},
		{`complete -C 'cat my'`, "my file.txt\nmy folder/\n"},
		{`complete --escape -C 'cat my'`, "my\\ file.txt\nmy\\ folder/\n"},
		{`complete -C 'cat "my f'`, "my file.txt\nmy folder/\n"},
		{`complete -c ashtest5 -s x -l ex -d 'desc here'; complete -c ashtest5`,
			"complete ashtest5 -l ex -d 'desc here'\ncomplete ashtest5 -s x -d 'desc here'\n"},
		{`complete -c ashtest5 -e -s x; complete -c ashtest5`, "complete ashtest5 -l ex -d 'desc here'\n"},
		{`complete -c ashtest6 -x -k -n 'true' -a '(echo a)'; complete ashtest6`,
			"complete -k --exclusive ashtest6 -a '(echo a)' -n true\n"},
		{`complete -c ashtest7 -w 'git checkout'; complete -c ashtest7`, "complete ashtest7 -w 'git checkout'\n"},
		{`complete -c ashtest5 -e; complete -c ashtest5`, ""},
	}
	for _, tt := range tests {
		if got := run(t, sh, tt.src); got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.src, got, tt.want)
		}
	}
	errs := []struct{ src, err string }{
		{"complete -c foo -s xy", "complete: -s xy: a short option is a single character\n"},
		{"complete -c foo -n 'if'", "complete: -n if: "},
		{"complete -z", "complete: -z: unknown option\n"},
		{"complete -E -C x", "complete: -E: unknown option\n"},
		{"complete --e", "complete: --e: unknown option\n"}, // ambiguous
		{"complete -c", "complete: -c: option requires an argument\n"},
	}
	for _, tt := range errs {
		out, errb := &lockedBuffer{}, &lockedBuffer{}
		io := &IO{In: &Stream{R: strings.NewReader("")}, Out: &Stream{W: out}, Err: &Stream{W: errb}}
		if st := sh.RunString(tt.src, "test.fish", io); st == 0 || !strings.HasPrefix(errb.String(), tt.err) {
			t.Errorf("%s: status %d, stderr %q, want prefix %q", tt.src, st, errb.String(), tt.err)
		}
	}
}

func TestCompleteLazyLoad(t *testing.T) {
	sh, _ := newCompShell(t)
	dir := filepath.Join(sh.ConfigDir(), "completions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"ashtestlazy.fish": `
function __ashtestlazy_items
    echo lazy1
    echo lazy2
end
complete -c ashtestlazy -f -a '(__ashtestlazy_items)'
complete -c ashtestlazy -s q -d 'Quiet \'mode\''
set -l local_to_file 1
echo this goes nowhere
echo neither does this >&2
`,
		"ashtestsh.ash": `
complete -c ashtestsh -f -a 'from-ash'
`,
		"ashtestbad.fish": `
complete -c ashtestbad -f -a 'ok'
if this is a syntax error (
`,
		"ashtestfail.fish": `
complete -c ashtestfail -f -a 'fine'
nonexistent-command-for-ash-tests
false
`,
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	errFile, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	sh.Stderr, sh.Stdout = errFile, errFile
	checkCompletions(t, sh, []compCase{
		{"ashtestlazy ", []string{"lazy1", "lazy2"}},
		{"ashtestlazy -", []string{"-q"}},
		{"ashtestsh ", []string{"from-ash"}},
		{"ashtestbad ", nil}, // the file doesn't parse: nothing is defined
		{"ashtestfail ", []string{"fine"}},
	})
	c := sh.Complete("ashtestlazy -", 13)
	if it := find(c, "-q"); it == nil || it.Description != "Quiet 'mode'" {
		t.Errorf("-q: %+v", it)
	}
	if sh.IsSet("local_to_file") {
		t.Error("set -l in a completion file leaked")
	}
	if b, _ := os.ReadFile(errFile.Name()); len(b) > 0 {
		t.Errorf("completion files wrote to the terminal: %q", b)
	}
	if sh.Stderr != errFile || sh.Stdout != errFile {
		t.Error("the shell's streams were not restored")
	}
	// $ash_complete_path replaces ~/.config/ash/completions
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "ashtestpath.ash"), []byte("complete -c ashtestpath -f -a found\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ashtestpath2.ash"), []byte("complete -c ashtestpath2 -f -a found\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh.SetVar("ash_complete_path", []string{filepath.Join(other, "none") + ":" + other}, setOpts{scope: setGlobal})
	checkCompletions(t, sh, []compCase{
		{"ashtestpath ", []string{"found"}},
		{"ashtestpath2 ", nil},
	})
}

func TestCompletePanic(t *testing.T) {
	sh, _ := newCompShell(t)
	builtins["__ashtest_panic"] = func(sh *Shell, io *IO, args []string) int { panic("boom") }
	t.Cleanup(func() { delete(builtins, "__ashtest_panic") })
	run(t, sh, `
function __ashtest_cond
    set -l inside 1
    __ashtest_panic
end
complete -c ashtest -f -n __ashtest_cond -a never
complete -c ashtest -f -a fine
`)
	scope, depth := sh.scope, len(sh.callStack)
	if c := sh.Complete("ashtest ", 8); len(c.Items) != 0 {
		t.Errorf("got %q", texts(c))
	}
	if sh.scope != scope || len(sh.callStack) != depth || sh.shared.complete.active.Load() != nil {
		t.Error("the shell's state was not restored")
	}
	if out := run(t, sh, "set -l x ok; echo $x"); out != "ok\n" {
		t.Errorf("after the panic: %q", out)
	}
}

func TestCompleteFishArgLists(t *testing.T) {
	sh, _ := newCompShell(t)
	dir := filepath.Join(sh.ConfigDir(), "completions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// the style of fish's ls.fish and eza's vendor completions
	src := `
complete -c ashtestls -l sort -x -d "Sort criteria" -a "
        extension\t'Sort by file extension'
        none\tDon\'t\ sort
        size\t'Sort by size'
    "
complete -c ashtestls -l color -x -a "
    always\t'Always use colour'
    auto\t'Use colour if standard output is a terminal'
"
`
	if err := os.WriteFile(filepath.Join(dir, "ashtestls.fish"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	checkCompletions(t, sh, []compCase{
		{"ashtestls --sort ", []string{"extension", "none", "size"}},
		{"ashtestls --color=a", []string{"--color=always", "--color=auto"}},
	})
	c := sh.Complete("ashtestls --sort ", 17)
	for text, desc := range map[string]string{"extension": "Sort by file extension", "none": "Don't sort", "size": "Sort by size"} {
		if it := find(c, text); it == nil || it.Description != desc {
			t.Errorf("%s: %+v, want description %q", text, it, desc)
		}
	}
}

func TestCompleteUsers(t *testing.T) {
	user := os.Getenv("USER")
	if len(user) < 2 {
		t.Skip("no $USER")
	}
	sh, _ := newCompShell(t)
	c := sh.Complete("cat ~"+user[:2], 5+2)
	it := find(c, "~"+user+"/")
	if it == nil || !it.NoSpace || it.Description != sh.HomeDir() {
		t.Errorf("~%s: %q", user[:2], texts(c))
	}
	c = sh.Complete("~"+user, 1+len(user))
	if find(c, "~"+user+"/") == nil {
		t.Errorf("command position ~%s: %q", user, texts(c))
	}
}

func TestCompleteNested(t *testing.T) {
	sh, _ := newCompShell(t)
	if err := os.WriteFile(filepath.Join(sh.Get("PATH"), "ashtest-tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// like fish's __fish_complete_subcommand: complete -C inside a
	// completion script
	run(t, sh, `
function __ashtest_sub
    complete -C (commandline -ct)
end
complete -c ashtestwrap -f -a '(__ashtest_sub)'
complete -c ashtest-tool -f -a 'nested-arg'
`)
	checkCompletions(t, sh, []compCase{
		{"ashtestwrap ashtest-t", []string{"ashtest-tool"}},
	})
	if out := run(t, sh, `complete -C 'ashtest-tool n'`); out != "nested-arg\n" {
		t.Errorf("complete -C: %q", out)
	}
}
