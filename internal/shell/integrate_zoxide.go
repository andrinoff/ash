package shell

// zoxide, the way `zoxide init` sets it up: z and zi jump, and every
// directory change is reported with `zoxide add`. The _ZO_* variables are
// zoxide's own configuration; they reach it through the environment.

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
)

func init() {
	registerIntegration(integration{"zoxide", "zoxide", "jump to frecent directories with zoxide (z, zi)", setupZoxide})
	builtins["zi"] = bZi
	builtinHelp["zi"] = "pick a frecent directory with zoxide"
}

type zoxideState struct {
	mu      sync.Mutex
	pending []string // directories waiting for `zoxide add`
	exe     string
	env     []string
	running bool
}

func setupZoxide(sh *Shell, io *IO, exe string) string {
	in := sh.shared.integr
	in.mu.Lock()
	in.jump = (*Shell).zoxideJump
	in.mu.Unlock()
	if in.once("zoxide") {
		sh.OnDirChanged(func(sh *Shell, dir string) { sh.zoxideAdd(dir) })
	}
	return "z and zi jump with " + exe + "; directories are added as you cd"
}

// zoxidePwd is the directory as zoxide should see it: physical when
// _ZO_RESOLVE_SYMLINKS=1.
func (sh *Shell) zoxidePwd(dir string) string {
	if sh.Get("_ZO_RESOLVE_SYMLINKS") == "1" {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return r
		}
	}
	return dir
}

// zoxideAdd records dir without making the shell wait: additions are
// queued and a single background `zoxide add` handles what piled up.
func (sh *Shell) zoxideAdd(dir string) {
	in := sh.shared.integr
	exe, ok := in.on("zoxide")
	if !ok || sh.privateMode() {
		return
	}
	z := &in.zoxide
	env := sh.Environ()
	z.mu.Lock()
	z.pending = append(z.pending, sh.zoxidePwd(dir))
	z.exe, z.env = exe, env
	start := !z.running
	z.running = true
	z.mu.Unlock()
	if start {
		in.background(z.drain)
	}
}

func (z *zoxideState) drain() {
	for {
		z.mu.Lock()
		dirs, exe, env := z.pending, z.exe, z.env
		z.pending = nil
		if len(dirs) == 0 {
			z.running = false
			z.mu.Unlock()
			return
		}
		z.mu.Unlock()
		c := exec.Command(exe, append([]string{"add", "--"}, dirs...)...)
		c.Env = env
		c.Dir = "/"
		c.Run()
	}
}

// zoxideJump is z after `integrate zoxide` (zoxide's __zoxide_z).
func (sh *Shell) zoxideJump(io *IO, args []string) int {
	exe, _ := sh.shared.integr.on("zoxide")
	a := args[1:]
	switch {
	case len(a) == 0:
		return sh.zoxideCd(io, sh.HomeDir())
	case len(a) == 1 && a[0] == "-":
		target := sh.Get("OLDPWD")
		if len(sh.dirPrev) > 0 {
			target = sh.dirPrev[len(sh.dirPrev)-1]
		}
		if target == "" {
			fmt.Fprintln(io.Err, "z: no previous directory")
			return 1
		}
		return sh.zoxideCd(io, target)
	case len(a) == 1 && integIsDir(sh.absPath(a[0])):
		return sh.zoxideCd(io, a[0])
	case len(a) == 2 && a[0] == "--":
		return sh.zoxideCd(io, a[1])
	}
	qargs := make([]string, 0, len(a)+4)
	qargs = append(qargs, "query", "--exclude", sh.zoxidePwd(sh.dir), "--")
	c := sh.Command(exe, append(qargs, a...)...)
	c.Stderr = integWriter(io.Err)
	out, st := integRun(c)
	if st != 0 {
		return st
	}
	return sh.zoxideCd(io, out)
}

// zoxideCd is zoxide's __zoxide_cd: cd, then print the directory when
// _ZO_ECHO=1.
func (sh *Shell) zoxideCd(io *IO, dir string) int {
	if st := sh.changeDir(dir, io, false); st != 0 {
		return st
	}
	if sh.Get("_ZO_ECHO") == "1" {
		fmt.Fprintln(io.Out, sh.zoxidePwd(sh.dir))
	}
	return 0
}

// bZi picks a directory with `zoxide query --interactive`, which draws on
// the terminal: it gets the terminal's stdin and stderr while we read its
// stdout.
func bZi(sh *Shell, io *IO, args []string) int {
	exe, ok := sh.shared.integr.on("zoxide")
	if !ok {
		return sh.runExternal(args, io, nil, nil)
	}
	c := sh.Command(exe, append([]string{"query", "--interactive", "--"}, args[1:]...)...)
	c.Stdin = integReader(io.In)
	c.Stderr = integWriter(io.Err)
	out, st := integRun(c)
	if st != 0 {
		return st
	}
	return sh.zoxideCd(io, out)
}
