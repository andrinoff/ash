package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadIFSTrimming(t *testing.T) {
	// Expected values are what bash prints.
	for _, c := range []struct{ src, stdin, want string }{
		{`IFS= read -r x; echo "[$x]"`, "  a  b  \n", "[  a  b  ]\n"},
		{`read -r x; echo "[$x]"`, "  a  b  \n", "[a  b]\n"},
		{`IFS=' ' read -r x; echo "[$x]"`, " \t a b \t \n", "[\t a b \t]\n"},
		{`read; echo "[$REPLY]"`, "  a  \n", "[  a  ]\n"},
		{`IFS=: read a b; echo "[$a][$b]"`, "a: b\n", "[a][ b]\n"},
		{`IFS=: read a b; echo "[$a][$b]"`, "x:y:\n", "[x][y]\n"},
		{`IFS=: read a b; echo "[$a][$b]"`, "x:y:z:\n", "[x][y:z:]\n"},
		{`IFS=: read a; echo "[$a]"`, "x:\n", "[x]\n"},
		{`IFS=: read a; echo "[$a]"`, ":x:\n", "[:x:]\n"},
		{`IFS=: read a b c; echo "[$a][$b][$c]"`, "x::y\n", "[x][][y]\n"},
		{`IFS=' :' read a b; echo "[$a][$b]"`, "a : b\n", "[a][b]\n"},
		{`IFS= read a b; echo "[$a][$b]"`, "x y\n", "[x y][]\n"},
		{`read -r x y z; echo "[$x][$y][$z]"`, "one  two   \n", "[one][two][]\n"},
		{`IFS=: read -a arr; echo "${#arr[@]} [${arr[0]}][${arr[1]}][${arr[2]}]"`, "a::b:\n", "3 [a][][b]\n"},
	} {
		out, errOut, _ := runScriptIn(t, c.src, c.stdin)
		if out != c.want {
			t.Errorf("%s with %q: got %q (stderr %q), want %q", c.src, c.stdin, out, errOut, c.want)
		}
	}
}

func TestReadBackslashes(t *testing.T) {
	for _, c := range []struct{ src, stdin, want string }{
		{`read x; echo "[$x]"`, "a\\\nb\n", "[ab]\n"},
		{`read -r x; echo "[$x]"`, "a\\\nb\n", "[a\\]\n"},
		{`read a b; echo "[$a][$b]"`, "x\\ y z\n", "[x y][z]\n"},
		{`IFS=: read a b; echo "[$a][$b]"`, "x\\:y:z\n", "[x:y][z]\n"},
		{`read -d , x; echo "[$x]"`, "a\\,b,c", "[a,b]\n"},
		{`read -r -d , x; echo "[$x]"`, "a\\,b,c", "[a\\]\n"},
		{`read a b; echo "[$a][$b]"`, "  x  \\  \n", "[x][ ]\n"},
		{`read a; echo "[$a]"`, "x\\ \n", "[x ]\n"},
		{`read a b c; echo "[$a][$b][$c] $?"`, "x\\\n", "[x][][] 1\n"},
	} {
		out, errOut, _ := runScriptIn(t, c.src, c.stdin)
		if out != c.want {
			t.Errorf("%s with %q: got %q (stderr %q), want %q", c.src, c.stdin, out, errOut, c.want)
		}
	}
}

func TestReadOptions(t *testing.T) {
	for _, c := range []struct{ src, stdin, want string }{
		// combined short options with attached values
		{`read -rn1 x; read -rsn2 y; read -n1 -r z; echo "[$x][$y][$z]"`, "abcdef\n", "[a][bc][d]\n"},
		{`read -n 2 x; echo "[$x]"`, "héllo\n", "[hé]\n"},
		// bash uses the first character of -d
		{`read -d ab x; echo "[$x]"`, "xbyaz", "[xby]\n"},
		{`read -d é x; echo "[$x]"`, "aébé", "[a]\n"},
		{`read -N 4 x; echo "[$x]"`, "l1\nl2\n", "[l1\nl]\n"},
		{`read -n abc x; echo $?`, "", "2\n"},
		{`read -e -i default x; echo "[$x]"`, "typed\n", "[typed]\n"},
		{`read -t 0; echo $?`, "x\n", "0\n"},
		{`read -r -d '' a; read -r -d '' b; echo "[$a][$b]"`, "x\x00y\x00", "[x][y]\n"},
	} {
		out, errOut, _ := runScriptIn(t, c.src, c.stdin)
		if out != c.want {
			t.Errorf("%s with %q: got %q (stderr %q), want %q", c.src, c.stdin, out, errOut, c.want)
		}
	}
}

func TestReadFishLineMode(t *testing.T) {
	for _, c := range []struct{ src, stdin, want string }{
		{`read -L a b; echo "[$a][$b] $status"`, "x y\nz w\n", "[x y][z w] 0\n"},
		{`read -L a b; echo "[$a][$b] $status" (count $b)`, "x y\n", "[x y][] 1 0\n"},
		{`read -L a; echo "[$a]"`, "  x  \n", "[  x  ]\n"},
		{`read -L a b; echo "[$a][$b] $status"`, "abc", "[abc][] 1\n"},
	} {
		out, errOut, _ := runScriptIn(t, c.src, c.stdin)
		if out != c.want {
			t.Errorf("%s with %q: got %q (stderr %q), want %q", c.src, c.stdin, out, errOut, c.want)
		}
	}
}

func TestReadLeavesFileOffsetAfterRecord(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("x", 5000)
	p := filepath.Join(dir, "in.txt")
	if err := os.WriteFile(p, []byte("l1\n"+long+"\nl3 x\nl4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, _ := runScript(t, `{ read -r a; read -r b; read -r c d; echo "${#a} ${#b} [$c][$d]"; cat; } < `+Quote(p))
	if want := "2 5000 [l3][x]\nl4\n"; out != want {
		t.Fatalf("got %q (stderr %q), want %q", out, errOut, want)
	}
	out, _, _ = runScript(t, `n=0; while read -r line; do n=$((n+1)); done < `+Quote(p)+`; echo $n`)
	if out != "4\n" {
		t.Fatalf("while read: got %q", out)
	}
}

func TestReadRunsTrapsWhileWaiting(t *testing.T) {
	// A trapped signal (say ctrl-c with trap '...; exit' INT) waited
	// for read to get a whole line before its handler ran. As in bash,
	// the handler runs while read waits, and read goes on waiting if
	// the handler doesn't exit.
	run := func(src string, feed func(w *os.File)) (string, int, time.Duration) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		go feed(w)
		sh := New()
		sh.Init()
		out := &lockedBuffer{}
		io := &IO{In: fileStream(r, false), Out: &Stream{W: out}, Err: &Stream{W: &lockedBuffer{}}}
		start := time.Now()
		st := sh.RunString(src, "test", io)
		if exit, s := sh.ShouldExit(); exit {
			st = s
		}
		return out.String(), st, time.Since(start)
	}
	out, st, d := run(`trap 'echo caught; exit 3' USR1
( sleep 0.1; kill -USR1 $$ ) &
read -t 4 x
echo "not reached $?"`, func(*os.File) {})
	if out != "caught\n" || st != 3 || d > 2*time.Second {
		t.Fatalf("exit in trap: got %q status %d after %v", out, st, d)
	}
	out, st, _ = run(`trap 'echo usr1' USR1
( sleep 0.1; kill -USR1 $$ ) &
read -t 4 x; echo "[$x] $?"`, func(w *os.File) {
		time.Sleep(400 * time.Millisecond)
		w.WriteString("line\n")
	})
	if out != "usr1\n[line] 0\n" || st != 0 {
		t.Fatalf("trap without exit: got %q status %d", out, st)
	}
}
