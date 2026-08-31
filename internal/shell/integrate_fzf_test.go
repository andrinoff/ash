package shell

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// fzfTest installs a fake fzf that saves its environment and stdin next
// to the log, plays a shift-delete when $LOG.delete exists, prints
// $LOG.reply and exits with $LOG.status.
func fzfTest(t *testing.T) (*integTest, *integEditor) {
	t.Chdir(t.TempDir())
	it := newIntegTest(t)
	it.tool("fzf", fakeFzf)
	ed := &integEditor{}
	it.sh.Editor = ed
	it.mustRun("integrate fzf")
	return it, ed
}

const fakeFzf = `printf '%s' "$FZF_DEFAULT_OPTS" > "$LOG.opts"
printf '%s' "${FZF_DEFAULT_COMMAND-unset}" > "$LOG.cmd"
printf '%s' "${FZF_DEFAULT_OPTS_FILE-unset}" > "$LOG.optsfile"
cat > "$LOG.stdin"
if [ -f "$LOG.delete" ]; then
	del=$(printf '%s' "$FZF_DEFAULT_OPTS" | sed -n 's/.*cat {+f} >> "\([^"]*\)".*/\1/p')
	cat "$LOG.delete" >> "$del"
fi
cat "$LOG.reply" 2>/dev/null
exit "$(cat "$LOG.status" 2>/dev/null || echo 0)"`

func TestFzfBindings(t *testing.T) {
	cases := []struct {
		name, setup string
		want        []string
	}{
		{"all", "", []string{
			"default ctrl-r __ash_fzf_history", "insert ctrl-r __ash_fzf_history",
			"default ctrl-t __ash_fzf_files", "insert ctrl-t __ash_fzf_files",
			"default alt-c __ash_fzf_cd", "insert alt-c __ash_fzf_cd"}},
		// Like fzf's scripts: an empty *_COMMAND turns its widget off.
		{"ctrl-t off", "FZF_CTRL_T_COMMAND=", []string{
			"default ctrl-r __ash_fzf_history", "insert ctrl-r __ash_fzf_history",
			"default alt-c __ash_fzf_cd", "insert alt-c __ash_fzf_cd"}},
		{"custom commands keep it on", "FZF_CTRL_T_COMMAND='fd -t f'; FZF_ALT_C_COMMAND=", []string{
			"default ctrl-r __ash_fzf_history", "insert ctrl-r __ash_fzf_history",
			"default ctrl-t __ash_fzf_files", "insert ctrl-t __ash_fzf_files"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it := newIntegTest(t)
			it.tool("fzf", "")
			ed := &integEditor{}
			it.sh.Editor = ed
			it.mustRun(c.setup + "\nintegrate fzf")
			if !equalLines(ed.binds, c.want) {
				t.Errorf("binds\n%q\nwant\n%q", ed.binds, c.want)
			}
			if calls := it.calls(); calls != nil {
				t.Errorf("fzf ran during integrate: %q", calls)
			}
		})
	}
}

var fzfDeleteFile = regexp.MustCompile(`cat \{\+f\} >> "[^"]*"`)

func TestFzfHistory(t *testing.T) {
	it, ed := fzfTest(t)
	h := &integHistory{entries: []string{"git status", "make test", "git status", "echo 'multi\nline'"}}
	it.sh.History = h
	it.write("opts-file", "--border\n")
	it.mustRun(`FZF_DEFAULT_OPTS='--color=dark --info inline'; FZF_CTRL_R_OPTS="--preview 'echo {}'"; FZF_DEFAULT_OPTS_FILE=` + it.home + "/opts-file")
	ed.buf, ed.cursor = "git", 3
	it.write("log.reply", "make test\x00")

	it.mustRun("__ash_fzf_history")
	if ed.buf != "make test" || ed.cursor != 9 {
		t.Errorf("buffer %q cursor %d", ed.buf, ed.cursor)
	}
	if calls := it.calls(); !equalLines(calls, []string{"fzf [--query] [git]"}) {
		t.Errorf("calls %q", calls)
	}
	// Newest first, each entry once, NUL-terminated so multi-line
	// commands stay whole.
	if got, want := it.file("log.stdin"), "git status\x00make test\x00echo 'multi\nline'\x00"; got != want {
		t.Errorf("stdin %q, want %q", got, want)
	}
	opts := fzfDeleteFile.ReplaceAllString(it.file("log.opts"), `cat {+f} >> "DEL"`)
	want := "--height 40% --min-height 20+ --bind=ctrl-z:ignore \n--border\n--color=dark --info inline " +
		"--scheme=history --bind=ctrl-r:toggle-sort,alt-r:toggle-raw --wrap-sign '\t↳ ' --highlight-line " +
		`--bind 'shift-delete:execute-silent(cat {+f} >> "DEL")+exclude-multi' --multi --preview 'echo {}' --read0 --print0`
	if opts != want {
		t.Errorf("FZF_DEFAULT_OPTS\n%q\nwant\n%q", opts, want)
	}
	if f := it.file("log.optsfile"); f != "" {
		t.Errorf("FZF_DEFAULT_OPTS_FILE=%q, want it empty", f)
	}

	// Several picks become several lines.
	it.write("log.reply", "a\x00b\x00")
	it.mustRun("__ash_fzf_history")
	if ed.buf != "a\nb" {
		t.Errorf("multi pick: %q", ed.buf)
	}

	// shift-delete removes entries from history.
	it.write("log.delete", "make test\x00")
	it.mustRun("__ash_fzf_history")
	if !equalLines(h.deleted, []string{"make test"}) {
		t.Errorf("deleted %q", h.deleted)
	}
	os.Remove(filepath.Join(it.home, "log.delete"))

	// Cancelled: the command line stays.
	ed.buf = "keep"
	it.write("log.reply", "")
	it.write("log.status", "130")
	if _, _, st := it.run("__ash_fzf_history"); st != 1 || ed.buf != "keep" {
		t.Errorf("cancel: status %d buffer %q", st, ed.buf)
	}
	// fzf errors are passed on.
	it.write("log.status", "2")
	if _, _, st := it.run("__ash_fzf_history"); st != 2 {
		t.Errorf("fzf error: status %d", st)
	}
	os.Remove(filepath.Join(it.home, "log.status"))

	// Without a line editor the pick is printed.
	it.sh.Editor = nil
	it.write("log.reply", "ls\x00")
	if out := it.mustRun("__ash_fzf_history"); out != "ls\n" {
		t.Errorf("no editor: %q", out)
	}
}

func TestFzfFiles(t *testing.T) {
	it, ed := fzfTest(t)
	it.write("log.reply", "src/a b.go\x00README.md\x00")
	ed.buf, ed.cursor = "cat  | wc", 4
	it.mustRun("__ash_fzf_files")
	if want := "cat 'src/a b.go' README.md  | wc"; ed.buf != want || ed.cursor != len("cat 'src/a b.go' README.md ") {
		t.Errorf("buffer %q cursor %d, want %q", ed.buf, ed.cursor, want)
	}
	if calls := it.calls(); !equalLines(calls, []string{"fzf"}) {
		t.Errorf("calls %q", calls)
	}
	// Unset FZF_CTRL_T_COMMAND: an empty FZF_DEFAULT_COMMAND makes fzf walk.
	if c := it.file("log.cmd"); c != "" {
		t.Errorf("FZF_DEFAULT_COMMAND=%q", c)
	}
	if got, want := it.file("log.opts"), "--height 40% --min-height 20+ --bind=ctrl-z:ignore --reverse --walker=file,dir,follow,hidden --scheme=path\n  -m --print0"; got != want {
		t.Errorf("FZF_DEFAULT_OPTS\n%q\nwant\n%q", got, want)
	}

	it.mustRun(`FZF_CTRL_T_COMMAND='fd --type f'; FZF_CTRL_T_OPTS="--preview 'bat {}'"; FZF_TMUX_HEIGHT=60%`)
	it.write("log.reply", "x\x00")
	ed.buf, ed.cursor = "", 0
	it.mustRun("__ash_fzf_files")
	if ed.buf != "x " {
		t.Errorf("buffer %q", ed.buf)
	}
	if c := it.file("log.cmd"); c != "fd --type f" {
		t.Errorf("FZF_DEFAULT_COMMAND=%q", c)
	}
	if got, want := it.file("log.opts"), "--height 60% --min-height 20+ --bind=ctrl-z:ignore --reverse --walker=file,dir,follow,hidden --scheme=path\n --preview 'bat {}' -m --print0"; got != want {
		t.Errorf("FZF_DEFAULT_OPTS\n%q\nwant\n%q", got, want)
	}
}

func TestFzfCd(t *testing.T) {
	it, _ := fzfTest(t)
	start := it.mkdir("start", "start/sub dir", "cdpath/sub dir")
	it.mustRun("cd " + start + "; CDPATH=" + it.home + "/cdpath; FZF_ALT_C_COMMAND='fd -t d'; FZF_ALT_C_OPTS='--no-sort'")
	it.write("log.reply", "sub dir\x00")
	it.mustRun("__ash_fzf_cd")
	// Relative to the current directory, never through $CDPATH.
	if want := filepath.Join(start, "sub dir"); it.sh.Dir() != want {
		t.Errorf("in %s, want %s", it.sh.Dir(), want)
	}
	if c := it.file("log.cmd"); c != "fd -t d" {
		t.Errorf("FZF_DEFAULT_COMMAND=%q", c)
	}
	if got, want := it.file("log.opts"), "--height 40% --min-height 20+ --bind=ctrl-z:ignore --reverse --walker=dir,follow,hidden --scheme=path\n --no-sort +m --print0"; got != want {
		t.Errorf("FZF_DEFAULT_OPTS\n%q\nwant\n%q", got, want)
	}
	it.write("log.reply", "")
	it.write("log.status", "130")
	if _, _, st := it.run("__ash_fzf_cd"); st != 1 {
		t.Errorf("cancel: status %d", st)
	}
}

func TestFzfTmux(t *testing.T) {
	it, _ := fzfTest(t)
	it.tool("fzf-tmux", "")
	it.mustRun("TMUX_PANE=%1; __ash_fzf_cd; true")
	if calls := it.calls(); !equalLines(calls, []string{"fzf"}) {
		t.Errorf("without FZF_TMUX: calls %q", calls)
	}
	cases := []struct{ setup, want string }{
		{"FZF_TMUX=1", "fzf-tmux [-d40%] [--]"},
		{"FZF_TMUX=0; FZF_TMUX_OPTS='-p 80%'", "fzf-tmux [-p] [80%] [--]"},
	}
	for _, c := range cases {
		it.mustRun(c.setup + "; __ash_fzf_cd; true")
		if calls := it.calls(); !equalLines(calls, []string{c.want}) {
			t.Errorf("%s: calls %q, want %q", c.setup, calls, c.want)
		}
	}
}

func TestRealFzfAcceptsOurOptions(t *testing.T) {
	it := newIntegTest(t)
	it.realTool("fzf")
	ed := &integEditor{buf: "stat", cursor: 4}
	it.sh.Editor = ed
	it.sh.History = &integHistory{entries: []string{"git status", "make", "stat -f %z x"}}
	it.mustRun("integrate fzf")

	// --filter makes fzf print its matches without a terminal, after
	// parsing every option the widget passes.
	it.mustRun("FZF_CTRL_R_OPTS=--filter=status")
	if _, errs, st := it.run("__ash_fzf_history"); st != 0 || ed.buf != "git status" || errs != "" {
		t.Errorf("history: status %d buffer %q stderr %q", st, ed.buf, errs)
	}
	// Without a terminal on stdin fzf reads it (empty here): no match.
	it.mustRun("FZF_CTRL_T_OPTS=--filter=x; FZF_ALT_C_OPTS=--filter=x")
	for _, w := range []string{"__ash_fzf_files", "__ash_fzf_cd"} {
		if _, errs, st := it.run(w); st != 1 || errs != "" {
			t.Errorf("%s: status %d stderr %q", w, st, errs)
		}
	}
}
