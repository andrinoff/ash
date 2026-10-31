package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pathRunFish runs fish-syntax src in a fresh shell whose current directory
// is dir (the process directory is left alone), with stdin.
func pathRunFish(t *testing.T, dir, stdin, src string) (string, string, int) {
	t.Helper()
	sh := New()
	sh.Init()
	sh.dir = dir
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	io := &IO{In: &Stream{R: strings.NewReader(stdin)}, Out: &Stream{W: out}, Err: &Stream{W: errb}}
	st := sh.RunString(src, "test.fish", io)
	return out.String(), errb.String(), st
}

// pathSmallFixture: file, script (executable), dir/sub, link -> file,
// dangling -> nowhere, lsub -> dir/sub, -dash.
func pathSmallFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, err := range []error{
		os.MkdirAll(filepath.Join(dir, "dir/sub"), 0o755),
		os.WriteFile(filepath.Join(dir, "file"), nil, 0o644),
		os.WriteFile(filepath.Join(dir, "script"), nil, 0o755),
		os.WriteFile(filepath.Join(dir, "-dash"), nil, 0o644),
		os.Symlink("file", filepath.Join(dir, "link")),
		os.Symlink("nowhere", filepath.Join(dir, "dangling")),
		os.Symlink("dir/sub", filepath.Join(dir, "lsub")),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Expected outputs come from fish 4.8.1.
func TestPathStrings(t *testing.T) {
	out, errs, _ := runFish(t, `path basename /usr/bin/ ./foo.mp4 a
echo status=$status
path basename a b
echo status=$status
path basename -E foo.tar.gz .config ''
path dirname /usr/bin/ a // //a/b
path extension foo.mp4 bar .config archive.tar.gz
echo status=$status
path extension bar
echo status=$status
path change-extension mp4 ./foo.wmv bar ''
path change-extension '' a.b.c
path normalize /usr/bin//../../etc/fish ./my/subdirs/../sub2 -- -/foo
echo status=$status
path normalize a/b
echo status=$status
path basename -q a/b
echo status=$status`)
	want := "bin\nfoo.mp4\na\nstatus=0\na\nb\nstatus=1\nfoo.tar\n.config\n/usr\n.\n//\n//a\n.mp4\n\n\n.gz\nstatus=0\n\nstatus=1\n" +
		"./foo.mp4\nbar.mp4\n.mp4\na.b\n/etc/fish\nmy/sub2\n./-/foo\nstatus=0\na/b\nstatus=1\nstatus=0\n"
	if out != want || errs != "" {
		t.Fatalf("got %q %q\nwant %q", out, errs, want)
	}
}

func TestPathSort(t *testing.T) {
	out, errs, _ := runFish(t, `path sort 10-foo 2-bar file1 File2 b A
path sort -r 10-foo 2-bar
path sort -u --key=basename /x/a /y/a /z/b
path sort --key=dirname /b/x /a/y
path sort --key=bogus a
echo status=$status`)
	want := "2-bar\n10-foo\nA\nb\nfile1\nFile2\n10-foo\n2-bar\n/x/a\n/z/b\n/a/y\n/b/x\nstatus=2\n"
	if out != want || errs != "path sort: Invalid sort key 'bogus'\n" {
		t.Fatalf("got %q %q", out, errs)
	}
}

func TestPathFilter(t *testing.T) {
	dir := pathSmallFixture(t)
	out, errs, _ := pathRunFish(t, dir, "", `path filter file dir link dangling missing
echo status=$status
path filter -f file dir link script
path filter -d file dir link lsub
path filter -l file dir link dangling
path filter -fx file script dir
path filter -t file,dir file dir link
path filter -p exec file script dir
path filter -v file missing dangling
path filter -- -dash
path filter -d lsub/../sub sub
path is file
echo status=$status
path is -d file
echo status=$status
path filter --all file dir
echo status=$status
path filter --all file missing
echo status=$status
path filter -t bogus file
echo status=$status
path filter -p bogus file
echo status=$status`)
	want := "file\ndir\nlink\nstatus=0\nfile\nlink\nscript\ndir\nlsub\nlink\nscript\nfile\ndir\nlink\nscript\ndir\n" +
		"missing\ndangling\n./-dash\nlsub/../sub\nstatus=0\nstatus=1\nstatus=0\nstatus=1\nstatus=2\nstatus=2\n"
	if out != want || errs != "path filter: Invalid type 'bogus'\npath filter: Invalid permission 'bogus'\n" {
		t.Fatalf("got %q %q\nwant %q", out, errs, want)
	}
}

func TestPathStdin(t *testing.T) {
	dir := pathSmallFixture(t)
	for _, tc := range []struct{ stdin, src, want string }{
		{"a/b\nc/d\n", "path basename", "b\nd\n"},
		{"x/y\nz", "path basename", "y\nz\n"},
		{"a.b\n\nc\n", "path extension", ".b\n\n\n"},
		{"a/b\x00c/d", "path basename", "b\nd\n"}, // NUL-separated input is detected
		{"x/y\nz\x00w", "path basename -z", "y\nz\nw\n"},
		{"file\ndir\nmissing\n", "path filter -f", "file\n"},
		{"b\na\n", "path sort", "a\nb\n"},
		{"x/y\n", "path basename a/b", "b\n"}, // arguments win over stdin
		{"", "path basename -Z a/b c/d", "b\x00d\x00"},
		{"", "echo a/b | path basename", "b\n"},
	} {
		out, errs, _ := pathRunFish(t, dir, tc.stdin, tc.src)
		if out != tc.want || errs != "" {
			t.Errorf("%q with stdin %q: got %q %q, want %q", tc.src, tc.stdin, out, errs, tc.want)
		}
	}
}

func TestPathResolve(t *testing.T) {
	dir := pathSmallFixture(t)
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, _, st := pathRunFish(t, dir, "", `path resolve link dir/sub/.. lsub/.. missing/x/../y '' /`)
	want := strings.Join([]string{real + "/file", real + "/dir", real + "/dir", real + "/missing/y", "/"}, "\n") + "\n"
	if out != want || st != 0 {
		t.Fatalf("got %q %d\nwant %q", out, st, want)
	}
	if _, _, st := runFish(t, `path resolve ''`); st != 1 {
		t.Fatalf("resolve of nothing: status %d", st)
	}
}

func TestPathMtime(t *testing.T) {
	dir := pathSmallFixture(t)
	t0 := time.Unix(1700000000, 0)
	if err := os.Chtimes(filepath.Join(dir, "file"), t0, t0); err != nil {
		t.Fatal(err)
	}
	out, _, _ := pathRunFish(t, dir, "", "path mtime file missing\necho status=$status\npath mtime missing\necho status=$status")
	if out != "1700000000\nstatus=0\nstatus=1\n" {
		t.Fatalf("got %q", out)
	}
	out, _, _ = pathRunFish(t, dir, "", "path mtime -R file")
	ago, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if want := time.Now().Unix() - 1700000000; err != nil || ago < want-5 || ago > want {
		t.Fatalf("got %q", out)
	}
}

func TestPathErrors(t *testing.T) {
	out, errs, _ := runFish(t, `path
echo status=$status
path bogus a
echo status=$status
path basename -f a
echo status=$status
path change-extension
echo status=$status
path sort -k
echo status=$status`)
	wantErr := "path: missing subcommand\npath bogus: invalid subcommand\npath basename: -f: unknown option\n" +
		"path change-extension: missing argument\npath sort: -k: option requires an argument\n"
	if out != "status=2\nstatus=2\nstatus=2\nstatus=2\nstatus=2\n" || errs != wantErr {
		t.Fatalf("got %q %q", out, errs)
	}
	out, _, st := runFish(t, "path --help")
	if st != 0 || !strings.HasPrefix(out, "path - ") {
		t.Fatalf("help: %q %d", out, st)
	}
}

// The tests below compare with fish itself. Run them with ASH_DIFF_FISH=1
// when fish 4.8 is installed.

func pathFixture(t *testing.T) string {
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "d/sub"), 0o755))
	must(os.Mkdir(filepath.Join(dir, "e"), 0o755))
	for _, f := range []string{"f", "d/f2", ".hidden", "x", "nr", "suid", "-f", "a.b"} {
		must(os.WriteFile(filepath.Join(dir, f), nil, 0o644))
	}
	must(os.Chmod(filepath.Join(dir, "x"), 0o755))
	must(os.Chmod(filepath.Join(dir, "nr"), 0))
	must(os.Chmod(filepath.Join(dir, "suid"), 0o4755))
	for link, target := range map[string]string{"lf": "f", "ld": "d", "broken": "nonexist", "loop1": "loop2", "loop2": "loop1", "lsub": "d/sub"} {
		must(os.Symlink(target, filepath.Join(dir, link)))
	}
	must(syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o644))
	return dir
}

type pathCase struct {
	args  []string
	stdin *string
}

func fishPath(t *testing.T, dir string, c pathCase) string {
	tmp := t.TempDir()
	data := ""
	for _, a := range c.args {
		data += a + "\x00"
	}
	if err := os.WriteFile(filepath.Join(tmp, "args"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	redir := ""
	if c.stdin != nil {
		os.WriteFile(filepath.Join(tmp, "in"), []byte(*c.stdin), 0o644)
		redir = " < " + filepath.Join(tmp, "in")
	}
	script := `set -l __args (string split0 < ` + filepath.Join(tmp, "args") + `)
cd ` + dir + `
path $__args >` + filepath.Join(tmp, "out") + ` 2>` + filepath.Join(tmp, "err") + redir + `
echo $status >` + filepath.Join(tmp, "st")
	if b, err := exec.Command("fish", "--no-config", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("fish: %v %s", err, b)
	}
	out, _ := os.ReadFile(filepath.Join(tmp, "out"))
	errb, _ := os.ReadFile(filepath.Join(tmp, "err"))
	st, _ := os.ReadFile(filepath.Join(tmp, "st"))
	return pathNorm(string(out), string(errb), strings.TrimSpace(string(st)))
}

func ashPath(t *testing.T, dir string, c pathCase) string {
	sh := New()
	sh.Init()
	sh.dir = dir
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	in := &Stream{TTY: true}
	if c.stdin != nil {
		in = &Stream{R: strings.NewReader(*c.stdin)}
	}
	io := &IO{In: in, Out: &Stream{W: out}, Err: &Stream{W: errb}}
	st := bPath(sh, io, append([]string{"path"}, c.args...))
	return pathNorm(out.String(), errb.String(), strconv.Itoa(st))
}

// pathNorm keeps the first error line (fish adds a backtrace) and treats
// fish's man page and ash's usage text as the same help.
func pathNorm(out, errs, st string) string {
	e, _, _ := strings.Cut(errs, "\n")
	if strings.HasPrefix(out, "PATH(1)") || strings.HasPrefix(out, "path - ") {
		out = "<help>"
	}
	return "status " + st + "\nout " + strconv.Quote(out) + "\nerr " + e
}

func pathSkipUnlessFish(t *testing.T) {
	if os.Getenv("ASH_DIFF_FISH") == "" {
		t.Skip("set ASH_DIFF_FISH=1 to compare with fish")
	}
	if _, err := exec.LookPath("fish"); err != nil {
		t.Skip("fish not installed")
	}
}

func TestPathDiffFish(t *testing.T) {
	pathSkipUnlessFish(t)
	dir := pathFixture(t)
	s := func(v string) *string { return &v }
	var cases []pathCase
	strs := []string{"", "/", "//", "///", "//a", "///a", "//a/b", "/a//b//c", ".", "..", "./", "../", "a", "a/", "a//", "/a", "/a/", "a/b", "a/b/",
		"a//b", "./a", "../a", "/a/b/c", "foo.mp4", "./foo.mp4", "/usr/bin/", "a b/c d", ".config", ".config.", ".config.d", "a/.", "a/..",
		"/.", "/..", ".a", "..a", "a.", "a..", ".a.b", "/foo/.bar", "foo/.bar.baz", "-foo", "--", "-", "a.b.c", "a/b.c/d", "a/b.c/",
		"a/b.cd//", "a/./b", "a/../b", "./a/.", "./a/..", "../..", "../a/../..", "/../a", "//a//b/", "-/foo", "./-foo", "a/b/../../..",
		".//", "/a/..", "/a/../", "a/b/c/", "./../a", "a/../../b", "/./a", "a/b/./c/./", "tar.gz", "a/b.c.", "é/ü.ñ", "a\nb/c.d"}
	for _, sub := range []string{"basename", "dirname", "extension", "normalize"} {
		for _, x := range strs {
			cases = append(cases, pathCase{args: []string{sub, "--", x}})
		}
		cases = append(cases, pathCase{args: []string{sub, "--", "a/b", "c", "d.e/", ""}}, pathCase{args: []string{sub, "-q", "--", "a", "b/c"}},
			pathCase{args: []string{sub, "-q", "--", "a"}}, pathCase{args: []string{sub, "-Z", "--", "a/b.c", "d"}})
	}
	for _, x := range strs {
		cases = append(cases, pathCase{args: []string{"basename", "-E", "--", x}})
	}
	for _, ext := range []string{"mp4", ".mp4", "", "..x", "x.", ".", "tar.gz", "a/b"} {
		for _, x := range []string{"./foo.wmv", "../banana", ".config", ".config.d", ".config.", "a/b.c/", "a/b/", "a/", "/", "", ".", "..", "foo", "a//", "foo.c"} {
			cases = append(cases, pathCase{args: []string{"change-extension", ext, "--", x}})
		}
	}
	cases = append(cases,
		pathCase{args: []string{"change-extension", "x"}}, pathCase{args: []string{"change-extension"}}, pathCase{args: []string{"change-extension", "-q", "x", "a.b"}},
		pathCase{args: []string{"change-extension", "-q", "x"}}, pathCase{args: []string{"change-extension", "--", "-x", "a.b"}},
		pathCase{args: []string{"change-extension", "x", "-foo.c"}}, pathCase{args: []string{"change-extension", "x", "a.b", "c"}})
	all := []string{"f", "x", "lf", "ld", "broken", "loop1", "fifo", "nr", "suid", "d", "e", "nonexist", "", ".hidden", "lsub", "-f", "/dev/null", "ld/..", "lsub/..", "d/sub/../f2"}
	for _, flags := range [][]string{{}, {"-f"}, {"-d"}, {"-l"}, {"-r"}, {"-w"}, {"-x"}, {"-fx"}, {"-fd"}, {"-t", "file"}, {"-t", "dir,file"},
		{"-t", "link"}, {"-t", "fifo"}, {"-t", "char"}, {"-t", "block"}, {"-t", "socket"}, {"--type=link,dir"}, {"-t", "file", "-t", "dir"},
		{"-p", "read"}, {"-p", "write"}, {"-p", "exec"}, {"-p", "suid"}, {"-p", "sgid"}, {"-p", "user"}, {"-p", "group"}, {"-p", "read,write"},
		{"-p", "exec", "-t", "file"}, {"-v"}, {"-v", "-f"}, {"-vq", "-f"}, {"-q", "-f"}, {"-t", ""}, {"-t", "file,"}, {"-fl"}, {"-l", "-t", "file"},
		{"-d", "-t", "link"}, {"--all"}, {"--all", "-v"}, {"--all", "-f"}, {"-Z"}, {"-z"}, {"--inv"}, {"-tfile"}, {"-pexec"}} {
		for _, sub := range []string{"filter", "is"} {
			args := append(append([]string{sub}, flags...), "--")
			cases = append(cases, pathCase{args: append(args, all...)})
		}
	}
	cases = append(cases,
		pathCase{args: []string{"filter", "--all", "--", "f", "x"}}, pathCase{args: []string{"filter", "--all", "--", "f", "nonexist"}},
		pathCase{args: []string{"filter", "--all", "-f", "--", "f", "d"}}, pathCase{args: []string{"filter", "--all", "-v", "--", "nonexist", "nope"}},
		pathCase{args: []string{"filter", "--all", "-v", "--", "nonexist", "f"}}, pathCase{args: []string{"filter", "--all"}},
		pathCase{args: []string{"filter", "-t", "bogus", "--", "f"}}, pathCase{args: []string{"filter", "-p", "bogus", "--", "f"}},
		pathCase{args: []string{"filter", "-t", "--", "f"}}, pathCase{args: []string{"filter", "-t"}}, pathCase{args: []string{"filter", "--type"}},
		pathCase{args: []string{"filter", "-p", "bogus", "-t", "bogus", "f"}}, pathCase{args: []string{"is", "-t", "bogus", "f"}},
		pathCase{args: []string{"filter", "-dt"}}, pathCase{args: []string{"filter", "-td", "f"}}, pathCase{args: []string{"filter", "-R", "a"}},
		pathCase{args: []string{"filter", "--all=1", "f"}}, pathCase{args: []string{"filter", "-type", "file", "f"}},
	)
	for _, x := range []string{"f", "./f", "lf", "ld", "ld/", "ld/..", "d/sub/..", "d/../d/./sub", "nonexist", "nonexist/foo/../bar", "broken",
		"broken/x", "loop1", "loop1/x", "//tmp", "/", "", ".", "..", "lsub", "lsub/../..", "/nonexist/a/../b", "nope/../d", "d/./sub/", "fifo", "-x", "f/x", "f/..", "nonexist/.."} {
		cases = append(cases, pathCase{args: []string{"resolve", "--", x}})
	}
	cases = append(cases, pathCase{args: []string{"resolve", "d", "e"}}, pathCase{args: []string{"resolve", "-q", "nonexist"}},
		pathCase{args: []string{"resolve", "-q", ""}}, pathCase{args: []string{"resolve", "-R", "a"}},
		pathCase{args: []string{"mtime", "--", "nonexist", "", "broken"}}, pathCase{args: []string{"mtime", "-q", "nonexist"}},
		pathCase{args: []string{"mtime", "-q", "f"}}, pathCase{args: []string{"mtime", "-f", "a"}},
	)
	for _, args := range [][]string{{"10-foo", "2-bar"}, {"--reverse", "10-foo", "2-bar"}, {"-r", "10-foo", "2-bar", "2-bar"}, {"b", "a", "c"}, {},
		{"--key=basename", "/x/b", "/a/c", "/b/a"}, {"--key=dirname", "/x/b", "/a/c", "/b/a", "/a/a"}, {"--key=path", "/x/b", "/a/c"},
		{"--key", "basename", "/x/b", "/a/c"}, {"--key=bogus", "/x/b"}, {"-u", "a", "b", "a", "c", "b"}, {"-u", "--key=basename", "/x/a", "/y/a", "/z/b"},
		{"-u", "--key=dirname", "/x/a", "/y/a", "/x/b"}, {"-ur", "b", "a", "b"}, {"A", "a", "B", "b", "_", "1", "10", "9", "a1", "a01", "a001", "a2"},
		{"a/b", "a/b/c", "a-b", "a.b", "ab"}, {"a b", "a"}, {"-r", "--key=basename", "/x/a", "/y/a", "/z/b"}, {"-ru", "--key=basename", "/x/a", "/y/a", "/z/b"},
		{"--key=dirname", "a", "b/c", ""}, {"--key=basename", "a/", "b//", "/"}, {"--", "-a", "b"}, {"-q", "b", "a"}, {"-k", "basename", "a"}, {"-rk", "x"},
		{"x1y", "x01y", "x1 y", "x1  z", "x 1", "X1y", "Éa", "éb", "ea", "z", "Z", "[", "`", "~", "a~", "a_", "a-", "a.", "Ab", "aB"},
		{"file10.txt", "file9.txt", "file010.txt", "file1.txt", "File2.txt", "file2.TXT", "1.2.3", "1.10.1", "1.2.10", "v1.0-rc1", "v1.0-rc10", "v1.0"}} {
		cases = append(cases, pathCase{args: append([]string{"sort"}, args...)})
	}
	cases = append(cases,
		pathCase{args: nil}, pathCase{args: []string{"bogus"}}, pathCase{args: []string{"bogus", "a"}}, pathCase{args: []string{"-q"}},
		pathCase{args: []string{"BaseName", "a"}}, pathCase{args: []string{"basename", "-f", "a"}}, pathCase{args: []string{"basename", "--foo", "a"}},
		pathCase{args: []string{"basename", "-v", "a"}}, pathCase{args: []string{"basename", "-E", "-Z", "a.b"}}, pathCase{args: []string{"basename", "-EZ", "a.b"}},
		pathCase{args: []string{"basename", "-qZ", "a/b"}}, pathCase{args: []string{"basename", "--quiet", "a/b"}}, pathCase{args: []string{"basename", "--null-out", "a/b"}},
		pathCase{args: []string{"basename", "--null-in", "a/b"}}, pathCase{args: []string{"basename", "--no-ext", "a.b"}}, pathCase{args: []string{"basename", "--null", "a.b"}},
		pathCase{args: []string{"basename", "--q", "a/b"}}, pathCase{args: []string{"dirname", "-E", "a/b"}}, pathCase{args: []string{"extension", "-E", "a.b"}},
		pathCase{args: []string{"basename", "-Ef", "a"}}, pathCase{args: []string{"basename", "-fE", "a"}}, pathCase{args: []string{"basename", "a", "-q"}},
		pathCase{args: []string{"basename", "a", "--", "-q"}}, pathCase{args: []string{"basename", "-no-ext", "a.b"}}, pathCase{args: []string{"basename", "-foo", "a"}},
		pathCase{args: []string{"basename", "--quiet=1", "a"}}, pathCase{args: []string{"sort", "-reverse", "a"}}, pathCase{args: []string{"basename", "-", "a"}},
		pathCase{args: []string{"normalize", "-v", "a"}}, pathCase{args: []string{"basename", "-h"}}, pathCase{args: []string{"--help"}},
		pathCase{args: []string{"extension", "-q", "a", "b.c"}}, pathCase{args: []string{"extension", "-q", "a"}}, pathCase{args: []string{"dirname", "-q", "."}},
		pathCase{args: []string{"basename"}, stdin: s("a/b\nc/d\n")}, pathCase{args: []string{"basename"}, stdin: s("a/b\nc/d")},
		pathCase{args: []string{"basename"}, stdin: s("a/b\n\nc/d\n")}, pathCase{args: []string{"extension"}, stdin: s("a.b\n\nc\n")},
		pathCase{args: []string{"basename"}, stdin: s("a/b\x00c/d\x00")}, pathCase{args: []string{"basename"}, stdin: s("a/b\x00c/d")},
		pathCase{args: []string{"basename"}, stdin: s("a\n/b\x00c/d\x00")}, pathCase{args: []string{"basename", "-z"}, stdin: s("a/b\x00c/d\x00")},
		pathCase{args: []string{"basename", "-z"}, stdin: s("a/b\nc/d\n")}, pathCase{args: []string{"basename", "-z"}, stdin: s("a/b\nc/d\n\x00x/y")},
		pathCase{args: []string{"basename"}, stdin: s("a/b\nc/d\nx\x00y")}, pathCase{args: []string{"basename"}, stdin: s("")},
		pathCase{args: []string{"basename"}, stdin: s("\n")}, pathCase{args: []string{"extension"}, stdin: s("\n")},
		pathCase{args: []string{"basename"}, stdin: s("-a\n--\n-q\n")}, pathCase{args: []string{"change-extension", "x"}, stdin: s("a.b\nc\n")},
		pathCase{args: []string{"basename", "-q"}, stdin: s("a/b\nc/d\n")}, pathCase{args: []string{"sort"}, stdin: s("b\na\n")},
		pathCase{args: []string{"basename"}, stdin: s("a/b\r\n")}, pathCase{args: []string{"basename", "--"}, stdin: s("a/b\n")},
		pathCase{args: []string{"filter", "-f"}, stdin: s("f\nd\n-f\nnonexist\n")}, pathCase{args: []string{"resolve"}, stdin: s("ld/..\n")},
		pathCase{args: []string{"normalize", "-Z"}, stdin: s("a//b\n-c\n")}, pathCase{args: []string{"basename"}, stdin: s(strings.Repeat("x/", 3000) + "a\x00b/c")},
		pathCase{args: []string{"basename"}, stdin: s(strings.Repeat("x", 5000) + "\na/b\x00c")},
	)
	for _, c := range cases {
		want, got := fishPath(t, dir, c), ashPath(t, dir, c)
		if want != got {
			in := ""
			if c.stdin != nil {
				in = " stdin=" + strconv.Quote(*c.stdin)
				if len(in) > 80 {
					in = in[:80]
				}
			}
			t.Errorf("path %q%s\n--- fish\n%s\n--- ash\n%s", c.args, in, want, got)
		}
	}
}

func TestPathSortDiffFish(t *testing.T) {
	pathSkipUnlessFish(t)
	dir := t.TempDir()
	alpha := []rune("aAbB019-._ /é~[Z0")
	seed := uint32(12345)
	rnd := func(n int) int {
		seed = seed*1664525 + 1013904223
		return int(seed>>8) % n
	}
	for round := 0; round < 30; round++ {
		var list []string
		for i := 0; i < 40; i++ {
			var b []rune
			for j := rnd(7); j >= 0; j-- {
				b = append(b, alpha[rnd(len(alpha))])
			}
			list = append(list, string(b))
		}
		for _, extra := range [][]string{nil, {"-r"}, {"--key=basename"}, {"-u", "--key=dirname"}} {
			c := pathCase{args: append(append([]string{"sort"}, extra...), append([]string{"--"}, list...)...)}
			if want, got := fishPath(t, dir, c), ashPath(t, dir, c); want != got {
				t.Fatalf("path %q\n--- fish\n%s\n--- ash\n%s", c.args, want, got)
			}
		}
	}
}
