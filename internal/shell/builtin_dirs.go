package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func bPwd(sh *Shell, io *IO, args []string) int {
	physical := false
	for _, a := range args[1:] {
		switch a {
		case "-P", "--physical":
			physical = true
		case "-L", "--logical":
			physical = false
		default:
			return sh.usage(io, "pwd", "unknown option "+a)
		}
	}
	d := sh.dir
	if physical {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			d = r
		}
	}
	fmt.Fprintln(io.Out, d)
	return 0
}

func bCd(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	for len(a) > 0 && (a[0] == "-L" || a[0] == "-P" || a[0] == "--") {
		if a[0] == "--" {
			a = a[1:]
			break
		}
		a = a[1:]
	}
	var target string
	switch {
	case len(a) == 0:
		target = sh.HomeDir()
	case a[0] == "-":
		if len(sh.dirPrev) > 0 {
			target = sh.dirPrev[len(sh.dirPrev)-1]
		} else if v := sh.Get("OLDPWD"); v != "" {
			target = v
		} else {
			fmt.Fprintln(io.Err, "cd: no previous directory")
			return 1
		}
	default:
		target = a[0]
	}
	if len(a) > 1 {
		fmt.Fprintln(io.Err, "cd: too many arguments")
		return 1
	}
	return sh.changeDir(target, io, len(a) > 0 && a[0] == "-")
}

// changeDir changes the shell's directory, honoring CDPATH.
func (sh *Shell) changeDir(target string, io *IO, echoDir bool) int {
	dest := ""
	if !filepath.IsAbs(target) && !strings.HasPrefix(target, ".") {
		for _, cp := range sh.GetList("CDPATH") {
			if cp == "" {
				continue
			}
			cand := filepath.Join(sh.absPath(sh.expandTildeString(cp)), target)
			if st, err := os.Stat(cand); err == nil && st.IsDir() {
				dest = cand
				if cp != "." {
					echoDir = true
				}
				break
			}
		}
	}
	if dest == "" {
		dest = sh.absPath(target)
	}
	dest = filepath.Clean(dest)
	st, err := os.Stat(dest)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			msg := fmt.Sprintf("cd: %s: no such directory", target)
			if s := sh.suggestDir(target); s != "" {
				msg += fmt.Sprintf(" (did you mean %s?)", s)
				sh.noteFix(target, s)
			}
			fmt.Fprintln(io.Err, msg)
		} else {
			fmt.Fprintf(io.Err, "cd: %s\n", errText(err))
		}
		return 1
	}
	if !st.IsDir() {
		fmt.Fprintf(io.Err, "cd: %s: not a directory\n", target)
		return 1
	}
	if err := unix.Access(dest, unix.X_OK); err != nil {
		fmt.Fprintf(io.Err, "cd: %s: permission denied\n", target)
		return 1
	}
	if !sh.subshell {
		if err := os.Chdir(dest); err != nil {
			fmt.Fprintf(io.Err, "cd: %s\n", errText(err))
			return 1
		}
	}
	old := sh.dir
	if old != dest {
		sh.dirPrev = append(sh.dirPrev, old)
		if len(sh.dirPrev) > 25 {
			sh.dirPrev = sh.dirPrev[len(sh.dirPrev)-25:]
		}
		sh.dirNext = nil
	}
	sh.dir = dest
	sh.SetVar("OLDPWD", []string{old}, setOpts{scope: setGlobal, export: 1})
	sh.SetVar("PWD", []string{dest}, setOpts{scope: setGlobal, export: 1})
	if echoDir {
		fmt.Fprintln(io.Out, dest)
	}
	if !sh.subshell {
		if sh.OnDirChange != nil {
			sh.OnDirChange(dest)
		}
		sh.runDirHooks(dest)
	}
	return 0
}

// suggestDir offers a close match for a mistyped directory.
func (sh *Shell) suggestDir(target string) string {
	dir, base := filepath.Split(target)
	parent := sh.absPath(sh.expandTildeString(dir))
	ents, err := os.ReadDir(parent)
	if err != nil {
		return ""
	}
	best, bestD := "", 3
	for _, e := range ents {
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		n := e.Name()
		if strings.EqualFold(n, base) {
			return dir + n
		}
		if d := editDistance(strings.ToLower(base), strings.ToLower(n), bestD); d < bestD {
			best, bestD = dir+n, d
		}
	}
	if bestD <= 2 && len(base) > 2 {
		return best
	}
	return ""
}

func bPrevd(sh *Shell, io *IO, args []string) int {
	n := 1
	if len(args) > 1 {
		if v, err := strconv.Atoi(args[len(args)-1]); err == nil {
			n = v
		}
	}
	for ; n > 0; n-- {
		if len(sh.dirPrev) == 0 {
			return 1
		}
		target := sh.dirPrev[len(sh.dirPrev)-1]
		prev := append([]string(nil), sh.dirPrev[:len(sh.dirPrev)-1]...)
		next := append([]string{sh.dir}, sh.dirNext...)
		if st := sh.changeDir(target, io, false); st != 0 {
			return st
		}
		sh.dirPrev, sh.dirNext = prev, next
	}
	return 0
}

func bNextd(sh *Shell, io *IO, args []string) int {
	n := 1
	if len(args) > 1 {
		if v, err := strconv.Atoi(args[len(args)-1]); err == nil {
			n = v
		}
	}
	for ; n > 0; n-- {
		if len(sh.dirNext) == 0 {
			return 1
		}
		target := sh.dirNext[0]
		next := append([]string(nil), sh.dirNext[1:]...)
		prev := append(append([]string(nil), sh.dirPrev...), sh.dir)
		if st := sh.changeDir(target, io, false); st != 0 {
			return st
		}
		sh.dirPrev, sh.dirNext = prev, next
	}
	return 0
}

func bCdh(sh *Shell, io *IO, args []string) int {
	if len(args) > 1 {
		return sh.changeDir(args[1], io, false)
	}
	if sh.Interactive && io.In.TTY && io.Out.TTY {
		if pick, ok := builtins["__ash_pick_dir"]; ok {
			return pick(sh, io, []string{"__ash_pick_dir"})
		}
	}
	seen := map[string]bool{}
	var dirs []string
	for i := len(sh.dirPrev) - 1; i >= 0; i-- {
		d := sh.dirPrev[i]
		if !seen[d] && d != sh.dir {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		fmt.Fprintln(io.Err, "cdh: no previous directories")
		return 1
	}
	home := sh.HomeDir()
	for i, d := range dirs {
		if i >= 25 {
			break
		}
		show := d
		if strings.HasPrefix(d, home) {
			show = "~" + d[len(home):]
		}
		fmt.Fprintf(io.Out, "%2d) %s\n", i+1, show)
	}
	if sh.shared.readLine == nil {
		return 0
	}
	ans, err := sh.shared.readLine("cdh> ")
	if err != nil {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(ans))
	if err != nil || n < 1 || n > len(dirs) {
		return 1
	}
	return sh.changeDir(dirs[n-1], io, false)
}

func bPushd(sh *Shell, io *IO, args []string) int {
	if len(args) < 2 {
		if len(sh.dirStack) == 0 {
			fmt.Fprintln(io.Err, "pushd: no other directory")
			return 1
		}
		top := sh.dirStack[len(sh.dirStack)-1]
		cur := sh.dir
		if st := sh.changeDir(top, io, false); st != 0 {
			return st
		}
		sh.dirStack[len(sh.dirStack)-1] = cur
		return printDirs(sh, io)
	}
	cur := sh.dir
	target := args[1]
	if strings.HasPrefix(target, "+") || strings.HasPrefix(target, "-") {
		n, err := strconv.Atoi(target[1:])
		if err == nil {
			all := append([]string{sh.dir}, reversed(sh.dirStack)...)
			if target[0] == '-' {
				n = len(all) - 1 - n
			}
			if n < 0 || n >= len(all) {
				fmt.Fprintln(io.Err, "pushd: directory stack index out of range")
				return 1
			}
			rot := append(all[n:], all[:n]...)
			if st := sh.changeDir(rot[0], io, false); st != 0 {
				return st
			}
			sh.dirStack = reversed(rot[1:])
			return printDirs(sh, io)
		}
	}
	if st := sh.changeDir(target, io, false); st != 0 {
		return st
	}
	sh.dirStack = append(sh.dirStack, cur)
	return printDirs(sh, io)
}

func reversed(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

func bPopd(sh *Shell, io *IO, args []string) int {
	if len(sh.dirStack) == 0 {
		fmt.Fprintln(io.Err, "popd: directory stack empty")
		return 1
	}
	top := sh.dirStack[len(sh.dirStack)-1]
	if st := sh.changeDir(top, io, false); st != 0 {
		return st
	}
	sh.dirStack = sh.dirStack[:len(sh.dirStack)-1]
	return printDirs(sh, io)
}

func bDirs(sh *Shell, io *IO, args []string) int {
	if len(args) > 1 && args[1] == "-c" {
		sh.dirStack = nil
		return 0
	}
	return printDirs(sh, io)
}

func printDirs(sh *Shell, io *IO) int {
	home := sh.HomeDir()
	parts := []string{sh.dir}
	parts = append(parts, reversed(sh.dirStack)...)
	for i, d := range parts {
		if strings.HasPrefix(d, home) {
			parts[i] = "~" + d[len(home):]
		}
	}
	fmt.Fprintln(io.Out, strings.Join(parts, " "))
	return 0
}

func bRealpath(sh *Shell, io *IO, args []string) int {
	status := 0
	noSymlinks := false
	for _, a := range args[1:] {
		if a == "-s" || a == "--no-symlinks" {
			noSymlinks = true
			continue
		}
		p := sh.absPath(a)
		if !noSymlinks {
			r, err := filepath.EvalSymlinks(p)
			if err != nil {
				fmt.Fprintf(io.Err, "realpath: %s\n", errText(err))
				status = 1
				continue
			}
			p = r
		}
		fmt.Fprintln(io.Out, filepath.Clean(p))
	}
	return status
}

func bAddPath(sh *Shell, io *IO, args []string) int {
	appendMode, move, verbose, dry := false, false, false, false
	var dirs []string
	for _, a := range args[1:] {
		switch a {
		case "-a", "--append":
			appendMode = true
		case "-p", "--prepend":
			appendMode = false
		case "-m", "--move":
			move = true
		case "-v", "--verbose":
			verbose = true
		case "-n", "--dry-run":
			dry = true
		case "-g", "--global", "-U", "--universal", "-P", "--path":
		default:
			dirs = append(dirs, a)
		}
	}
	path := append([]string(nil), sh.GetList("PATH")...)
	changed := false
	var add []string
	for _, d := range dirs {
		d = sh.absPath(sh.expandTildeString(d))
		st, err := os.Stat(d)
		if err != nil || !st.IsDir() {
			if verbose {
				fmt.Fprintf(io.Err, "add_path: skipping %s (not a directory)\n", d)
			}
			continue
		}
		present := -1
		for i, p := range path {
			if p == d {
				present = i
				break
			}
		}
		if present >= 0 {
			if !move {
				continue
			}
			path = append(path[:present], path[present+1:]...)
		}
		add = append(add, d)
		changed = true
	}
	if !changed {
		return 1
	}
	if appendMode {
		path = append(path, add...)
	} else {
		path = append(add, path...)
	}
	if dry {
		fmt.Fprintln(io.Out, strings.Join(path, ":"))
		return 0
	}
	if verbose {
		fmt.Fprintf(io.Err, "add_path: PATH is now %s\n", strings.Join(path, ":"))
	}
	sh.SetVar("PATH", path, setOpts{scope: setGlobal, export: 1})
	return 0
}
