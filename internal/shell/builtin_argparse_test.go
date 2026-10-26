package shell

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The expected outputs below were produced by fish 4.8.1 running the same
// scripts.
var argparseScripts = []struct {
	name, src, out, err string
}{
	{"basics", `function f
    argparse h/help 'n/name=' v/verbose -- $argv; or return
    echo "h=[$_flag_h] help=[$_flag_help] n=[$_flag_n] name=[$_flag_name] v=[$_flag_v]"
    echo "argv=[$argv] count="(count $argv) "opts=[$argv_opts]"
end
f -n bob x y
f --name=bob -vh x --help -- -z
f -vnbob --verbose
f -n -v
f -z a
echo status=$status
f a --name
echo status=$status`,
		"h=[] help=[] n=[bob] name=[bob] v=[]\nargv=[x y] count=2 opts=[-n bob]\n" +
			"h=[-h --help] help=[-h --help] n=[bob] name=[bob] v=[-v]\nargv=[x -z] count=2 opts=[--name=bob -vh --help]\n" +
			"h=[] help=[] n=[bob] name=[bob] v=[-v --verbose]\nargv=[] count=0 opts=[-vnbob --verbose]\n" +
			"h=[] help=[] n=[-v] name=[-v] v=[]\nargv=[] count=0 opts=[-n -v]\nstatus=2\nstatus=2\n",
		"f: -z: unknown option\nf: --name: option requires an argument\n"},
	{"optional and repeated values", `function g
    argparse 'o/opt=?' 'm/multi=+' 'a/any=*' -- $argv; or return
    echo "o="(count $_flag_o)"[$_flag_o] m=[$_flag_m] a="(count $_flag_a)"[$_flag_a] argv=[$argv]"
end
g -ofoo --opt x -m 1 --multi=2 -m3
g --opt=bar -a -aone --any
g -o`,
		"o=0[] m=[1 2 3] a=0[] argv=[x]\no=1[bar] m=[] a=3[ one ] argv=[]\no=0[] m=[] a=0[] argv=[]\n", ""},
	{"integer flags", `function num
    argparse 'n#lines' v -- $argv; or return
    echo "n=[$_flag_n] lines=[$_flag_lines] argv=[$argv]"
end
num -5 file
num -n 10 --lines=20 -v -3
num -n x
echo status=$status
num -v5
echo status=$status
function neg
    argparse '#-max' -- $argv; or return
    echo "max=[$_flag_max]"
end
neg -7
neg --9`,
		"n=[5] lines=[5] argv=[file]\nn=[3] lines=[3] argv=[]\nstatus=1\nstatus=2\nmax=[7]\nmax=[-9]\n",
		"num: Value 'x' for flag 'n' is not an integer\nnum: -v5: unknown option\n"},
	{"validation", `function isok
    echo "checking $_flag_name=$_flag_value for $_argparse_cmd"
    test "$_flag_value" = ok
end
function v
    argparse 'c/check=!isok' -- $argv; or return
    echo "check=$_flag_check"
end
v -c ok
v --check bad
echo status=$status
function r
    argparse 'n/num=!_validate_int --min 1 --max 10' -- $argv; or return
    echo "num=$_flag_num"
end
r -n 5
r -n 0
echo status=$status
r --num 11
r --num=x`,
		"check=ok\nstatus=1\nnum=5\nstatus=1\n",
		"checking c=ok for v\nchecking check=bad for v\nr: Value '0' for flag 'n' less than min allowed of '1'\n" +
			"r: Value '11' for flag 'num' greater than max allowed of '10'\nr: Value 'x' for flag 'num' is not an integer\n"},
	{"exclusive and argument counts", `function x
    argparse -x a,b -x b,long a b long -- $argv; or return
    echo ok $_flag_a $_flag_b $_flag_long
end
x -a --long
x -a -b
echo status=$status
x -b --long
echo status=$status
function mm
    argparse -N 1 --max-args=2 -- $argv; or return
    echo "args: $argv"
end
mm
echo status=$status
mm a b c
echo status=$status
mm a b
argparse -n mytool v -- -q
echo status=$status`,
		"ok -a --long\nstatus=2\nstatus=2\nstatus=1\nstatus=1\nargs: a b\nstatus=2\n",
		"x: a/a b/b: options cannot be used together\nx: b/b long: options cannot be used together\n" +
			"mm: expected >= 1 arguments; got 0\nmm: expected <= 2 arguments; got 3\nmytool: -q: unknown option\n"},
	{"stop, ignore and move unknown", `function s
    argparse -s v -- $argv; or return
    echo "v=[$_flag_v] argv=[$argv]"
end
s -v cmd -v --other
function i
    argparse -i v 'n=' -- $argv; or return
    echo "v=[$_flag_v] n=[$_flag_n] argv=[$argv] opts=[$argv_opts]"
end
i -x -v --long=1 -n 3 file -vz
function u
    argparse -u 'k/keep=&' v -- $argv; or return
    echo "keep=[$_flag_keep] v=[$_flag_v] argv=[$argv] opts=[$argv_opts]"
end
u -x -k one -vkthree --zz file`,
		"v=[-v] argv=[cmd -v --other]\nv=[-v -v] n=[3] argv=[-x --long=1 file -z] opts=[-v -n 3 -v]\n" +
			"keep=[three] v=[-v] argv=[file] opts=[-x -v --zz]\n", ""},
	{"long option forms", `function l
    argparse help version verbose 'name=' -- $argv; or return
    echo "help=[$_flag_help] version=[$_flag_version] verbose=[$_flag_verbose] name=[$_flag_name]"
end
l --hel --vers -verb -name=x
l --ver
echo status=$status
function st
    argparse -S h/help -- $argv; or return
    echo "help=[$_flag_help]"
end
st --help
st --hel
echo status=$status`,
		"help=[--help] version=[--version] verbose=[--verbose] name=[x]\nstatus=2\nhelp=[--help]\nstatus=2\n",
		"l: --ver: unknown option\nst: --hel: unknown option\n"},
	{"usage errors", `argparse 'n/name=x' -- a
echo status=$status
argparse h/help
echo status=$status
argparse h h -- -h
echo status=$status
argparse '?' -- -h
echo status=$status
argparse -N x -- a
echo status=$status
argparse --bogus -- a
echo status=$status
argparse -x h h -- -h
echo status=$status`,
		"status=1\nstatus=2\nstatus=1\nstatus=1\nstatus=2\nstatus=2\nstatus=1\n",
		"argparse: Invalid option spec 'n/name=x' at char 'x'\nargparse: Missing -- separator\n" +
			"argparse: Long flag 'h' already defined\nargparse: Short flag '?' invalid, must be alphanum or '#'\n" +
			"argparse: Invalid --min-args value 'x'\nargparse: --bogus: unknown option\nargparse: exclusive flag string 'h' is not valid\n"},
	{"scoping", `function d
    set -l _flag_color auto
    argparse 'color=' -- $argv
    echo "color=$_flag_color"
end
d
d --color=red
function c
    if not argparse q/quiet -- $argv
        return 1
    end
    set -q _flag_quiet; and echo quiet
    echo "argv=[$argv]"
end
c -q one
c --nope
echo status=$status
function e
    argparse dry-run -- $argv
    echo "dry=[$_flag_dry_run] argv="(count $argv)
end
e --dry-run
argparse -- a b
echo "top: $argv"`,
		"color=auto\ncolor=red\nquiet\nargv=[one]\nstatus=1\ndry=[--dry-run] argv=0\ntop: a b\n",
		"c: --nope: unknown option\n"},
}

func TestArgparse(t *testing.T) {
	for _, tc := range argparseScripts {
		t.Run(tc.name, func(t *testing.T) {
			out, errs, _ := runFish(t, tc.src)
			if out != tc.out {
				t.Errorf("stdout:\n%s\nwant:\n%s", out, tc.out)
			}
			if errs != tc.err {
				t.Errorf("stderr:\n%s\nwant:\n%s", errs, tc.err)
			}
		})
	}
}

func TestArgparseFlagsAreLocal(t *testing.T) {
	out, _, _ := runFish(t, `function f
    argparse v -- $argv
end
f -v
set -q _flag_v; and echo leaked
set -q argv_opts; and echo leaked opts
echo done`)
	if out != "done\n" {
		t.Fatalf("got %q", out)
	}
}

func TestArgparseValidationIsolated(t *testing.T) {
	// The check runs in its own scope: it cannot see the caller's locals,
	// its own locals do not leak, and its stdout becomes an error message.
	out, errs, _ := runFish(t, `function f
    set -l secret 1
    argparse 'n=!echo "[$secret][$_flag_value]"; set -l tmp x' -- $argv; or return
    echo "n=$_flag_n tmp=[$tmp]"
    set -q _flag_value; or echo clean
end
f -n 7
echo after`)
	if out != "n=7 tmp=[]\nclean\nafter\n" || errs != "[][7]\n" {
		t.Fatalf("got %q %q", out, errs)
	}
}

func TestArgparseKeepsBytes(t *testing.T) {
	sh := New()
	sh.Init()
	errb := &lockedBuffer{}
	io := &IO{In: &Stream{R: strings.NewReader("")}, Out: &Stream{W: errb}, Err: &Stream{W: errb}}
	st := bArgparse(sh, io, []string{"argparse", "n=", "v", "--", "-vn\xff\xfe", "x\xff"})
	if st != 0 || strings.Join(sh.GetList("_flag_n"), "|") != "\xff\xfe" || strings.Join(sh.GetList("argv"), "|") != "x\xff" ||
		strings.Join(sh.GetList("argv_opts"), "|") != "-vn\xff\xfe" {
		t.Fatalf("status %d %q: n=%q argv=%q opts=%q", st, errb.String(), sh.GetList("_flag_n"), sh.GetList("argv"), sh.GetList("argv_opts"))
	}
}

func TestArgparseHelp(t *testing.T) {
	out, _, st := runFish(t, "argparse --help")
	if st != 0 || !strings.HasPrefix(out, "argparse - ") {
		t.Fatalf("got %q %d", out, st)
	}
}

// TestArgparseDiffFish compares many argparse calls with fish itself.
// Run it with ASH_DIFF_FISH=1 when fish 4.8 is installed.
func TestArgparseDiffFish(t *testing.T) {
	if os.Getenv("ASH_DIFF_FISH") == "" {
		t.Skip("set ASH_DIFF_FISH=1 to compare with fish")
	}
	if _, err := exec.LookPath("fish"); err != nil {
		t.Skip("fish not installed")
	}
	cases := [][]string{
		{"h/help", "n/name=", "v/verbose", "--", "-n", "bob", "x", "y"},
		{"h/help", "n/name=", "v/verbose", "--", "--name=bob", "-vh", "x", "--help", "--", "-z"},
		{"h/help", "n/name=", "v/verbose", "--", "-nbob", "-v", "--verbose"},
		{"h/help", "n/name=", "v/verbose", "--", "-z"},
		{"h/help", "n/name=", "v/verbose", "--", "--zzz"},
		{"h/help", "n/name=", "v/verbose", "--", "-n"},
		{"h/help", "n/name=", "v/verbose", "--", "--name"},
		{"-n", "foo", "h/help", "--", "x", "-zh"},
		{"-n", "foo", "h/help", "--", "-h", "-zhh"},
		{"-n", "foo", "h/help", "--", "--hel"},
		{"-n", "foo", "h/help", "--", "--he=3"},
		{"-n", "foo", "h/help", "--", "-h=3"},
		{"-n", "foo", "h/help", "--", "--h"},
		{"-n", "foo", "h/help", "--", "-hh"},
		{"-n", "foo", "h/help", "--", "a", "--", "-z"},
		{"-n", "foo", "h/help", "--", "-"},
		{"-n", "foo", "h/help", "--", "--"},
		{"-n", "foo", "h/help", "--", "--", "--"},
		{"-n", "foo", "h/help", "--", "a", "-", "b"},
		{""}, {"?", "--"}, {"h-", "--"}, {"h/", "--"}, {"/", "--"}, {"/x", "--", "-h"}, {"/x", "--", "--x"},
		{"h-help", "--", "-h"}, {"#", "--"}, {"#-", "--", "-5"}, {"#-max", "--", "-5"}, {"h#max", "--", "-h"},
		{"h#", "--", "-h", "3"}, {"h#=", "--"}, {"#max=", "--"}, {"n/name=x", "--"}, {"n/name=?x", "--"},
		{"n/name=+!foo", "--"}, {"n=*", "--", "-n"}, {"n=&", "--", "-n", "x"}, {"n&", "--", "-n"}, {"n=+&", "--"}, {"n&=", "--"},
		{"n!x", "--"}, {"help", "--", "-h"}, {"h/he.lp", "--"}, {"h/he_lp-x", "--", "--he_lp-x"}, {"hh", "--", "-h"},
		{"h=", "--", "-h"}, {"1", "--", "-1"}, {"1/one", "--", "--one"}, {"n/name=!", "--", "-n", "x"},
		{"abc/def", "--"}, {"a-b", "--", "--b"}, {"ab-c", "--", "--ab-c"}, {"_x", "--"}, {"x/", "--"}, {"h/=", "--", "-h", "x"},
		{"x==", "--"}, {"n/name=?!true", "--", "-nx"}, {"#-=", "--"}, {"a_b", "--", "--a_b"}, {"a_", "--"},
		{"h/help", "h/hello", "--"}, {"h/help", "x/help", "--"}, {"/x", "/x", "--"}, {"x/&", "--", "-x"}, {"a#&", "--", "-5"},
		{"a=?+", "--"}, {"a=+?", "--"}, {"a=*?", "--"}, {"a+", "--"}, {"a?", "--"}, {"ab/c", "--"}, {"a/b/c", "--"},
		{"a/b-c", "--", "--b-c"}, {"a-b/c", "--"}, {"/a/b", "--"}, {"h/a b", "--"}, {"a=&&", "--"}, {"a&&", "--"},
		{"a&!", "--"}, {"//", "--"}, {"/-x", "--", "---x"}, {"/-x", "--", "--x"}, {"/--x", "--", "---x"}, {"/-", "--"},
		{"h--x", "--", "---x"}, {"h/-", "--"}, {"h#-", "--", "-5"},
		{"#--max", "--", "-5"}, {"9z", "--", "--9z"}, {"h/-x", "--"}, {"h-/x", "--"}, {"h#/x", "--"}, {"#/x", "--"}, {"#/", "--"},
		{"h-#", "--"}, {"a", "a/b", "--"}, {"b/a", "a", "--"}, {"help", "h/help", "--"}, {"v", "v", "--"}, {"v", "w/v", "--"},
		{"v", "--", "--v"}, {"v", "--", "-v"}, {"n=", "--", "--n", "3"}, {"n=", "--", "-n", "3"},
		{"v&", "--", "--v"}, {"v=?", "--", "--v=3"}, {"v#", "--", "-5"}, {"1", "--", "--1"}, {"v", "vv", "--", "--v"},
		{"v", "vx", "--", "--v"}, {"--"}, {"--", "a", "b"}, {}, {"h"}, {"h", "x"}, {"-n"}, {"-n", "foo"}, {"-n", "foo", "--"},
		{"-N"}, {"-N", "x", "h", "--", "a"}, {"-N", "-1", "h", "--", "a"}, {"-N", "2", "h", "--", "a"}, {"-N", "2", "h", "--", "a", "b"},
		{"-X", "1", "h", "--", "a", "b"}, {"-X", "0", "h", "--", "a"}, {"--min-args=3", "--", "a", "b"}, {"--max-args=1", "--", "a", "b"},
		{"-X", "x", "--", "a"}, {"-N", "1x", "--", "a"}, {"-N", "", "--", "a"}, {"-N", " 2", "--", "a"}, {"-N", "+2", "--", "a", "b"},
		{"-N", "0x10", "--", "a"}, {"-N", "99999999999999999999", "--", "a"}, {"-N", "2 ", "--", "a"}, {"-X", "9223372036854775808", "--"},
		{"-x", "h,v", "h", "v", "--", "-h", "-v"}, {"-x", "h,v", "h/help", "v/verbose", "--", "--help", "-v"},
		{"-x", "help,verbose", "help", "verbose", "--", "--help", "--verbose"}, {"-x", "h", "h", "--", "-h"},
		{"-x", "h,z", "h", "--", "-h"}, {"-x", "h,", "h", "--", "-h"}, {"-x", ",h", "h", "--", "-h"}, {"-x", "h,h", "h", "--", "-h"},
		{"-x", "a,b", "-x", "b,c", "a", "b", "c", "--", "-c", "-b"}, {"-x", "a,b", "-x", "b,c", "a", "b", "c", "--", "-a", "-c"},
		{"-x", "a,b", "a", "b", "--", "x"}, {"--exclusive=a,b", "a", "b", "--", "-a", "-b"}, {"-xa,b", "a", "b", "--", "-a", "-b"},
		{"-x", "h-help,v", "h-help", "v", "--", "--help", "-v"}, {"-x", "help,v", "h-help", "v", "--", "--help", "-v"},
		{"-x", "n,v", "n=+", "v", "--", "-n", "1", "-v"}, {"-x", "max,v", "#-max", "v", "--", "-5", "-v"},
		{"-x", "#,v", "#-max", "v", "--", "-5", "-v"}, {"-N", "3", "-x", "a,b", "a", "b", "--", "-a", "-b"},
		{"n/name=!chk", "--", "-n", "ok"}, {"n/name=!chk", "--", "--name", "ok"}, {"n/name=!chk", "--", "--name=bad"},
		{"n/name=!chk", "--", "-nbad"}, {"-n", "myname", "n/name=!chk", "--", "-nbad"}, {"name=!chk", "--", "--name", "bad"},
		{"n/name=+!chk", "--", "-n", "ok", "-n", "bad"}, {"n/name=?!chk", "--", "-n"}, {"n/name=?!chk", "--", "-nok"},
		{"n/name=!chk2", "--", "-nbad"}, {"m#max!chk", "--", "-5"}, {"m#max!chk", "--", "-m", "5"}, {"m#max!chk", "--", "--max", "5"},
		{"n/name=!set -l q 1; echo $argv; false", "--", "-n", "x"}, {"n/name=!true", "--", "-n", "x", "-z"}, {"n/name=!false", "--", "-z", "-n", "x"},
		{"n/name=?", "--", "-n", "--name"}, {"n/name=?", "--", "-nfoo", "--name"}, {"n/name=?", "--", "-nfoo", "-n", "bar"},
		{"n/name=*", "--", "-nfoo", "-n", "bar", "--name", "--name=x"}, {"n/name=*", "--", "-n"},
		{"n/name=+", "--", "-n", "a", "--name", "b", "-nc", "--name=d"}, {"n/name=", "--", "-n", "a", "--name", "b"},
		{"n/name=", "--", "-n", "-v"}, {"n/name=", "--", "-n", "--"}, {"n/name=", "--", "-n", ""}, {"n/name=", "--", "--name="},
		{"n/name=?", "--", "--name="}, {"n/name=?", "--", "-n="}, {"n/name=", "--", "-n=x"},
		{"#-max", "--", "-5", "-7"}, {"#-max", "--", "--5"}, {"#-max", "--", "--max", "5"}, {"#-max", "--", "--max=5"},
		{"#-max", "--", "+5"}, {"#-max", "--", "-5x"}, {"#-max", "--", "-x5"}, {"#-max", "v", "--", "-v5"}, {"#-max", "v", "--", "-5v"},
		{"#-max", "--", "-0"}, {"#-max", "--", "-99999999999999999999"}, {"#-max", "--", "- 5"}, {"#-max", "--", "-+5"},
		{"#-max", "--", "---5"}, {"#-max", "--", "-5.5"}, {"#max", "--", "-5"}, {"#max", "--", "--max", "6"}, {"m#max", "--", "-5"},
		{"m#max", "--", "-m", "5"}, {"m#max", "--", "-m5"}, {"m#max", "--", "-m", "x"}, {"m#max", "--", "--max=7"}, {"m#", "--", "-5"},
		{"1#", "--", "-5"}, {"#-max", "#-min", "--", "-5"}, {"#-max", "m#min", "--", "-5"}, {"m#max", "#-x", "--"}, {"-i", "#-max", "--", "-5", "-z"},
		{"#-max", "5", "--", "-5"}, {"#-max", "n=", "--", "-n", "-5"}, {"#-max", "--", "--", "-5"}, {"-s", "m#max", "--", "-5", "a", "-6"},
		{"m#max", "--", "--max=x"}, {"m#max", "--", "--ma=x"}, {"m#max", "--", "-m", ""}, {"m#max", "--", "-m", "-5"},
		{"m#max", "--", "-m", "0x10"}, {"m#max", "--", "-m", " 5"}, {"m#max", "--", "-m", "+5"}, {"m#max", "--", "-m", "99999999999999999999"},
		{"m#max!true", "--", "-m", "x"}, {"m#max!false", "--", "-m", "5"}, {"-n", "foo", "m#max", "--", "-m", "x"}, {"#-max", "--", "-5 "},
		{"h/help&", "v", "--", "-h", "-v", "x"}, {"h/help&", "v", "--", "-hv", "x"}, {"h/help&", "v", "--", "-vh", "x"},
		{"n/name=&", "v", "--", "-n", "foo", "-v", "x"}, {"n/name=&", "v", "--", "-vnfoo", "x"}, {"n/name=&", "v", "--", "-vn", "foo", "x"},
		{"n/name=+&", "v", "--", "-n", "foo", "-v", "x", "--name=bar"}, {"n/name=?&", "v", "--", "-nfoo", "-v", "x", "--name"},
		{"n/name=&!true", "--", "-n", "foo"}, {"n/name=!true&", "--", "-n", "foo"}, {"m#max&", "--", "-5", "-m", "3"},
		{"#-max&", "--", "-5"}, {"h-help", "--", "--help"}, {"h-help", "--", "--he"}, {"h-help=", "--", "--help=x"}, {"h-help&", "--", "--help"},
		{"help", "hello", "--", "--he"}, {"help", "hello", "--", "--help"}, {"help", "hello", "--", "--hell"}, {"help", "hello", "--", "-he"},
		{"help", "hello", "--", "-h"}, {"help", "hello", "--", "-hello"}, {"help", "hello", "--", "-hello=3"}, {"help", "hello=", "--", "-hello=3"},
		{"help", "hello=", "--", "-hello", "3"}, {"help", "hello=", "--", "-hel", "3"}, {"help", "hello=", "--", "-hell3"}, {"h", "help", "--", "-help"},
		{"h", "elp", "--", "-help"}, {"e", "help", "--", "-help"}, {"x/help", "--", "-help"}, {"x/help", "--", "-he"}, {"x-help", "--", "-help"},
		{"v", "verbose", "--", "-ver"}, {"verbose", "v", "--", "-ver"}, {"version", "verbose", "--", "--v"}, {"version", "verbose", "--", "--vers"},
		{"version", "verbose", "--", "--verb"}, {"-n", "foo", "version", "verbose", "--", "--vers=1"}, {"version=", "verbose", "--", "--vers"},
		{"version=", "verbose", "--", "--vers", "1"}, {"v", "n=", "--", "-vn"}, {"v", "n=", "--", "x", "-vn"},
		{"-S", "help", "--", "-help"}, {"-S", "help", "--", "--hel"}, {"-S", "help", "--", "--help"}, {"-S", "h/help", "--", "-h"},
		{"-S", "h/help", "--", "-help"}, {"--strict-longopts", "help", "--", "-h"}, {"-S", "v", "--", "--v"}, {"-S", "n/name=", "--", "--nam=x"},
		{"-i", "h", "--", "-z", "-h", "x"}, {"-i", "h", "--", "-zh", "x"}, {"-i", "h", "--", "-hz", "x"}, {"-i", "h", "--", "-z", "x", "--", "-y"},
		{"-i", "h", "--", "--zz", "x", "--yy=3", "-h"}, {"-i", "h", "--", "--zz=3"}, {"-i", "h", "--", "-h", "--", "-z"},
		{"-i", "h", "n=", "--", "-z", "-n", "3"}, {"-i", "h", "n=", "--", "-zn", "3"}, {"-u", "h", "--", "-z", "-h", "x"},
		{"-u", "h", "--", "-zh", "x"}, {"-u", "h", "--", "-hz", "x"}, {"-u", "h", "--", "-z", "x", "--", "-y"},
		{"-u", "h", "--", "--zz", "x", "--yy=3", "-h"}, {"-u", "h", "--", "--zz=3"}, {"-iu", "h", "--", "-z"}, {"-ui", "h", "--"},
		{"--unknown-arguments=optional", "a", "b", "--", "-u", "-a", "-ub"}, {"--unknown-arguments=required", "a", "b", "--", "-u", "-a", "-ub"},
		{"--unknown-arguments=none", "a", "b", "--", "-u", "-a", "-ub"}, {"-i", "--unknown-arguments=optional", "a", "b", "--", "-u", "-a", "-ub"},
		{"-i", "--unknown-arguments=required", "a", "b", "--", "-u", "-a", "-ub"}, {"-i", "--unknown-arguments=none", "a", "b", "--", "-u", "-a", "-ub"},
		{"--unknown-arguments=required", "a", "b", "--", "-u"}, {"--unknown-arguments=required", "a", "b", "--", "--uu", "x"},
		{"--unknown-arguments=none", "a", "b", "--", "-bar"}, {"--unknown-arguments=none", "a", "b", "bar", "--", "-bar"},
		{"--unknown-arguments=optional", "a", "b", "--", "-abrx"}, {"-u", "a", "b", "--", "-bux"}, {"-u", "n=", "a", "b", "--", "-bun"},
		{"-u", "n=", "a", "b", "--", "-bnu"}, {"-i", "n=", "a", "b", "--", "-bnu"}, {"-i", "n=", "a", "b", "--", "-bun"},
		{"-i", "n=", "a", "b", "--", "-bu", "-a", "x"}, {"-i", "n=", "a", "b", "--", "-bua", "x"}, {"-i", "--unknown-arguments=none", "n=", "a", "b", "--", "-bua", "x"},
		{"-i", "--unknown-arguments=none", "n=", "a", "b", "--", "-bun", "x"}, {"-i", "--unknown-arguments=none", "n=", "a", "b", "--", "-buna", "x"},
		{"-i", "--unknown-arguments=required", "n=", "a", "b", "--", "-bu", "x"}, {"-u", "--unknown-arguments=required", "n=", "a", "b", "--", "-bu", "x"},
		{"-u", "--unknown-arguments=none", "n=", "a", "b", "--", "-bua", "x"}, {"-u", "--unknown-arguments=required", "a", "b", "--", "-bu"},
		{"-u", "--unknown-arguments=required", "a", "b", "--", "-bub", "x"}, {"-u", "--unknown-arguments=none", "a", "b", "--", "-bub", "x"},
		{"--unknown-arguments=none", "a", "b", "--", "--zz=3"}, {"--unknown-arguments=none", "-i", "a", "b", "--", "--zz=3"},
		{"--unknown-arguments=bogus", "a", "--"}, {"--unknown-arguments", "ab", "--", "-u"}, {"--unknown-arguments", "none", "ab", "--", "-u"},
		{"-S", "-i", "help", "--", "-help", "-hel", "--hel"}, {"-S", "-u", "help", "--", "-help", "-hel", "--hel"}, {"--strict-longopts", "-u", "h/help", "--", "-help"},
		{"-s", "h", "--", "a", "--", "-h"}, {"-s", "h", "--", "-h", "a", "-h"}, {"-s", "h", "--", "-h", "--", "a", "-h"}, {"-s", "h", "--", "-", "-h"},
		{"-s", "-i", "h", "--", "-z", "-h", "x", "-h"}, {"-s", "-u", "h", "--", "-z", "-h", "x", "-h"}, {"-s", "n=", "--", "-n", "a", "b", "-n", "c"},
		{"-u", "h&", "--", "-hz"}, {"-u", "h&", "--", "-zh"}, {"-u", "h&", "v", "--", "-vhz"}, {"-i", "h&", "v", "--", "-vhz"}, {"-i", "h&", "v", "--", "-zvh"},
		{"-i", "v", "--", "-vz5"}, {"-i", "n=?", "--", "-nz5"}, {"-i", "n=", "--", "-z", "-n"},
		{"h/he_lp-x", "--", "--he_lp-x"}, {"h/dry-run", "--", "--dry-run"}, {"h/x-y-", "--", "--x-y-"}, {"h/-xy", "--", "--xy"}, {"h/-xy", "--", "---xy"},
		{"ab-c=", "--", "--ab-c=1"}, {"5/five", "--", "-5"}, {"#-max", "h", "--", "-h5"}, {"dry-run", "--", "-dry-run"},
		{"-sz", "h", "--", "x"}, {"--stop", "h", "--", "a", "-h"}, {"--min", "1", "h", "--", "a"}, {"--m", "1", "h", "--", "a"}, {"--name=x", "h", "--", "-z"},
		{"-nfoo", "h", "--", "-z"}, {"-sn"}, {"--name"}, {"--nam=q", "h", "--", "-z"}, {"--ignore", "h", "--", "-z"}, {"--str", "help", "--", "-help"},
		{"--unknown-arguments"}, {"--unknown-arguments="}, {"-z", "-h"}, {"--help=3"}, {"-move-unknown", "h", "--", "-z"}, {"-max-args", "0", "--", "a"},
		{"-unknown-arguments", "none", "h", "--", "-z"}, {"-n", "", "h", "--", "-z"}, {"-n", "a", "-n", "b", "h", "--", "-z"},
		{"n/num=!_validate_int --min 0 --max 99", "--", "--num", "42"}, {"n/num=!_validate_int --min 0 --max 99", "--", "--num", "100"},
		{"n/num=!_validate_int --min 0 --max 99", "--", "-n", "-1"}, {"n/num=!_validate_int --min 0 --max 99", "--", "-nx"},
		{"n/num=!_validate_int", "--", "-n", "007"}, {"n/num=!_validate_int --max=5", "--", "-n6"}, {"n=*!chk", "--", "-n", "-nok"},
		{"n=*!chk", "--", "-nbad"}, {"-x", "a,b", "-x", "a,c", "a", "b", "c", "--", "-a"}, {"-X", "1", "-N", "1", "--"},
		{"h/help", "--", "-hhelp"}, {"h/help=?", "--", "-h", "--help=", "-h"}, {"q", "--", "-qqq", "--q"},
	}
	for _, c := range cases {
		want, got := fishArgparse(t, c), ashArgparse(t, c)
		if want != got {
			t.Errorf("argparse %q\n--- fish\n%s\n--- ash\n%s", c, want, got)
		}
	}
}

const argparseDiffPrelude = `
function chk; echo "chk cmd=$_argparse_cmd name=$_flag_name value=$_flag_value"; test "$_flag_value" = ok; end
function chk2; echo out1; echo out2; return 3; end
`

// fishArgparse runs argparse at fish's top level and reports the status,
// the first error lines and every _flag_*, argv and argv_opts variable.
func fishArgparse(t *testing.T, args []string) string {
	dir := t.TempDir()
	argFile := filepath.Join(dir, "args")
	errFile := filepath.Join(dir, "err")
	data := ""
	for _, a := range args {
		data += a + "\x00"
	}
	if err := os.WriteFile(argFile, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	script := argparseDiffPrelude + `
set -l __args (string split0 < ` + argFile + `)
argparse $__args 2>` + errFile + `
set -l __st $status
echo "status $__st"
for __n in (set -n | sort -u)
    if string match -qr '^(_flag_.*|argv|argv_opts)$' -- $__n
        set -l __line "$__n" (count $$__n)
        for __v in $$__n
            set -a __line "[$__v]"
        end
        echo "V $__line"
    end
end
`
	cmd := exec.Command("fish", "--no-config", "-c", script)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Run()
	e, _ := os.ReadFile(errFile)
	return argparseNorm(out.String(), string(e)+errb.String())
}

func ashArgparse(t *testing.T, args []string) string {
	sh := New()
	sh.Init()
	sh.SetArgs(nil)
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	io := &IO{In: &Stream{R: strings.NewReader("")}, Out: &Stream{W: out}, Err: &Stream{W: errb}}
	sh.RunString(argparseDiffPrelude, "prelude.fish", io)
	st := bArgparse(sh, io, append([]string{"argparse"}, args...))
	var b strings.Builder
	b.WriteString(out.String())
	b.WriteString("status " + strconv.Itoa(st) + "\n")
	for _, n := range sh.VarNames() {
		if strings.HasPrefix(n, "_flag_") || n == "argv" || n == "argv_opts" {
			vals := sh.GetList(n)
			line := []string{n, strconv.Itoa(len(vals))}
			for _, v := range vals {
				line = append(line, "["+v+"]")
			}
			b.WriteString("V " + strings.Join(line, " ") + "\n")
		}
	}
	return argparseNorm(b.String(), errb.String())
}

// argparseNorm sorts output lines and drops fish's error trailer (the
// backtrace after the first blank line).
func argparseNorm(out, errs string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	sort.Strings(lines)
	var el []string
	for _, l := range strings.Split(errs, "\n") {
		if l == "" {
			break
		}
		el = append(el, "E: "+l)
	}
	sort.Strings(el)
	return strings.Join(append(lines, el...), "\n")
}
