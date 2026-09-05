package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/andrinoff/ash/internal/syntax"
)

// hlShell returns a shell whose PATH, HOME, config and working directory
// are temporary and known:
//
//	bin/        cat env git grep ls make nice nohup sudo vim; notexec (not executable)
//	home/       .config/ proj/
//	functions/  hello.fish (autoloadable; sets $hl_loaded when loaded)
//	work/       README.md main.go notes.txt "my file.txt" run.sh (executable)
//	            src/a.txt src/lib/ link -> src
//
// It also defines the alias ll, the function greet, the abbreviations
// gco, co (only for git) and dotdot (a regex abbreviation whose function
// would set $hl_ran).
func hlShell(t testing.TB) *Shell {
	t.Helper()
	root := t.TempDir()
	mk := func(p string, mode os.FileMode, body string) {
		t.Helper()
		dir := strings.HasSuffix(p, "/")
		p = filepath.Join(root, p)
		if dir {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"cat", "env", "git", "grep", "ls", "make", "nice", "nohup", "sudo", "vim"} {
		mk("bin/"+n, 0o755, "#!/bin/sh\n")
	}
	mk("bin/notexec", 0o644, "")
	mk("home/.config/", 0, "")
	mk("home/proj/", 0, "")
	mk("functions/hello.fish", 0o644, "set -g hl_loaded 1\nfunction hello; echo hello; end\n")
	for _, f := range []string{"README.md", "main.go", "notes.txt", "my file.txt", "src/a.txt"} {
		mk("work/"+f, 0o644, "")
	}
	mk("work/run.sh", 0o755, "#!/bin/sh\n")
	mk("work/src/lib/", 0, "")
	if err := os.Symlink("src", filepath.Join(root, "work/link")); err != nil {
		t.Fatal(err)
	}
	sh := New()
	set := func(name string, vals ...string) {
		t.Helper()
		if err := sh.SetVar(name, vals, setOpts{scope: setGlobal, export: 1}); err != nil {
			t.Fatal(err)
		}
	}
	set("PATH", filepath.Join(root, "bin"))
	set("HOME", filepath.Join(root, "home"))
	set("XDG_CONFIG_HOME", filepath.Join(root, "home/.config"))
	set("XDG_DATA_HOME", filepath.Join(root, "home/.local/share"))
	set("XDG_CACHE_HOME", filepath.Join(root, "home/.cache"))
	set("ash_function_path", filepath.Join(root, "functions"))
	sh.UnsetVar("CDPATH", setGlobal)
	sh.UnsetVar("EDITOR", setGlobal)
	sh.dir = filepath.Join(root, "work")
	errs := &lockedBuffer{}
	io := &IO{In: &Stream{}, Out: &Stream{}, Err: &Stream{W: errs}}
	setup := `alias ll='ls -l'
function greet; echo hi; end
function hlmark; set -g hl_ran 1; echo .; end
abbr -a gco git checkout
abbr -a --command git co checkout
abbr -a --regex '\.\.+' --function hlmark dotdot`
	if st := sh.RunString(setup, "", io); st != 0 || errs.String() != "" {
		t.Fatalf("setup: status %d: %s", st, errs.String())
	}
	return sh
}

// roleChars renders roles compactly, one character per rune.
var roleChars = [hlNumRoles]byte{
	hlNormal: '.', hlCommand: 'c', hlError: '!', hlKeyword: 'k', hlParam: 'p',
	hlOption: 'o', hlQuote: 'q', hlEscape: 'e', hlVariable: 'v', hlOperator: 'O',
	hlEnd: ';', hlRedirection: 'r', hlComment: '#',
}

// hlRoles highlights src and returns its roles, one character per rune,
// and a mask with '_' under the runes marked as a valid path (trailing
// spaces trimmed). It doesn't recover from panics, unlike Highlight.
func hlRoles(sh *Shell, src string, fish bool) (roles, paths string) {
	h := sh.newHighlighter(src)
	h.fish = fish
	h.list(0, 0)
	var r, p strings.Builder
	for i := 0; i < len(src); {
		c := h.col[i]
		if base := c & hlRoleMask; base < hlNumRoles {
			r.WriteByte(roleChars[base])
		} else {
			r.WriteByte('?') // left unclassified
		}
		if c&hlValidPath != 0 {
			p.WriteByte('_')
		} else {
			p.WriteByte(' ')
		}
		_, n := utf8.DecodeRuneInString(src[i:])
		i += n
	}
	h.release()
	return r.String(), strings.TrimRight(p.String(), " ")
}

type hlCase struct {
	src   string
	roles string
	paths string // '_' under runes underlined as valid paths; "" for none
}

func checkRoles(t *testing.T, sh *Shell, fish bool, cases []hlCase) {
	t.Helper()
	for _, tc := range cases {
		roles, paths := hlRoles(sh, tc.src, fish)
		if roles != tc.roles || paths != tc.paths {
			t.Errorf("%q:\n got %q paths %q\nwant %q paths %q", tc.src, roles, paths, tc.roles, tc.paths)
		}
	}
}

// Roles: . normal, c command, ! error, k keyword, p param, o option,
// q quote, e escape, v variable, O operator, ; end, r redirection,
// # comment.
//
// Where the syntax overlaps, expectations were checked against fish 4.8.1
// (fish_indent --html), which agrees except where ash deliberately
// differs: and/or/not are keywords and && || are "end" (fish: operator),
// a lone "-" is a parameter (fish: option), $var has its own "variable"
// role (fish: operator), commands are checked for existence (fish_indent
// can't), ? is a glob, and <(...), &! and heredocs exist.

func TestHighlightSh(t *testing.T) {
	checkRoles(t, hlShell(t), false, []hlCase{
		{src: `echo hello world`,
			roles: `cccc.ppppp.ppppp`},
		{src: `ls -la --color=auto - --`,
			roles: `cc.ooo.oooooooooooo.p.oo`},
		{src: `ls -- -a --`,
			roles: `cc.oo.pp.pp`},
		{src: `echo "hi $USER" 'x' $'a\tb'`,
			roles: `cccc.qqqqvvvvvq.qqq.qqqeeqq`},
		{src: `echo a\ b \n \x41 \$x`,
			roles: `cccc.peep.ee.eeee.eep`},
		{src: `echo "a\"b\$c\\d\e"`,
			roles: `cccc.qqeeqeeqeeqqqq`},
		{src: `echo $x ${x} $1 $? $$ $#`,
			roles: `cccc.vv.vvvv.vv.vv.vv.vv`},
		{src: `echo ${x:-def} ${#x} ${x/a/b} ${x:1:2}`,
			roles: `cccc.vvvOOpppv.vvOvv.vvvOpOpv.vvvOpOpv`},
		{src: `echo $x[1] $x[2..-1]`,
			roles: `cccc.vvOpO.vvOpppppO`},
		{src: `echo (ls -la) $(ls) "a$(ls)b"`,
			roles: `cccc.Occ.oooO.OOccO.qqOOccOqq`},
		{src: "echo `ls -la`",
			roles: `cccc.Occ.oooO`},
		{src: `echo $(( x + 1 )) $((y*2))`,
			roles: `cccc.OOO.v.p.p.OO.OOOvppOO`},
		{src: `echo {a,b}c {1..3} {x} a{b,c}`,
			roles: `cccc.OpOpOp.OpOOpO.ppp.pOpOpO`},
		{src: `echo *.go ?x **/y [ab]c`,
			roles: `cccc.Oppp.Op.OOpp.OppOp`},
		{src: `echo ~/x ~ a~b`,
			roles: `cccc.Opp.O.ppp`,
			paths: `         _`},
		{src: `echo a#b #comment`,
			roles: `cccc.ppp.########`},
		{src: `# just a comment`,
			roles: `################`},
		{src: `FOO=bar BAR+=x arr=(a b) ls`,
			roles: `vvvOppp.vvvOOp.vvvOOp.pO.cc`},
		{src: `FOO=1`,
			roles: `vvvOp`},
		{src: `x[1]=a ls`,
			roles: `vOpOOp.cc`},
		{src: `ls > out.txt 2>&1 >> log < README.md`,
			roles: `cc.r.rrrrrrr.rrrr.rr.rrr.r.rrrrrrrrr`},
		{src: `ls &> all &>> all2 >| x >? y`,
			roles: `cc.rr.rrr.rrr.rrrr.rr.r.rr.r`},
		{src: `ls 2>/nonexistent/x < nope <> rw`,
			roles: `cc.rr!!!!!!!!!!!!!!.r.!!!!.rr.rr`},
		{src: `echo hi >&2 1<&0 >&- >&x 2>&y`,
			roles: `cccc.pp.rrr.rrrr.rrr.rrr.rrr!`},
		{src: `ls > src`,
			roles: `cc.r.!!!`},
		{src: `ls > $(echo f)`,
			roles: `cc.r.OOcccc.pO`},
		{src: "cat <<EOF\nhi $x\nEOF\nls",
			roles: `ccc.rrrrr.qqqvv.rrr.cc`},
		{src: "cat <<'EOF'\n$x\nEOF",
			roles: `ccc.rrrrrrr.qq.rrr`},
		{src: "cat <<-EOF\n\thi\n\tEOF",
			roles: `ccc.rrrrrr.qqq..rrr`},
		{src: `cat <<< "$x" <<<word`,
			roles: `ccc.rrr.rrrr.rrrrrrr`},
		{src: `cat <(ls) >(cat)`,
			roles: `ccc.OOccO.OOcccO`},
		{src: `ls | grep x && echo a || echo b`,
			roles: `cc.;.cccc.p.;;.cccc.p.;;.cccc.p`},
		{src: `ls & ls &! ls |& cat &| cat`,
			roles: `cc.;.cc.;;.cc.;;.ccc.;;.ccc`},
		{src: `if true; then echo; elif false; then :; else echo; fi`,
			roles: `kk.cccc;.kkkk.cccc;.kkkk.ccccc;.kkkk.c;.kkkk.cccc;.kk`},
		{src: `while true; do break; done`,
			roles: `kkkkk.cccc;.kk.kkkkk;.kkkk`},
		{src: `until false; do continue 2; done`,
			roles: `kkkkk.ccccc;.kk.kkkkkkkk.p;.kkkk`},
		{src: `for x in a b; do echo $x; done`,
			roles: `kkk.p.kk.p.p;.kk.cccc.vv;.kkkk`},
		{src: `for ((i=0; i<3; i++)); do echo; done`,
			roles: `kkk.OOvppO.vppO.vppOO;.kk.cccc;.kkkk`},
		{src: `case $x in a|b) echo;; (c) ;; *) ;; esac`,
			roles: `kkkk.vv.kk.pOpO.cccc;;.OpO.;;.OO.;;.kkkk`},
		{src: "case $x in\n  a) ls ;;\nesac",
			roles: `kkkk.vv.kk...pO.cc.;;.kkkk`},
		{src: `f() { echo; }`,
			roles: `cOO.k.cccc;.k`},
		{src: `function f { echo; }`,
			roles: `kkkkkkkk.p.k.cccc;.k`},
		{src: `function f() { echo; }`,
			roles: `kkkkkkkk.pOO.k.cccc;.k`},
		{src: `{ echo; } > out`,
			roles: `k.cccc;.k.r.rrr`},
		{src: `(cd src; ls) | cat`,
			roles: `Occ.ppp;.ccO.;.ccc`,
			paths: `    ___`},
		{src: `[[ -f README.md && $x == y ]]`,
			roles: `kk.oo.ppppppppp.OO.vv.OO.p.kk`,
			paths: `      _________`},
		{src: `(( x = 1 + y ))`,
			roles: `OO.v.p.p.p.v.OO`},
		{src: `! true && ! false`,
			roles: `k.cccc.;;.k.ccccc`},
		{src: `time -p ls`,
			roles: `kkkk.oo.cc`},
		{src: `return 1`,
			roles: `kkkkkk.p`},
		{src: "if true\n  echo a\nelse\n  echo b\nend",
			roles: `kk.cccc...cccc.p.kkkk...cccc.p.kkk`},
		{src: "echo a \\\n  b",
			roles: `cccc.p.e...p`},
		{src: "echo foo\\\nbar",
			roles: `cccc.pppe.ppp`},
		{src: `echo 'it\'s'`,
			roles: `cccc.qqqqqp!`},
		{src: `echo 'a\\b'`,
			roles: `cccc.qqqqqq`},
		{src: "echo `ls`",
			roles: `cccc.OccO`},
		{src: `echo a 2>| cat`,
			roles: `cccc.p.rrr.rrr`},
		{src: `{ echo a }`,
			roles: `k.cccc.p.p`},
	})
}

// Fish syntax works in the interactive (sh-quoting) mode too.
func TestHighlightFishSyntax(t *testing.T) {
	checkRoles(t, hlShell(t), false, []hlCase{
		{src: `if test -f x; echo yes; else; echo no; end`,
			roles: `kk.cccc.oo.p;.cccc.ppp;.kkkk;.cccc.pp;.kkk`},
		{src: `for i in 1 2 3; echo $i; end`,
			roles: `kkk.p.kk.p.p.p;.cccc.vv;.kkk`},
		{src: `begin; echo a; end`,
			roles: `kkkkk;.cccc.p;.kkk`},
		{src: `switch $x; case a b; echo; case '*'; echo; end`,
			roles: `kkkkkk.vv;.kkkk.p.p;.cccc;.kkkk.qqq;.cccc;.kkk`},
		{src: `function foo -d "desc"; echo; end`,
			roles: `kkkkkkkk.ppp.oo.qqqqqq;.cccc;.kkk`},
		{src: "function foo\n  echo\nend",
			roles: `kkkkkkkk.ppp...cccc.kkk`},
		{src: "function foo\n{ echo; }",
			roles: `kkkkkkkk.ppp.k.cccc;.k`},
		{src: `if true; echo; else if false; echo; end`,
			roles: `kk.cccc;.cccc;.kkkk.kk.ccccc;.cccc;.kkk`},
		{src: `set -l x (math 1 + 2); and echo $x[1]; or not true`,
			roles: `ccc.oo.p.Occcc.p.p.pO;.kkk.cccc.vvOpO;.kk.kkk.cccc`},
		{src: `echo (echo (echo a))`,
			roles: `cccc.Occcc.Occcc.pOO`},
		{src: `echo (ls)[1] $status $argv[2..]`,
			roles: `cccc.OccOOpO.vvvvvvv.vvvvvOpppO`},
		{src: `not not true; ! ! true`,
			roles: `kkk.kkk.cccc;.k.k.cccc`},
		{src: `echo {a,b} ~ **.go`,
			roles: `cccc.OpOpO.O.OOppp`,
			paths: `           _`},
	})
}

// Fish quoting rules, as the parser uses for .fish files.
func TestHighlightFishQuoting(t *testing.T) {
	checkRoles(t, hlShell(t), true, []hlCase{
		{src: `echo 'it\'s'`,
			roles: `cccc.qqqeeqq`},
		{src: `echo 'a\\b'`,
			roles: `cccc.qqeeqq`},
		{src: "echo `ls`",
			roles: `cccc.pppp`},
		{src: `echo a 2>| cat >| cat`,
			roles: `cccc.p.;;;.ccc.;;.ccc`},
		{src: `{ echo a }`,
			roles: `k.cccc.p.k`},
	})
}

func TestHighlightErrors(t *testing.T) {
	checkRoles(t, hlShell(t), false, []hlCase{
		{src: `fi`, roles: `!!`},
		{src: `done`, roles: `!!!!`},
		{src: `end`, roles: `!!!`},
		{src: `esac`, roles: `!!!!`},
		{src: `then`, roles: `!!!!`},
		{src: `else`, roles: `!!!!`},
		{src: `elif`, roles: `!!!!`},
		{src: `do`, roles: `!!`},
		{src: `}`, roles: `!`},
		{src: `in`, roles: `!!`},
		{src: `]]`, roles: `!!`},
		{src: `)`, roles: `!`},
		{src: `if true; then echo; end`,
			roles: `kk.cccc;.kkkk.cccc;.!!!`},
		{src: `if true; echo; fi`,
			roles: `kk.cccc;.cccc;.!!`},
		{src: `if true; echo; elif true; then :; fi`,
			roles: `kk.cccc;.cccc;.!!!!.cccc;.kkkk.c;.kk`},
		{src: `while true; echo; done`,
			roles: `kkkkk.cccc;.cccc;.!!!!`},
		{src: `begin; done`,
			roles: `kkkkk;.!!!!`},
		{src: `echo $(fi)`,
			roles: `cccc.OO!!O`},
		{src: `if true; then echo $(fi); fi`,
			roles: `kk.cccc;.kkkk.cccc.OO!!O;.kk`},
		{src: `echo a ) b`,
			roles: `cccc.p.!.!`},
		{src: `echo a;; echo b`,
			roles: `cccc.p!!.cccc.p`},
		{src: `true && and false`,
			roles: `cccc.;;.!!!.ccccc`},
		{src: `echo a | not grep x`,
			roles: `cccc.p.;.!!!.cccc.p`},
		{src: `> out if`,
			roles: `r.rrr.!!`},
		{src: `if true; then :; fi foo`,
			roles: `kk.cccc;.kkkk.c;.kk.!!!`},
		{src: `begin; end foo`,
			roles: `kkkkk;.kkk.!!!`},
		{src: `| cat`, roles: `!.ccc`},
		{src: `&& ls`, roles: `!!.cc`},
		{src: `& ls`, roles: `!.cc`},
		{src: `ls | | cat`, roles: `cc.;.!.ccc`},
		{src: `ls & & ls`, roles: `cc.;.!.cc`},
		{src: `ls && ;`, roles: `cc.;;.!`},
		{src: `if ; then`, roles: `kk.!.kkkk`},
		{src: `! ;`, roles: `k.!`},
		{src: `ls > ; echo`, roles: `cc.!.;.cccc`},
		{src: `echo ${x!y}`, roles: `cccc.vvv!!v`},
		{src: `nosuch arg`, roles: `!!!!!!.ppp`},
		{src: `'if' true`, roles: `!!!!.pppp`},
	})
}

func TestHighlightCommands(t *testing.T) {
	sh := hlShell(t)
	checkRoles(t, sh, false, []hlCase{
		{src: `ll -a`, roles: `cc.oo`},             // alias
		{src: `greet`, roles: `ccccc`},             // function
		{src: `hello`, roles: `ccccc`},             // autoloadable function
		{src: `gco dev`, roles: `ccc.ppp`},         // abbreviation
		{src: `co`, roles: `!!`},                   // abbreviation for git's arguments only
		{src: `....`, roles: `cccc`},               // regex abbreviation
		{src: `nosuch`, roles: `!!!!!!`},           // unknown
		{src: `notexec`, roles: `!!!!!!!`},         // in PATH but not executable
		{src: `./run.sh`, roles: `cccccccc`},       // executable path
		{src: `./README.md`, roles: `!!!!!!!!!!!`}, // not executable
		{src: `../work/`, roles: `cccccccc`},       // implicit cd
		{src: `src/`, roles: `cccc`},               // implicit cd
		{src: `src`, roles: `!!!`},                 // needs a slash
		{src: `..`, roles: `cc`},                   // implicit cd
		{src: `~/`, roles: `Oc`},                   // implicit cd
		{src: `'ls' l\s`, roles: `qqqq.pee`},       // quoting doesn't matter
		{src: `$(echo ls) -la`, roles: `OOcccc.ppO.ooo`},
		{src: `$NOSUCHVAR file`, roles: `vvvvvvvvvv.pppp`}, // unknown: may expand to nothing
		{src: `[ -f x ]`, roles: `c.oo.p.p`},
		{src: `command ls`, roles: `ccccccc.cc`},
		{src: `command cd`, roles: `ccccccc.!!`}, // command skips builtins
		{src: `command -v cd`, roles: `ccccccc.oo.cc`},
		{src: `builtin echo`, roles: `ccccccc.cccc`},
		{src: `builtin ls`, roles: `ccccccc.!!`},
		{src: `exec ls`, roles: `cccc.cc`},
		{src: `sudo -u root ls -la`, roles: `cccc.oo.pppp.cc.ooo`},
		{src: `sudo --user=root ls`, roles: `cccc.ooooooooooo.cc`},
		{src: `sudo -E FOO=1 make`, roles: `cccc.oo.vvvOp.cccc`},
		{src: `sudo -e README.md`, roles: `cccc.oo.ppppppppp`, paths: `        _________`},
		{src: `sudo nosuch`, roles: `cccc.!!!!!!`},
		{src: `sudo cd`, roles: `cccc.!!`},
		{src: `env -i FOO=1 BAR=2 ls`, roles: `ccc.oo.vvvOp.vvvOp.cc`},
		{src: `env -u HOME ls`, roles: `ccc.oo.pppp.cc`},
		{src: `nohup nice -n 10 make`, roles: `ccccc.cccc.oo.pp.cccc`},
		{src: `sudo -- -x`, roles: `cccc.oo.!!`},
		{src: `time nosuch`, roles: `kkkk.!!!!!!`},
		{src: `ec`, roles: `!!`}, // still being typed: fish shows it red too
	})
	if _, ok := sh.funcs["hello"]; ok || sh.IsSet("hl_loaded") {
		t.Error("highlighting loaded an autoload function")
	}
	if sh.IsSet("hl_ran") {
		t.Error("highlighting ran an abbreviation function")
	}

	sh.SetVar("EDITOR", []string{"vim"}, setOpts{scope: setGlobal})
	sh.SetVar("BADEDITOR", []string{"nosuch"}, setOpts{scope: setGlobal})
	sh.SetVar("cmd", []string{"git", "status"}, setOpts{scope: setGlobal})
	checkRoles(t, sh, false, []hlCase{
		{src: `$EDITOR file`, roles: `vvvvvvv.pppp`},
		{src: `$BADEDITOR file`, roles: `!!!!!!!!!!.pppp`},
		{src: `$cmd`, roles: `vvvv`},
		{src: `"$EDITOR"`, roles: `qvvvvvvvq`},
	})
}

func TestHighlightPaths(t *testing.T) {
	sh := hlShell(t)
	checkRoles(t, sh, false, []hlCase{
		{src: `cat README.md main.go nope src/ src/a.txt "my file.txt" my\ file.txt link`,
			roles: `ccc.ppppppppp.ppppppp.pppp.pppp.ppppppppp.qqqqqqqqqqqqq.ppeepppppppp.pppp`,
			paths: `    _________ _______      ____ _________ _____________ ____________ ____`},
		// The word being typed at the end only needs to be a prefix.
		{src: `cat REA`, roles: `ccc.ppp`, paths: `    ___`},
		{src: `cat REA `, roles: `ccc.ppp.`},
		{src: `cat REA | cat`, roles: `ccc.ppp.;.ccc`},
		{src: `cat src/l`, roles: `ccc.ppppp`, paths: `    _____`},
		{src: `cat src/x`, roles: `ccc.ppppp`},
		{src: `cat src/lib/`, roles: `ccc.pppppppp`, paths: `    ________`},
		{src: `cat README.md/`, roles: `ccc.pppppppppp`},
		{src: `cat "my f`, roles: `ccc.!qqqq`, paths: `    _____`},
		{src: `echo $HOME/x ~/.config`, roles: `cccc.vvvvvpp.Opppppppp`, paths: `             _________`},
		{src: `ls -l src`, roles: `cc.oo.ppp`, paths: `      ___`},
		{src: `for f in src/*.txt main.go; end`, roles: `kkk.p.kk.ppppOpppp.ppppppp;.kkk`, paths: `                   _______`},
		// cd wants a directory, and says so.
		{src: `cd src`, roles: `cc.ppp`, paths: `   ___`},
		{src: `cd nope`, roles: `cc.!!!!`},
		{src: `cd sr`, roles: `cc.pp`, paths: `   __`},
		{src: `cd sr `, roles: `cc.!!.`},
		{src: `cd README.md`, roles: `cc.!!!!!!!!!`},
		{src: `cd REA`, roles: `cc.!!!`},
		{src: `cd -`, roles: `cc.p`},
		{src: `cd -P src`, roles: `cc.oo.ppp`, paths: `      ___`},
		{src: `cd ~`, roles: `cc.O`, paths: `   _`},
		{src: `cd ..`, roles: `cc.pp`, paths: `   __`},
		{src: `cd link`, roles: `cc.pppp`, paths: `   ____`},
		{src: `cd src/li`, roles: `cc.pppppp`, paths: `   ______`},
		{src: `cd $HOME`, roles: `cc.vvvvv`, paths: `   _____`},
		{src: `cd $NOSUCHVAR`, roles: `cc.vvvvvvvvvv`},
		{src: `cd proj`, roles: `cc.!!!!`},
		// Redirection targets are checked rather than underlined.
		{src: `cat < READ`, roles: `ccc.r.rrrr`},
		{src: `cat < READ | cat`, roles: `ccc.r.!!!!.;.ccc`},
		{src: `cat < src`, roles: `ccc.r.!!!`},
	})
	if hlFoldCase {
		checkRoles(t, sh, false, []hlCase{{src: `cat read`, roles: `ccc.pppp`, paths: `    ____`}})
	} else {
		checkRoles(t, sh, false, []hlCase{{src: `cat read`, roles: `ccc.pppp`}})
	}
	sh.SetVar("CDPATH", []string{filepath.Dir(sh.dir) + "/home"}, setOpts{scope: setGlobal})
	checkRoles(t, sh, false, []hlCase{
		{src: `cd proj`, roles: `cc.pppp`, paths: `   ____`},
		{src: `cd pr`, roles: `cc.pp`, paths: `   __`},
		{src: `cd ./proj`, roles: `cc.!!!!!!`},
	})
}

func TestHighlightIncomplete(t *testing.T) {
	checkRoles(t, hlShell(t), false, []hlCase{
		{src: `echo "abc`, roles: `cccc.!qqq`},
		{src: `echo 'abc`, roles: `cccc.!qqq`},
		{src: `echo $'abc`, roles: `cccc.!qqqq`},
		{src: "echo 'multi\nline", roles: `cccc.!qqqqqqqqqq`},
		{src: `echo (ls`, roles: `cccc.Occ`},
		{src: `echo $(ls -`, roles: `cccc.OOcc.p`},
		{src: `echo "$(ls`, roles: `cccc.!OOcc`},
		{src: `cat <(ls`, roles: `ccc.OOcc`},
		{src: "echo `ls", roles: `cccc.Occ`},
		{src: `echo ${x`, roles: `cccc.vvv`},
		{src: `echo ${x:-`, roles: `cccc.vvvOO`},
		{src: `echo $`, roles: `cccc.v`},
		{src: `echo a$`, roles: `cccc.pv`},
		{src: `echo $ b`, roles: `cccc.p.p`},
		{src: `echo {a,b`, roles: `cccc.OpOp`},
		{src: `echo $x[`, roles: `cccc.vvp`},
		{src: `echo foo\`, roles: `cccc.pppe`},
		{src: `ls |`, roles: `cc.;`},
		{src: `ls &&`, roles: `cc.;;`},
		{src: `ls ||`, roles: `cc.;;`},
		{src: "ls |\n  grep x", roles: `cc.;...cccc.p`},
		{src: `ls >`, roles: `cc.r`},
		{src: `ls 2>`, roles: `cc.rr`},
		{src: "cat <<EOF\nhello", roles: `ccc.rrrrr.qqqqq`},
		{src: `if true; then`, roles: `kk.cccc;.kkkk`},
		{src: `if`, roles: `kk`},
		{src: `while`, roles: `kkkkk`},
		{src: `for x in`, roles: `kkk.p.kk`},
		{src: `case x in`, roles: `kkkk.p.kk`},
		{src: `case x in a)`, roles: `kkkk.p.kk.pO`},
		{src: `function`, roles: `kkkkkkkk`},
		{src: `begin`, roles: `kkkkk`},
		{src: `{`, roles: `k`},
		{src: `(`, roles: `O`},
		{src: `[[ -f`, roles: `kk.oo`},
		{src: `((`, roles: `OO`},
		{src: `(( x )`, roles: `OO.v.O`},
		{src: `echo $((`, roles: `cccc.OOO`},
		{src: `git sta`, roles: `ccc.ppp`},
	})
}

func TestHighlightRunes(t *testing.T) {
	sh := hlShell(t)
	for _, src := range []string{
		"",
		"echo \"héllo wörld\" 日本 | grep ü",
		"ls 'ünïcödé\n",
		"échø $x\xff\xfe end",
		"🙂",
	} {
		out := sh.Highlight(src)
		if len(out) != utf8.RuneCountInString(src) {
			t.Errorf("%q: %d styles for %d runes", src, len(out), utf8.RuneCountInString(src))
		}
	}
	roles, _ := hlRoles(sh, "echo \"héllo wörld\" 日本 | grep ü", false)
	if want := `cccc.qqqqqqqqqqqqq.pp.;.cccc.p`; roles != want {
		t.Errorf("multibyte roles %q, want %q", roles, want)
	}
}

func TestHighlightStyles(t *testing.T) {
	sh := hlShell(t)
	out := sh.Highlight("ls README.md 'x'")
	sgr := func(role string) string { return sh.Style(role).SGR() }
	path := hlMergePath(sh.Style("param"), sh.Style("valid_path")).SGR()
	want := []string{sgr("command"), sgr("command"), "", path, path, path, path, path, path, path, path, path, "", sgr("quote"), sgr("quote"), sgr("quote")}
	if strings.Join(out, "|") != strings.Join(want, "|") {
		t.Errorf("styles:\n got %q\nwant %q", out, want)
	}
	if path != "\x1b[4;39m" {
		t.Errorf("default valid path style %q", path)
	}

	// Theme variables apply, and keyword/variable fall back to the
	// command/operator colors when only those are customized (fish does).
	sh.SetVar("fish_color_command", []string{"red"}, setOpts{scope: setGlobal})
	sh.SetVar("fish_color_operator", []string{"green"}, setOpts{scope: setGlobal})
	sh.SetVar("fish_color_valid_path", []string{"yellow", "--bold"}, setOpts{scope: setGlobal})
	out = sh.Highlight("if ls $x src")
	for i, want := range map[int]string{0: "\x1b[31m", 3: "\x1b[31m", 6: "\x1b[32m", 9: "\x1b[1;33m"} {
		if out[i] != want {
			t.Errorf("style %d = %q, want %q", i, out[i], want)
		}
	}
	sh.SetVar("ash_color_keyword", []string{"blue"}, setOpts{scope: setGlobal})
	if out := sh.Highlight("if"); out[0] != "\x1b[34m" {
		t.Errorf("keyword style %q", out[0])
	}
}

func TestSuggestionOK(t *testing.T) {
	sh := hlShell(t)
	sh.SetVar("CDPATH", []string{filepath.Dir(sh.dir) + "/home"}, setOpts{scope: setGlobal})
	for _, tc := range []struct {
		line string
		ok   bool
	}{
		{"ls -la", true},
		{"nosuch foo", false},
		{"notexec", false},
		{"ll", true},
		{"greet you", true},
		{"hello", true},
		{"gco main", false}, // abbreviations aren't commands
		{"./run.sh", true},
		{"./nope.sh", false},
		{"src/", true},
		{"cd src", true},
		{"cd src/lib", true},
		{"cd nope", false},
		{"cd README.md", false},
		{"cd .", false}, // already there
		{"cd ../work", false},
		{"cd", true},
		{"cd -", true},
		{"cd ~", true},
		{"cd ..", true},
		{"cd -P src", true},
		{"cd -- nope", false},
		{"cd proj", true}, // via CDPATH
		{`cd "$HOME"`, true},
		{"cd $NOSUCHVAR", true},
		{"cd src; nosuch", true},
		{"FOO=1 ls", true},
		{"FOO=1 nosuch", false},
		{"sudo ls", true},
		{"sudo nosuch", false},
		{"env FOO=1 nosuch", false},
		{"time nosuch", false},
		{"if nosuch; then ls; fi", false},
		{"if ls; then nosuch; fi", true},
		{"for x in a; do nosuch; done", false},
		{"(nosuch)", false},
		{"echo $(nosuch)", true},
		{"$(nosuch) x", true},
		{"true && nosuch", true},
		{"f() { nosuch; }", true},
		{"", true},
		{"   ", true},
		{"# comment", true},
		{"echo 'unterminated", true},
	} {
		if got := sh.SuggestionOK(tc.line); got != tc.ok {
			t.Errorf("SuggestionOK(%q) = %v, want %v", tc.line, got, tc.ok)
		}
	}
	if _, ok := sh.funcs["hello"]; ok || sh.IsSet("hl_loaded") {
		t.Error("SuggestionOK loaded an autoload function")
	}
}

// For complete input, the highlighter flags exactly the lines the parser
// rejects (all commands here exist). Some errors only show at run time,
// like "and" after a pipe, which runs a command named "and"; those are
// covered by TestHighlightErrors.
func TestHighlightAgreesWithParser(t *testing.T) {
	sh := hlShell(t)
	for _, src := range []string{
		"echo a; echo b", "echo a; ; echo b", "if true; then echo; fi", "if true; echo; end",
		"if true; then echo; elif false; then echo; else echo; fi", "if true; echo; else if false; echo; else; echo; end",
		"while false; do echo; done", "while false; echo; end", "until true; do echo; done",
		"for x in a b; echo $x; end", "for x in a; do echo; done", "for ((i=0; i<2; i++)); do echo; done",
		"case x in a|b) echo;; *) echo;; esac", "switch x; case a; echo; case '*'; end",
		"begin; echo; end", "{ echo; }", "(echo) | cat", "f() { echo; }", "function f; echo; end",
		"function f { echo; }", "echo a | cat && true || false", "! true", "not true",
		"true; and false; or true", "time ls", "echo $(ls) (ls) \"$(ls)\" `ls`", "FOO=1 ls",
		"FOO=1", "ls > /dev/null 2>&1", "[[ -n x && x == y ]]", "(( 1 + 2 ))", "echo {a,b} {1..3}",
		"ls &", "ls &!", "cat <<EOF\nhi\nEOF", "echo ${x:-a} ${#x} $x[1]", "if true; end > /dev/null",
		"fi", "done", "end", "esac", "then", "else", "}", "in", ")", "echo a )", "echo a;; echo b",
		"if true; then echo; end", "while true; echo; done", "| cat", "&& ls", "& ls", "ls | | cat", "if ; then echo; fi", "begin; end foo",
		"ls > ; echo", "if true; then echo; fi foo", "echo $(fi)", "begin; done", "> x if true; then :; fi",
	} {
		_, err := syntax.Parse(src)
		if syntax.IsIncomplete(err) {
			t.Errorf("%q: test input is incomplete: %v", src, err)
			continue
		}
		roles, _ := hlRoles(sh, src, false)
		if flagged := strings.Contains(roles, "!"); flagged != (err != nil) {
			t.Errorf("%q: parser error %v, highlight %q", src, err, roles)
		}
	}
}

// Over its time budget, the highlighter stops consulting the file system
// and gives commands and paths the benefit of the doubt.
func TestHighlightBudget(t *testing.T) {
	sh := hlShell(t)
	h := sh.newHighlighter("nosuch src > /nonexistent/x; cd nope")
	h.start = time.Now().Add(-time.Minute)
	h.list(0, 0)
	for i, r := range h.col {
		if r&hlRoleMask == hlError {
			t.Errorf("byte %d marked as an error over budget", i)
		}
	}
	h.release()
	if roles, _ := hlRoles(sh, "nosuch", false); roles != "!!!!!!" {
		t.Errorf("budget leaked into the next call: %q", roles)
	}
}

// Directory listings for prefix matches are reused until the directory
// changes.
func TestHighlightDirCache(t *testing.T) {
	sh := hlShell(t)
	dir := filepath.Join(sh.dir, "src")
	if _, paths := hlRoles(sh, "cat src/ne", false); paths != "" {
		t.Fatalf("unexpected match: %q", paths)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(dir, future, future); err != nil {
		t.Fatal(err)
	}
	if _, paths := hlRoles(sh, "cat src/ne", false); paths != "    ______" {
		t.Errorf("stale listing: paths %q", paths)
	}
}

// Hostile input (a pasted blob of brackets, say) must not make
// highlighting quadratic: each of these took seconds before look-ahead
// scans were budgeted, and takes a few milliseconds now.
func TestHighlightLinear(t *testing.T) {
	sh := hlShell(t)
	const n = 100000
	for name, src := range map[string]string{
		"braces":   "echo " + strings.Repeat("{a,", n/3),
		"quoted":   "echo " + strings.Repeat("{'", n/2),
		"globs":    "echo " + strings.Repeat("[", n),
		"arith":    "echo " + strings.Repeat("$((", n/3),
		"subst":    "echo " + strings.Repeat("$(", n/2),
		"index":    "echo " + strings.Repeat("$x[(", n/4),
		"subscr":   "echo " + strings.Repeat("${a[", n/4),
		"assign":   strings.Repeat("a[", n/2),
		"compound": strings.Repeat("if true; then case x in a) ", n/30),
	} {
		start := time.Now()
		out := sh.Highlight(src)
		sh.SuggestionOK(src)
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s: %v for %d bytes", name, d, len(src))
		}
		if len(out) != len(src) {
			t.Errorf("%s: %d styles for %d bytes", name, len(out), len(src))
		}
	}
}

func FuzzHighlight(f *testing.F) {
	for _, s := range []string{
		"echo \"a $b\" 'c' $(d) (e) `f` ${g:-h} $i[1] {j,k} ~/l *.m",
		"if a; then b; elif c; then d; else e; fi; while f; do g; done",
		"case $x in a|b) c;; (d) e;; esac; switch x; case y; end",
		"for ((i=0;i<3;i++)); do (( j += i )); done; [[ -f x && y =~ (z|w) ]]",
		"cat <<EOF >out 2>&1\n$x `y`\nEOF\ncat <<-'E'\n\tz\n\tE",
		"f() { g; }; function h -d x; i; end; { j; }",
		"sudo -u root env A=1 nice -n 1 command -v k | l &> m &! n &",
		"echo $'\\x41' $\"q\" \\\n x \\", "${", "$((", "$(", "<(", "[[", "((", "'", "\"", "`", "\\",
	} {
		f.Add(s)
	}
	sh := hlShell(f)
	f.Fuzz(func(t *testing.T, src string) {
		roles, _ := hlRoles(sh, src, false)
		if strings.Contains(roles, "?") {
			t.Errorf("%q: unclassified text: %q", src, roles)
		}
		hlRoles(sh, src, true)
		if n := len(sh.Highlight(src)); n != utf8.RuneCountInString(src) {
			t.Errorf("%q: %d styles", src, n)
		}
		sh.SuggestionOK(src)
	})
}

func BenchmarkHighlight(b *testing.B) {
	sh := hlShell(b)
	line := `git commit -m "fix: handle $USER input" && ls -la src/ | grep -v test > notes.txt`
	if n := utf8.RuneCountInString(line); n < 75 || n > 85 {
		b.Fatalf("line has %d runes", n)
	}
	b.ReportAllocs()
	for b.Loop() {
		sh.Highlight(line)
	}
}

// Typing a path in a big directory: the listing is read once.
func BenchmarkHighlightTypingPath(b *testing.B) {
	sh := hlShell(b)
	dir := filepath.Join(sh.dir, "big")
	if err := os.Mkdir(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	for i := range 2000 {
		if err := os.WriteFile(filepath.Join(dir, "file"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26%26))), nil, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		sh.Highlight("cat big/filexxxz")
	}
}

func BenchmarkSuggestionOK(b *testing.B) {
	sh := hlShell(b)
	b.ReportAllocs()
	for b.Loop() {
		sh.SuggestionOK("cd src && git status --short | grep -v '^??'")
	}
}
