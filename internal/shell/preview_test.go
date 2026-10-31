package shell

import (
	"math/rand"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrinoff/ash/internal/syntax"
)

// previewShell is a shell whose HOME and current directory are a temp dir
// with a few files in it.
func previewShell(t testing.TB) (*Shell, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	sh := New()
	sh.Init()
	sh.dir = dir
	for _, f := range []string{
		"a.go", "b.go", "c.go", "notes.txt", "my file.txt", ".hidden.go",
		"src/x.go", "src/deep/y.go", "src/deep/z.txt", "src/.git/HEAD",
	} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return sh, dir
}

func previewIO() *IO {
	return &IO{In: &Stream{}, Out: &Stream{W: &lockedBuffer{}}, Err: &Stream{W: &lockedBuffer{}}}
}

func TestPreviewWord(t *testing.T) {
	sh, dir := previewShell(t)
	sh.RunString(`x=hello; n=21; empty=""; list=(a "b c" d); multi="one
two"; ref=x`, "test", previewIO())
	sh.History = &integHistory{entries: []string{"make test", "ls"}}
	cases := []struct {
		buf    string
		cursor int // -1: the end of buf
		want   string
	}{
		// globs
		{"ls *.go", -1, "3 matches: a.go b.go c.go"},
		{"ls *.go -la", 4, "3 matches: a.go b.go c.go"},
		{"ls *.go -la", 7, "3 matches: a.go b.go c.go"},
		{"ls *.go -la", 3, "3 matches: a.go b.go c.go"},
		{"ls *.xyz", -1, "no matches"},
		{"ls .*.go", -1, "1 match: .hidden.go"},
		{"ls [ab].go", -1, "2 matches: a.go b.go"},
		{"ls ?.go", -1, "3 matches: a.go b.go c.go"},
		{"ls my*", -1, "1 match: 'my file.txt'"},
		{"ls src/**.go", -1, "2 matches: src/deep/y.go src/x.go"},
		{"ls **/*.txt", -1, "3 matches: 'my file.txt' notes.txt src/deep/z.txt"},
		{"ls src/*/", -1, "1 match: src/deep/"},
		{"ls *.go | wc", 7, "3 matches: a.go b.go c.go"},
		{"ls {a,b}.go", -1, "2 values: a.go b.go"},
		{"ls {a,*}.go", -1, "4 values: a.go a.go b.go c.go"},
		// variables
		{"echo $x", -1, "hello"},
		{"echo ${x}", -1, "hello"},
		{`echo "$x world"`, -1, "hello world"},
		{"echo $x/bin", -1, "hello/bin"},
		{"echo $nosuch", -1, "(unset)"},
		{`echo "$nosuch"`, -1, "(unset)"},
		{"echo $nosuch[1]", -1, "(unset)"},
		{"echo $nosuch/bin", -1, "/bin"},
		{"echo $empty", -1, "(empty)"},
		{"echo $multi", -1, "one⏎two"},
		{"echo $list", -1, "3 values: a 'b c' d"},
		{"echo $list[2]", -1, "b c"},
		{"echo $list[2..3]", -1, "2 values: 'b c' d"},
		{"echo ${list[0]}", -1, "a"},
		{"echo ${#x}", -1, "5"},
		{"echo ${x:-dflt}", -1, "hello"},
		{"echo ${nosuch:-dflt}", -1, "dflt"},
		{"echo ${x/l/L}", -1, "heLlo"},
		{"echo ${x^^}", -1, "HELLO"},
		{"echo ${x:1:3}", -1, "ell"},
		{"echo ${!ref}", -1, "hello"},
		{"echo pre$list", -1, "3 values: prea 'preb c' pred"},
		// ~
		{"cd ~", -1, dir},
		{"cd ~/src", -1, dir + "/src"},
		{"cd ~/s*", -1, "1 match: " + dir + "/src"},
		// braces
		{"cp f{,.bak}", -1, "2 values: f f.bak"},
		{"echo {1..5}", -1, "5 values: 1 2 3 4 5"},
		{"echo {01..3}", -1, "3 values: 01 02 03"},
		{"echo {a..e..2}", -1, "3 values: a c e"},
		// arithmetic
		{"echo $((1 + 2))", -1, "3"},
		{"echo $((n * 2))", -1, "42"},
		{"echo $(( $n + 1 ))", -1, "22"},
		{"echo $((x + 1))", -1, ""},   // x holds a word, not a number
		{"echo $((2 ** 10))", -1, ""}, // refused: big exponents loop
		{"echo $((1 / 0))", -1, ""},
		// history
		{"sudo !!", -1, "make test"},
		{"echo !$", -1, "test"},
		{`echo "!!"`, -1, ""},
		// nothing to expand
		{"echo plain", -1, ""},
		{"echo '*.go'", -1, ""},
		{`echo "*.go"`, -1, ""},
		{`echo \*.go`, -1, ""},
		{"echo $", -1, ""},
		{"echo a~b", -1, ""},
		{"[ -f x ]", 0, ""},
		{"git log HEAD~1", -1, ""},
		{"", 0, ""},
		{"ls *.go ", -1, ""},
		{"ls   *.go", 3, ""},
		{"echo # $x", -1, ""},
		// code is never run
		{"echo $(ls)", -1, ""},
		{"echo `ls`", -1, ""},
		{"echo (ls)", -1, ""},
		{"cat <(ls)", -1, ""},
		{"echo ${x:-$(ls)}", -1, ""},
		{"echo $x$(ls)", 6, ""},
		// but words inside command substitutions are previewed
		{"echo $(ls *.go)", 12, "3 matches: a.go b.go c.go"},
		{"echo (ls *.go)", 11, "3 matches: a.go b.go c.go"},
		{"echo `ls $x`", 10, "hello"},
		{`echo "$(echo $x)"`, 15, "hello"},
		{"diff <(sort *.txt) b", 13, "2 matches: 'my file.txt' notes.txt"},
		{"echo $(ls *.go", -1, "3 matches: a.go b.go c.go"},
		// unfinished words are closed for the preview
		{`echo "$x`, -1, "hello"},
		{`echo "$x/`, -1, "hello/"},
		{"echo ${x", -1, "hello"},
		{"echo $list[3", -1, "d"},
		{"echo $((n + 1", -1, "22"},
		{`echo $x\`, -1, "hello"},
		// where the word is
		{"FOO=$x/bin make", 5, "hello/bin"},
		{"FOO=*.go make", 6, ""},
		{"make FOO=$x", -1, "FOO=hello"},
		{"[[ -f *.go ]]", 9, ""},
		{"[[ $x == y ]]", 4, "hello"},
		{"case $x in", 7, "hello"},
		{"cat <<$x", -1, ""},
		{"cat < $x", -1, "hello"},
		{"echo $x; ls *.go", 7, "hello"},
		{"echo $x && ls *.go", -1, "3 matches: a.go b.go c.go"},
		{"for f in *.go", -1, "3 matches: a.go b.go c.go"},
		{"if test $x", -1, "hello"},
	}
	for _, c := range cases {
		cur := c.cursor
		if cur < 0 {
			cur = len(c.buf)
		}
		if got := sh.PreviewWord(c.buf, cur); got != c.want {
			t.Errorf("PreviewWord(%q, %d) = %q, want %q", c.buf, cur, got, c.want)
		}
	}
}

func TestPreviewWordTildeUser(t *testing.T) {
	sh, _ := previewShell(t)
	u, err := user.Lookup("root")
	if err != nil {
		t.Skip("no root user:", err)
	}
	if got := sh.PreviewWord("cd ~root", 8); got != u.HomeDir {
		t.Errorf("~root: got %q want %q", got, u.HomeDir)
	}
	// unknown users stay as they are, like in the real expansion
	if got := sh.PreviewWord("cd ~nosuchuser42", 16); got != "~nosuchuser42" {
		t.Errorf("~nosuchuser42: got %q", got)
	}
}

func TestPreviewWordLimits(t *testing.T) {
	sh, dir := previewShell(t)
	many := filepath.Join(dir, "many")
	os.Mkdir(many, 0o755)
	for i := 0; i < 250; i++ {
		os.WriteFile(filepath.Join(many, "f"+strconv.Itoa(i)), nil, 0o644)
	}
	got := sh.PreviewWord("ls many/*", 9)
	if !strings.HasPrefix(got, "200+ matches: many/f0 many/f1 many/f2 ") || !strings.HasSuffix(got, " …") {
		t.Errorf("many files: %q", got)
	}
	if len(got) > previewMaxText+10 {
		t.Errorf("preview too long: %d bytes", len(got))
	}
	got = sh.PreviewWord("echo {1..100000}", 16)
	if !strings.HasPrefix(got, "50+ values: 1 2 3 ") || !strings.HasSuffix(got, " …") {
		t.Errorf("long sequence: %q", got)
	}
	got = sh.PreviewWord("echo {1..1000}{1..1000}", 23)
	if !strings.HasPrefix(got, "50+ values: 11 12 13 ") {
		t.Errorf("product of sequences: %q", got)
	}
	// a cartesian product of long lists
	sh.RunString(`big=(`+strings.Repeat("x ", 2000)+`)`, "test", previewIO())
	if got := sh.PreviewWord("echo $big$big", 13); got != "" {
		t.Errorf("product of lists: %q", got)
	}
	// replacing in long values takes too long: ${x//pat/rep} tries every
	// substring
	sh.SetVar("long", []string{strings.Repeat("ab", 2000)}, setOpts{scope: setGlobal})
	sh.SetVar("short", []string{strings.Repeat("ab", 100)}, setOpts{scope: setGlobal})
	for _, buf := range []string{"echo ${long//a*b/x}", "echo ${long//a/x}", "echo ${short//a*b/x}"} {
		if got := sh.PreviewWord(buf, len(buf)); got != "" {
			t.Errorf("costly %q: %q", buf, got)
		}
	}
	if got := sh.PreviewWord("echo ${short//a/x}", 18); !strings.HasPrefix(got, "xbxb") {
		t.Errorf("cheap replace: %q", got)
	}
	if got := sh.PreviewWord("echo ${short#a*b}", 17); !strings.HasPrefix(got, "abab") {
		t.Errorf("cheap trim: %q", got)
	}
	long := sh.PreviewWord("echo $long", 10)
	if len(long) > previewMaxText+len("…") || !strings.HasSuffix(long, "…") {
		t.Errorf("long value not clipped: %d bytes", len(long))
	}
}

func TestPreviewWordDoesNotChangeTheShell(t *testing.T) {
	sh, _ := previewShell(t)
	sh.RunString(`arr=(1 2 3); i=0; ref='arr[i++]'`, "test", previewIO())
	for _, c := range []struct{ buf, want string }{
		{"echo ${newvar:=assigned}", "assigned"},
		{"echo ${newvar2=assigned}", "assigned"},
		{"echo $((m = 5))", "5"},
		{"echo $((k++))", "0"},
		{"echo $((i += 2))", "2"},
		{"echo ${arr[i++]}", "1"},
		{"echo ${arr:i++:1}", "1"},
		{"echo ${!ref}", ""}, // the subscript would assign
		{"echo $RANDOM", ""},
		{"echo $((RANDOM))", ""},
	} {
		if got := sh.PreviewWord(c.buf, len(c.buf)); got != c.want {
			t.Errorf("PreviewWord(%q) = %q, want %q", c.buf, got, c.want)
		}
	}
	for _, name := range []string{"newvar", "newvar2", "m", "k"} {
		if sh.IsSet(name) {
			t.Errorf("$%s was assigned", name)
		}
	}
	if v := sh.Get("i"); v != "0" {
		t.Errorf("$i changed to %q", v)
	}
}

func TestPreviewWordNoglob(t *testing.T) {
	sh, _ := previewShell(t)
	sh.RunString("set -f; x=1", "test", previewIO())
	if got := sh.PreviewWord("ls *.go", 7); got != "" {
		t.Errorf("set -f: got %q", got)
	}
	if got := sh.PreviewWord("ls {a,b}$x", 10); got != "2 values: a1 b1" {
		t.Errorf("set -f braces: got %q", got)
	}
}

// TestPreviewWordConcurrent backs the promise that PreviewWord only reads
// the shell: run with -race.
func TestPreviewWordConcurrent(t *testing.T) {
	sh, _ := previewShell(t)
	sh.RunString(`x=hello; list=(a b c)`, "test", previewIO())
	sh.History = &integHistory{entries: []string{"make test"}}
	bufs := []string{"ls *.go", "echo $x", "echo $list[2]", "echo ${y:=z}", "echo $((n=1))", "cd ~/src", "echo {1..9}", "sudo !!", "echo $RANDOM"}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				b := bufs[(g+i)%len(bufs)]
				sh.PreviewWord(b, len(b))
			}
		}()
	}
	wg.Wait()
}

// TestPreviewGlobMatchesGlob checks that the capped glob finds what the
// real one does.
func TestPreviewGlobMatchesGlob(t *testing.T) {
	sh, dir := previewShell(t)
	os.Symlink(filepath.Join(dir, "src"), filepath.Join(dir, "link"))
	for _, pat := range []string{
		"*", "*.go", ".*", "*/", "src/*", "src/*/*", "**", "**.go", "src/**/*.txt", "*/*.go",
		"[a-b].go", "nosuch/*", "link/*", "link/**.go", dir + "/*.go", "my\\ file.txt", "a.go",
	} {
		want := sh.glob(pat)
		b := &globBudget{deadline: time.Now().Add(time.Minute), max: 1 << 30}
		got := sh.previewGlob(pat, b)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%q:\n got %q\nwant %q", pat, got, want)
		}
	}
}

func TestExpandSeqN(t *testing.T) {
	for _, s := range []string{"1..10", "10..1", "1..10..3", "-3..3", "01..10", "-05..5", "a..f", "z..u..2", "A..c", "5..5"} {
		parts := strings.Split(s, "..")
		seq := &syntax.BraceSeq{Start: parts[0], End: parts[1]}
		if len(parts) == 3 {
			seq.Step = parts[2]
		}
		want := expandSeq(seq)
		got, more := expandSeqN(seq, 1000)
		if more || strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("{%s}: got %q (more %v) want %q", s, got, more, want)
		}
		if len(want) > 2 {
			got, more = expandSeqN(seq, 2)
			if !more || strings.Join(got, " ") != strings.Join(want[:2], " ") {
				t.Errorf("{%s} capped: got %q (more %v)", s, got, more)
			}
		}
	}
	// the ends of the int range don't overflow
	seq := &syntax.BraceSeq{Start: "9223372036854775806", End: "9223372036854775807"}
	if got, more := expandSeqN(seq, 50); len(got) != 2 || more {
		t.Errorf("near max int: %q %v", got, more)
	}
}

// TestPreviewWordOddInput feeds the scanner and parser junk at every
// cursor position. previewWord (unlike PreviewWord) lets panics through.
func TestPreviewWordOddInput(t *testing.T) {
	sh, _ := previewShell(t)
	sh.RunString(`x=1`, "test", previewIO())
	r := rand.New(rand.NewSource(1))
	const alphabet = "ab $x{}[]()<>|&;'\"`\\~!?*#=:,.-/\n\t@%^0123456789é"
	inputs := []string{
		"$((", "${", "$(", "`", "\"", "'", "\\", "$'", "$\"", "<(", ">(", "$x[", "${x:", "${!",
		"$(((((((", "${x:-${y:-${z", "\"$(\"$(\"", "{a,{b,{c", "$((1+(2", ")))", "]]]", "}}}",
		"$x[(", "~", "~/", "!!", "!$", "a=b", "a[=b", "<<", "<<-", "echo $((1/0))", "${x:1:2:3}",
		strings.Repeat("$(", 200), strings.Repeat("${x:-", 200), strings.Repeat("(", 2000),
	}
	for i := 0; i < 3000; i++ {
		n := r.Intn(24)
		b := make([]byte, 0, n)
		for j := 0; j < n; j++ {
			b = append(b, alphabet[r.Intn(len(alphabet))])
		}
		inputs = append(inputs, string(b))
	}
	for _, in := range inputs {
		step := 1 + len(in)/64 // long inputs: a sample of positions
		for cur := 0; cur <= len(in); cur += step {
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("panic on %q at %d: %v", in, cur, p)
					}
				}()
				sh.previewWord(in, cur)
			}()
		}
	}
	if sh.PreviewWord("x", -1) != "" || sh.PreviewWord("x", 5) != "" {
		t.Error("cursor out of range should give nothing")
	}
}

func FuzzPreviewWord(f *testing.F) {
	for _, s := range []string{"ls *.go", "echo $x", "echo \"$(ls *.go)\"", "cp f{,.bak}", "echo ${x:-$((1+2))}", "cat <(ls) $y[1 2]"} {
		f.Add(s, len(s))
	}
	sh, _ := previewShell(f)
	f.Fuzz(func(t *testing.T, buf string, cursor int) {
		if cursor < 0 || cursor > len(buf) {
			cursor = len(buf)
		}
		sh.previewWord(buf, cursor)
	})
}

func BenchmarkPreviewWord(b *testing.B) {
	sh, dir := previewShell(b)
	for i := 0; i < 40; i++ {
		os.WriteFile(filepath.Join(dir, "file"+strconv.Itoa(i)+".go"), nil, 0o644)
	}
	sh.RunString(`list=(a b c d e)`, "bench", previewIO())
	long := "git commit -m 'a long message about the change' --author me " + strings.Repeat("arg ", 30)
	for _, c := range []struct{ name, buf string }{
		{"plain", long},
		{"var", "echo $HOME/src"},
		{"list", "echo $list"},
		{"brace", "cp file{,.bak}"},
		{"glob", "ls *.go"},
		{"glob-deep", "ls src/**.go"},
		{"inner", "echo $(ls *.go)"},
	} {
		b.Run(c.name, func(b *testing.B) {
			cur := len(c.buf)
			if c.name == "inner" {
				cur = len(c.buf) - 1
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sh.PreviewWord(c.buf, cur)
			}
		})
	}
}
