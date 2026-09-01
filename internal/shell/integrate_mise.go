package shell

// mise, set up the way `mise activate bash` does it, natively: ash speaks
// bash, so mise is told the shell is bash and its hook-env output (export
// and unset lines) is run as is.

import (
	"bytes"
	"os"
	"strconv"
)

func init() {
	registerIntegration(integration{"mise", "mise", "activate mise tools and environment", setupMise})
}

type miseState struct {
	exe       string
	skipFirst bool // activation already ran hook-env for the first prompt
	chpwdRan  bool // a cd ran hook-env since the last prompt
}

func setupMise(sh *Shell, io *IO, exe string) string {
	in := sh.shared.integr
	m := &in.mise
	m.exe = exe
	global := setOpts{scope: setGlobal, export: 1}
	sh.SetVar("__MISE_EXE", []string{exe}, global)
	sh.SetVar("MISE_SHELL", []string{"bash"}, global)
	// Save the PATH from before activation; re-activation keeps it.
	if sh.Get("__MISE_ORIG_PATH") == "" {
		sh.SetVar("__MISE_ORIG_PATH", append([]string(nil), sh.GetList("PATH")...), global)
	}
	// `mise shell` and `mise deactivate` print code for the shell to run.
	q := Quote(exe)
	sh.integrationEval(`mise() {
	if [ "$#" = 0 ]; then
		command `+q+`
		return
	fi
	case "$1" in
	deactivate | shell | sh)
		case " $* " in
		*" --help "* | *" -h "*) ;;
		*)
			eval "$(command `+q+` "$@")"
			return $?
			;;
		esac
		;;
	esac
	command `+q+` "$@"
}`, "mise activate")
	if in.once("mise") {
		sh.OnPrePrompt(func(sh *Shell) { sh.misePrompt() })
		sh.OnDirChanged(func(sh *Shell, dir string) { sh.miseChdir() })
	}
	// Apply the environment now, so the rest of the config sees it, and
	// let the first prompt skip the hook.
	m.skipFirst = true
	sh.miseHookEnv("", "--force")
	return "hook-env runs before each prompt and after cd"
}

// miseActive reports whether the hooks should run: integrated and not
// switched off by `mise deactivate` (which unsets MISE_SHELL).
func (sh *Shell) miseActive() bool {
	_, ok := sh.shared.integr.on("mise")
	return ok && sh.Get("MISE_SHELL") != ""
}

// misePrompt is _mise_hook_prompt_command.
func (sh *Shell) misePrompt() {
	if !sh.miseActive() {
		return
	}
	m := &sh.shared.integr.mise
	if m.chpwdRan {
		m.chpwdRan, m.skipFirst = false, false
		return
	}
	if m.skipFirst {
		m.skipFirst = false
		return
	}
	sh.miseHookEnv("precmd")
}

// miseChdir is _mise_hook_chpwd: the new directory's tools and variables
// apply right away, so `cd project; node` on one line works.
func (sh *Shell) miseChdir() {
	if !sh.miseActive() {
		return
	}
	sh.shared.integr.mise.chpwdRan = true
	sh.miseHookEnv("chpwd")
}

// miseHookEnv runs `mise hook-env` and applies what it prints.
func (sh *Shell) miseHookEnv(reason string, extra ...string) {
	m := &sh.shared.integr.mise
	args := make([]string, 0, 8+len(extra))
	args = append(args, "hook-env", "--shell-pid", strconv.Itoa(os.Getpid()), "-s", "bash")
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	args = append(args, extra...)
	c := sh.Command(m.exe, args...)
	c.Stderr = sh.Stderr
	var out bytes.Buffer
	c.Stdout = &out
	c.Run()
	sh.integrationEval(out.String(), "mise hook-env")
}
