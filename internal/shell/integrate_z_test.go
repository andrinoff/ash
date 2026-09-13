package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The expected data files and scores below were produced by running
// rupa/z's awk programs (the same ones jethrokuan/z uses for fish) on the
// same input with awk(1); only the line order differs, since awk prints
// its arrays in hash order while z keeps file order.

const (
	zDataA = "/R/alpha|5|1699990000\n/R/beta|2.5|1699000000\n/R/gamma|0.5|1690000000\n/R/delta|12.345678|1699999000\n"
	zDataB = "/R/a|8000|1600000000\n/R/b|1000.5|1650000000\n/R/c|1.005|1660000000\n/R/d|0.9|1670000000\n"
	zDataC = "/R/x|3|1690000000\n/R/x|4.5|1695000000\n/R/y|1|1600000000\n"
	zDataD = "/R/huge|1234567.5|1600000000\n/R/sci|1.5e+03|1650000000\n/R/frac|3.14159265|1660000000\n"
	zDataE = "/R/src/ash|10|1699990000\n/R/src/ash/internal/shell|25|1699000000\n/R/src/Ash-old|40|1600000000\n" +
		"/R/projects/alpha|3|1699999990\n/R/projects/alpha/docs|1.5|1699999000\n/R/Documents/Notes|7|1690000000\n" +
		"/R/old|0.0001|1000000000\n"
	zDataF = "/R/proj|1|1600000000\n/R/proj/a|50|1699990000\n/R/proj/b|30|1699000000\n/R/other|100|1699999999\n"
)

func TestZAddMatchesRupa(t *testing.T) {
	cases := []struct {
		name, data, path string
		now              int64
		want             string
	}{
		{"visit again", zDataA, "/R/beta", 1700000000,
			"/R/alpha|5|1699990000\n/R/beta|3.5|1700000000\n/R/delta|12.345678|1699999000\n"},
		{"first visit", zDataA, "/R/epsilon", 1700000000,
			"/R/alpha|5|1699990000\n/R/beta|2.5|1699000000\n/R/delta|12.345678|1699999000\n/R/epsilon|1|1700000000\n"},
		{"aging with a new path", zDataB, "/R/new", 1700000000,
			"/R/a|7920|1600000000\n/R/b|990.495|1650000000\n/R/c|0.99495|1660000000\n/R/new|0.99|1700000000\n"},
		{"aging with a known path", zDataB, "/R/b", 1700000000,
			"/R/a|7920|1600000000\n/R/b|991.485|1700000000\n/R/c|0.99495|1660000000\n"},
		{"after aging ranks below 1 go", "/R/a|7920|1600000000\n/R/b|990.495|1650000000\n/R/c|0.99495|1660000000\n/R/new|0.99|1700000000\n", "/R/a", 1700000100,
			"/R/a|7921|1700000100\n/R/b|990.495|1650000000\n"},
		{"duplicate lines: the last wins", zDataC, "/R/x", 1700000000,
			"/R/x|5.5|1700000000\n/R/y|1|1600000000\n"},
		{"duplicate lines, other path", zDataC, "/R/y", 1700000000,
			"/R/x|4.5|1695000000\n/R/y|2|1700000000\n"},
		{"awk number formats", zDataD, "/R/frac", 1700000000,
			"/R/huge|1.22222e+06|1600000000\n/R/sci|1485|1650000000\n/R/frac|4.10018|1700000000\n"},
		{"empty file", "", "/R/new", 1700000000, "/R/new|1|1700000000\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(zFormat(zAdd(zParse([]byte(c.data)), c.path, c.now)))
			if got != c.want {
				t.Errorf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

func TestZParseRoundTrip(t *testing.T) {
	cases := []struct{ name, in, want string }{
		// Untouched lines come back byte for byte, whatever the number
		// spelling.
		{"jethrokuan/z data", zDataD + "/R/int|12|1700000000\n", zDataD + "/R/int|12|1700000000\n"},
		{"no trailing newline", "/R/a|1|2", "/R/a|1|2\n"},
		// A '|' in the path survives; lines whose numbers are not numbers
		// are dropped instead of being turned into rank 0 like awk does.
		{"malformed lines", "/R/pipe|x|7|1670000000\n\n/R/bad|abc|1680000000\nnot a line\n/R/nan|NaN|1\n/R/ok|2|1600000000\n",
			"/R/pipe|x|7|1670000000\n/R/ok|2|1600000000\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(zFormat(zParse([]byte(c.in)))); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestAwkNumber(t *testing.T) {
	cases := []struct {
		v    float64
		want string
	}{
		{1, "1"}, {7920, "7920"}, {0.99, "0.99"}, {4.9005, "4.9005"},
		{0.99 * 4.9005, "4.85149"}, {1222221.825, "1.22222e+06"}, {0.00001, "1e-05"},
		{-10000, "-10000"}, {100000.5, "100000"}, {1e6, "1000000"},
	}
	for _, c := range cases {
		if got := awkNumber(c.v); got != c.want {
			t.Errorf("awkNumber(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestZFrecent(t *testing.T) {
	cases := []struct {
		rank, time float64
		want       float64
	}{
		{10, 1699990000, 166666},
		{25, 1699000000, 9259},
		{40, 1600000000, 149},
		{3, 1699999990, 89928},
		{1.5, 1699999000, 41666},
		{7, 1690000000, 262},
		{0.0001, 1000000000, 0},
		{100, 1699999999, 2999760},
		{50, 1699990000, 833333},
	}
	for _, c := range cases {
		if got := zFrecent(c.rank, c.time, 1700000000); got != c.want {
			t.Errorf("zFrecent(%v, %v) = %v, want %v", c.rank, c.time, got, c.want)
		}
	}
}

func TestZFind(t *testing.T) {
	all := func(string) bool { return true }
	cases := []struct {
		name, data, query, typ string
		exists                 func(string) bool
		want                   string
	}{
		{"frecency", zDataE, "shell", "", all, "/R/src/ash/internal/shell"},
		{"lowercase ignores case", zDataE, "ash", "", all, "/R/src/ash"},
		{"uppercase must match case", zDataE, "Ash", "", all, "/R/src/Ash-old"},
		{"case falls back like rupa/z", zDataE, "NOTES", "", all, "/R/Documents/Notes"},
		{"words in order", zDataE, "r.*ash", "", all, "/R/src/ash"},
		{"words out of order", zDataE, "shell.*internal", "", all, ""},
		{"by rank", zDataE, "src", "rank", all, "/R/src/Ash-old"},
		{"by recency", zDataE, "src", "recent", all, "/R/src/ash"},
		{"frecency picks the recent", zDataE, "src", "", all, "/R/src/ash"},
		{"a zero score is no match", zDataE, "^/R/old$", "", all, ""},
		{"common root wins", zDataF, "proj", "", all, "/R/proj"},
		{"no common root with -r", zDataF, "proj", "rank", all, "/R/proj/a"},
		{"no common root", zDataF, "/", "", all, "/R/other"},
		{"only existing directories", zDataF, "proj/", "", func(p string) bool { return p != "/R/proj/a" }, "/R/proj/b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, got, _, err := zFind(zParse([]byte(c.data)), c.query, c.query, c.typ, 1700000000, c.exists)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
	if _, _, _, err := zFind(nil, "(", "(", "", 0, all); err == nil {
		t.Error("an invalid regex should fail")
	}
}

func TestZListing(t *testing.T) {
	all := func(string) bool { return true }
	cases := []struct{ name, query, typ, want string }{
		{"frecency", "", "",
			"149        /R/src/Ash-old\n262        /R/Documents/Notes\n9259       /R/src/ash/internal/shell\n" +
				"41666      /R/projects/alpha/docs\n89928      /R/projects/alpha\n166666     /R/src/ash\n"},
		{"common root first", "alpha", "",
			"common:    /R/projects/alpha\n41666      /R/projects/alpha/docs\n89928      /R/projects/alpha\n"},
		{"rank as written", "", "rank",
			"0.0001     /R/old\n1.5        /R/projects/alpha/docs\n3          /R/projects/alpha\n7          /R/Documents/Notes\n" +
				"10         /R/src/ash\n25         /R/src/ash/internal/shell\n40         /R/src/Ash-old\n"},
		{"recency", "", "recent",
			"-700000000 /R/old\n-100000000 /R/src/Ash-old\n-10000000  /R/Documents/Notes\n-1000000   /R/src/ash/internal/shell\n" +
				"-10000     /R/src/ash\n-1000      /R/projects/alpha/docs\n-10        /R/projects/alpha\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			matches, _, common, err := zFind(zParse([]byte(zDataE)), c.query, c.query, c.typ, 1700000000, all)
			if err != nil {
				t.Fatal(err)
			}
			if got := zListing(matches, common); got != c.want {
				t.Errorf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

// zTest sets up `integrate z` with a fixed clock and some directories.
func zTest(t *testing.T) (*integTest, string) {
	t.Chdir(t.TempDir()) // cd changes the process directory; restore it
	it := newIntegTest(t)
	now := int64(1700000000)
	it.sh.shared.integr.z.now = func() time.Time { return time.Unix(now, 0) }
	it.mustRun("integrate z")
	work := it.mkdir("work", "work/alphadir/sub", "work/betadir", "skipme/thing")
	return it, work
}

func TestZRecordsDirectories(t *testing.T) {
	it, work := zTest(t)
	it.export("Z_EXCLUDE", "skip")
	it.mustRun("cd " + work + "/alphadir; cd ../betadir; cd ../alphadir; cd " + it.home + "/skipme/thing; cd ~; cd /")
	it.sh.WaitIntegrations(10 * time.Second)
	data := it.file(".local/share/z/data")
	want := work + "/alphadir|2|1700000000\n" + work + "/betadir|1|1700000000\n"
	if data != want {
		t.Errorf("data file:\n%s\nwant:\n%s", data, want)
	}
	if fi, err := os.Stat(filepath.Join(it.home, ".local/share/z")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("data dir: %v %v", fi, err)
	}

	// $Z_DATA wins over the default location; private mode records nothing.
	other := filepath.Join(it.home, "elsewhere", "zdata")
	it.export("Z_DATA", other)
	it.mustRun("cd " + work + "; fish_private_mode=1; cd " + work + "/betadir")
	it.sh.WaitIntegrations(10 * time.Second)
	if got := it.file("elsewhere/zdata"); got != work+"|1|1700000000\n" {
		t.Errorf("Z_DATA file: %q", got)
	}
}

func TestZJump(t *testing.T) {
	it, work := zTest(t)
	it.write(".local/share/z/data",
		work+"/alphadir|10|1699990000\n"+
			work+"/alphadir/sub|3|1699999990\n"+
			work+"/betadir|20|1600000000\n"+
			work+"/gone|99|1699999999\n")
	cases := []struct {
		cmd, out string
		status   int
	}{
		{"z -e beta", work + "/betadir\n", 0},
		{"z -e alpha", work + "/alphadir\n", 0}, // common root of alphadir and its sub
		{"z -e -r alpha", work + "/alphadir\n", 0},
		{"z -e -t alpha", work + "/alphadir/sub\n", 0},
		{"z -e alpha sub", work + "/alphadir/sub\n", 0},
		{"z -e gone", "", 1}, // directories that no longer exist never match
		{"z -e nothing-like-this", "", 1},
		{"z -l beta", "common:    " + work + "/betadir\n74         " + work + "/betadir\n", 0},
		{"z -q", "", 2},
		{"z -e '('", "", 2},
	}
	for _, c := range cases {
		out, _, st := it.run(c.cmd)
		if out != c.out || st != c.status {
			t.Errorf("%s: got %q status %d, want %q status %d", c.cmd, out, st, c.out, c.status)
		}
	}

	// -e prints a completed directory instead of going there.
	start := it.sh.Dir()
	if out, _, st := it.run("z -e " + work + "/betadir"); out != work+"/betadir\n" || st != 0 || it.sh.Dir() != start {
		t.Errorf("z -e /abs: got %q status %d, in %s", out, st, it.sh.Dir())
	}

	it.mustRun("z beta")
	if it.sh.Dir() != work+"/betadir" {
		t.Fatalf("z beta went to %s", it.sh.Dir())
	}
	// -c only looks below the current directory.
	it.mustRun("cd " + work + "/alphadir")
	if out := it.mustRun("z -c -e sub"); out != work+"/alphadir/sub\n" {
		t.Errorf("z -c: %q", out)
	}
	// The cds above recorded alphadir again: rank 11, visited just now.
	wantList := "common:    " + work + "/alphadir\n89928      " + work + "/alphadir/sub\n330000     " + work + "/alphadir\n"
	if out := it.mustRun("z -c"); out != wantList {
		t.Errorf("z -c lists:\n%s\nwant:\n%s", out, wantList)
	}
	it.mustRun("cd " + work + "/betadir")
	if out, _, st := it.run("z -c -e sub"); st != 1 {
		t.Errorf("z -c from betadir: %q %d", out, st)
	}
	// An absolute directory (a completion) is used as is.
	it.mustRun("z " + work + "/betadir")
	if it.sh.Dir() != work+"/betadir" {
		t.Errorf("z /abs went to %s", it.sh.Dir())
	}
	// -x forgets the current directory.
	it.mustRun("z -x")
	if strings.Contains(it.file(".local/share/z/data"), "/betadir|") {
		t.Errorf("-x kept the directory:\n%s", it.file(".local/share/z/data"))
	}
	// --clean drops directories that are gone.
	it.mustRun("z --clean")
	if strings.Contains(it.file(".local/share/z/data"), "/gone|") {
		t.Errorf("--clean kept a missing directory:\n%s", it.file(".local/share/z/data"))
	}
	_, errs, st := it.run("z nothing-like-this")
	if st != 1 || errs != "z: 'nothing-like-this' did not match any results\n" {
		t.Errorf("no match: %q %d", errs, st)
	}
}

// -c stays below the current directory: a sibling whose name merely starts
// with it (alphadir-zz next to alphadir) is not below it.
func TestZHereStaysBelow(t *testing.T) {
	it, work := zTest(t)
	it.mkdir("work/alphadir-zz/sub")
	it.write(".local/share/z/data",
		work+"/alphadir|1|1699999999\n"+
			work+"/alphadir/sub|3|1699999990\n"+
			work+"/alphadir-zz/sub|90|1699999999\n")
	it.mustRun("cd " + work + "/alphadir") // rank 2, visited now: 60000
	if out, _, st := it.run("z -c -e sub"); out != work+"/alphadir/sub\n" || st != 0 {
		t.Errorf("z -c -e sub: %q status %d", out, st)
	}
	want := "common:    " + work + "/alphadir\n60000      " + work + "/alphadir\n89928      " + work + "/alphadir/sub\n"
	if out := it.mustRun("z -c -l"); out != want {
		t.Errorf("z -c -l:\n%s\nwant:\n%s", out, want)
	}
	// From / everything is below.
	it.mustRun("cd /")
	if out := it.mustRun("z -c -e zz"); out != work+"/alphadir-zz/sub\n" {
		t.Errorf("z -c -e zz from /: %q", out)
	}
}

func TestZNeedsIntegrate(t *testing.T) {
	it := newIntegTest(t)
	if _, _, st := it.run("z foo"); st != 127 {
		t.Errorf("z without integrate: status %d, want 127", st)
	}
	if !strings.Contains(it.mustRun("integrate"), "z         available     built in\n") {
		t.Errorf("list before integrate z:\n%s", it.mustRun("integrate"))
	}
	// z needs nothing installed; integrating twice adds one hook.
	it.mustRun("integrate z; integrate z")
	if !strings.Contains(it.mustRun("integrate"), "z         enabled       built in\n") {
		t.Errorf("list after integrate z:\n%s", it.mustRun("integrate"))
	}
	if n := len(it.sh.shared.hooks.dirChange); n != 1 {
		t.Errorf("%d directory hooks, want 1", n)
	}
}

func TestZWriteKeepsOthersFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("/R/a|1|1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "data")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// A symlinked data file is followed, not replaced.
	if err := zWrite(link, zParse([]byte("/R/b|2|2\n"))); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced: %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "/R/b|2|2\n" {
		t.Errorf("target: %q", data)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Errorf("temporary files left behind: %v", ents)
	}
}
