package shell

// fzf key bindings, built like the ones `fzf --bash` and `fzf --fish`
// print: ctrl-r searches history, ctrl-t inserts files, alt-c changes
// directory. The options go through FZF_DEFAULT_OPTS in the same order as
// fzf's scripts put them, so FZF_DEFAULT_OPTS, FZF_DEFAULT_OPTS_FILE and
// the FZF_CTRL_R/CTRL_T/ALT_C variables behave the same.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func init() {
	registerIntegration(integration{"fzf", "fzf", "ctrl-r history, ctrl-t files and alt-c cd with fzf", setupFzf})
	builtins["__ash_fzf_history"] = bFzfHistory
	builtinHelp["__ash_fzf_history"] = "search history with fzf (ctrl-r)"
	builtins["__ash_fzf_files"] = bFzfFiles
	builtinHelp["__ash_fzf_files"] = "insert files picked with fzf (ctrl-t)"
	builtins["__ash_fzf_cd"] = bFzfCd
	builtinHelp["__ash_fzf_cd"] = "cd into a directory picked with fzf (alt-c)"
}

func setupFzf(sh *Shell, io *IO, exe string) string {
	// Like fzf's scripts, a widget whose *_COMMAND variable is set to the
	// empty string is left unbound.
	var bs []integBinding
	var keys []string
	for _, w := range []struct{ key, cmd, v string }{
		{"ctrl-r", "__ash_fzf_history", "FZF_CTRL_R_COMMAND"},
		{"ctrl-t", "__ash_fzf_files", "FZF_CTRL_T_COMMAND"},
		{"alt-c", "__ash_fzf_cd", "FZF_ALT_C_COMMAND"},
	} {
		if sh.IsSet(w.v) && sh.Get(w.v) == "" {
			continue
		}
		bs = append(bs, integBinding{w.key, w.cmd})
		keys = append(keys, w.key)
	}
	sh.integrateBind(bs...)
	msg := "bound " + strings.Join(keys, ", ")
	if sh.Editor == nil {
		msg += " (when the line editor starts)"
	}
	return msg
}

// fzfDefaults is fzf's __fzf_defaults: our defaults, then the options
// file, then $FZF_DEFAULT_OPTS, then the widget's own options.
func (sh *Shell) fzfDefaults(prepend, extra string) string {
	height := sh.Get("FZF_TMUX_HEIGHT")
	if height == "" {
		height = "40%"
	}
	var b strings.Builder
	b.WriteString("--height ")
	b.WriteString(height)
	b.WriteString(" --min-height 20+ --bind=ctrl-z:ignore ")
	b.WriteString(prepend)
	b.WriteByte('\n')
	if f := sh.Get("FZF_DEFAULT_OPTS_FILE"); f != "" {
		if data, err := os.ReadFile(sh.absPath(f)); err == nil && len(data) > 0 {
			b.Write(data)
			if data[len(data)-1] != '\n' {
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString(sh.Get("FZF_DEFAULT_OPTS"))
	b.WriteByte(' ')
	b.WriteString(extra)
	return strings.TrimRight(b.String(), "\n")
}

// fzfCommand is fzf's __fzfcmd: fzf-tmux inside tmux when FZF_TMUX or
// FZF_TMUX_OPTS ask for it, fzf otherwise.
func (sh *Shell) fzfCommand(args []string) (*exec.Cmd, bool) {
	name := "fzf"
	if sh.Get("TMUX_PANE") != "" {
		t, opts := sh.Get("FZF_TMUX"), sh.Get("FZF_TMUX_OPTS")
		if t != "" && t != "0" || opts != "" {
			pre := strings.Fields(opts)
			if len(pre) == 0 {
				h := sh.Get("FZF_TMUX_HEIGHT")
				if h == "" {
					h = "40%"
				}
				pre = []string{"-d" + h}
			}
			name = "fzf-tmux"
			args = append(append(pre, "--"), args...)
		}
	}
	p, ok := sh.LookPath(name)
	if !ok {
		return nil, false
	}
	return sh.Command(p, args...), true
}

// fzfRun runs fzf with env added to the shell's environment and returns
// the selected items (fzf runs with --print0) and its status. fzf draws
// on the terminal through stderr and reads keys from /dev/tty.
func (sh *Shell) fzfRun(io *IO, name string, env []string, stdin io.Reader, args []string) ([]string, int) {
	c, ok := sh.fzfCommand(args)
	if !ok {
		fmt.Fprintf(io.Err, "%s: fzf not found\n", name)
		return nil, 127
	}
	c.Env = append(c.Env, env...)
	c.Stdin = stdin
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = integWriter(io.Err)
	st := integStatus(c.Run())
	return integSplit0(out.String()), st
}

// integListReader streams items, each followed by sep, without building
// one big buffer.
type integListReader struct {
	items []string
	off   int
	sep   byte
}

func (r *integListReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) && len(r.items) > 0 {
		s := r.items[0]
		if r.off < len(s) {
			c := copy(p[n:], s[r.off:])
			n += c
			r.off += c
			continue
		}
		p[n] = r.sep
		n++
		r.items = r.items[1:]
		r.off = 0
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// bFzfHistory is ctrl-r: pick commands from history (newest first) with
// the command line as the query; the selection replaces the command line.
func bFzfHistory(sh *Shell, io *IO, args []string) int {
	buf := ""
	if sh.Editor != nil {
		buf, _ = sh.Editor.Buffer()
	}
	var entries []string
	if sh.History != nil {
		all := sh.History.Recent(0)
		seen := make(map[string]struct{}, len(all))
		entries = make([]string, 0, len(all))
		for _, e := range all {
			if _, dup := seen[e]; !dup {
				seen[e] = struct{}{}
				entries = append(entries, e)
			}
		}
	}
	// shift-delete deletes the marked entries from history, as in fzf's
	// own scripts: fzf appends them to a file we read afterwards.
	del := ""
	deleteBind := ""
	if sh.History != nil {
		if f, err := os.CreateTemp("", "ash-fzf-delete-*"); err == nil {
			del = f.Name()
			f.Close()
			defer os.Remove(del)
			if !strings.ContainsAny(del, "'\"\\$`") {
				deleteBind = " --bind 'shift-delete:execute-silent(cat {+f} >> \"" + del + "\")+exclude-multi'"
			}
		}
	}
	opts := sh.fzfDefaults("", "--scheme=history --bind=ctrl-r:toggle-sort,alt-r:toggle-raw --wrap-sign '\t↳ ' --highlight-line"+
		deleteBind+" --multi "+sh.Get("FZF_CTRL_R_OPTS")+" --read0 --print0")
	env := []string{"FZF_DEFAULT_OPTS=" + opts, "FZF_DEFAULT_OPTS_FILE="}
	sel, st := sh.fzfRun(io, args[0], env, &integListReader{items: entries}, append([]string{"--query", buf}, args[1:]...))
	if del != "" {
		if data, err := os.ReadFile(del); err == nil {
			for _, e := range integSplit0(string(data)) {
				sh.History.Delete(e)
			}
		}
	}
	if len(sel) == 0 {
		return fzfNothing(st)
	}
	line := strings.Join(sel, "\n")
	if sh.Editor == nil {
		fmt.Fprintln(io.Out, line)
		return 0
	}
	sh.Editor.SetBuffer(line, len(line))
	return 0
}

// bFzfFiles is ctrl-t: insert the picked files, quoted, at the cursor.
// $FZF_CTRL_T_COMMAND lists them; when it is empty fzf walks the tree.
func bFzfFiles(sh *Shell, io *IO, args []string) int {
	opts := sh.fzfDefaults("--reverse --walker=file,dir,follow,hidden --scheme=path", sh.Get("FZF_CTRL_T_OPTS")+" -m --print0")
	env := []string{"FZF_DEFAULT_COMMAND=" + sh.Get("FZF_CTRL_T_COMMAND"), "FZF_DEFAULT_OPTS=" + opts, "FZF_DEFAULT_OPTS_FILE="}
	// fzf runs FZF_DEFAULT_COMMAND (or walks) only when stdin is the
	// terminal, so it gets ours.
	sel, st := sh.fzfRun(io, args[0], env, integReader(io.In), args[1:])
	if len(sel) == 0 {
		return fzfNothing(st)
	}
	var b strings.Builder
	for _, s := range sel {
		b.WriteString(Quote(s))
		b.WriteByte(' ')
	}
	text := b.String()
	if sh.Editor == nil {
		fmt.Fprintln(io.Out, text)
		return 0
	}
	buf, pos := sh.Editor.Buffer()
	if pos < 0 || pos > len(buf) {
		pos = len(buf)
	}
	sh.Editor.SetBuffer(buf[:pos]+text+buf[pos:], pos+len(text))
	return 0
}

// bFzfCd is alt-c: cd into the picked directory ($FZF_ALT_C_COMMAND
// lists them; when it is empty fzf walks directories).
func bFzfCd(sh *Shell, io *IO, args []string) int {
	opts := sh.fzfDefaults("--reverse --walker=dir,follow,hidden --scheme=path", sh.Get("FZF_ALT_C_OPTS")+" +m --print0")
	env := []string{"FZF_DEFAULT_COMMAND=" + sh.Get("FZF_ALT_C_COMMAND"), "FZF_DEFAULT_OPTS=" + opts, "FZF_DEFAULT_OPTS_FILE="}
	sel, st := sh.fzfRun(io, args[0], env, integReader(io.In), args[1:])
	if len(sel) == 0 {
		return fzfNothing(st)
	}
	// An absolute path keeps $CDPATH out of it, like fzf's `unset CDPATH`.
	return sh.changeDir(sh.absPath(sel[0]), io, false)
}

// fzfNothing is the status when nothing was picked: 1 when the user
// cancelled or nothing matched, fzf's own status on errors.
func fzfNothing(st int) int {
	if st == 0 || st == 1 || st == 130 {
		return 1
	}
	return st
}
