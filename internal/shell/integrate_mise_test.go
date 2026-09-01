package shell

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// miseDeactivate is what `mise deactivate` prints for bash (mise
// 2026.10), followed by the PATH it restores.
const miseDeactivate = `# shellcheck shell=bash
if [[ "$(declare -p PROMPT_COMMAND 2>/dev/null)" == "declare -a"* ]]; then
	_mise_prompt_command=()
	for _mise_pc in ${PROMPT_COMMAND[@]+"${PROMPT_COMMAND[@]}"}; do
		if [[ $_mise_pc != "_mise_hook_prompt_command" && $_mise_pc != "_mise_hook" ]]; then
			_mise_prompt_command+=("$_mise_pc")
		fi
	done
	PROMPT_COMMAND=(${_mise_prompt_command[@]+"${_mise_prompt_command[@]}"})
	unset _mise_prompt_command _mise_pc
elif [[ ${PROMPT_COMMAND-} == *_mise_hook_prompt_command* ]]; then
	_mise_prompt_command_value="${PROMPT_COMMAND-}"
	_mise_prompt_command_value="${_mise_prompt_command_value//_mise_hook_prompt_command;/}"
	_mise_prompt_command_value="${_mise_prompt_command_value//;_mise_hook_prompt_command/}"
	_mise_prompt_command_value="${_mise_prompt_command_value//_mise_hook_prompt_command/}"
	printf -v PROMPT_COMMAND '%s' "$_mise_prompt_command_value"
	unset _mise_prompt_command_value
elif [[ ${PROMPT_COMMAND-} == *_mise_hook* ]]; then
	_mise_prompt_command_value="${PROMPT_COMMAND-}"
	_mise_prompt_command_value="${_mise_prompt_command_value//_mise_hook;/}"
	_mise_prompt_command_value="${_mise_prompt_command_value//;_mise_hook/}"
	_mise_prompt_command_value="${_mise_prompt_command_value//_mise_hook/}"
	printf -v PROMPT_COMMAND '%s' "$_mise_prompt_command_value"
	unset _mise_prompt_command_value
fi

if declare -p chpwd_functions >/dev/null 2>&1; then
	_mise_chpwd_functions=()
	for _mise_f in ${chpwd_functions[@]+"${chpwd_functions[@]}"}; do
		if [[ $_mise_f != "_mise_hook_chpwd" && $_mise_f != "_mise_hook" ]]; then
			_mise_chpwd_functions+=("$_mise_f")
		fi
	done
	chpwd_functions=(${_mise_chpwd_functions[@]+"${_mise_chpwd_functions[@]}"})
	unset _mise_chpwd_functions _mise_f
fi

declare -F _mise_hook_prompt_command >/dev/null && unset -f _mise_hook_prompt_command
declare -F _mise_add_prompt_command >/dev/null && unset -f _mise_add_prompt_command
declare -F _mise_hook_chpwd >/dev/null && unset -f _mise_hook_chpwd
declare -F _mise_hook >/dev/null && unset -f _mise_hook
if [ -n "${_mise_cmd_not_found_handle:-}" ]; then
	eval "$_mise_cmd_not_found_handle"
	unset _mise_cmd_not_found_handle
	declare -F _command_not_found_handle >/dev/null && unset -f _command_not_found_handle
elif [[ "$(declare -f command_not_found_handle 2>/dev/null)" == *"hook-not-found"* ]]; then
	declare -F command_not_found_handle >/dev/null && unset -f command_not_found_handle
fi
declare -F mise >/dev/null && unset -f mise
unset MISE_SHELL
unset __MISE_DIFF
unset __MISE_SESSION
unset __MISE_EXE
unset __MISE_FLAGS
unset __MISE_HOOK_ENABLED
unset __MISE_BASH_CHPWD_RAN
unset __MISE_BASH_SKIP_FIRST_PROMPT
unset _mise_cmd_not_found
export PATH='/usr/bin:/bin'
`

func miseTest(t *testing.T) *integTest {
	t.Chdir(t.TempDir())
	it := newIntegTest(t)
	it.tool("mise", `case "$1" in
hook-env) cat "$LOG.hookenv" 2>/dev/null ;;
shell) printf "export MISE_NODE_VERSION='%s'\n" "${2#node@}" ;;
deactivate) cat "$LOG.deactivate" ;;
*) echo "ran mise $*" ;;
esac`)
	it.write("log.deactivate", miseDeactivate)
	return it
}

func TestMiseActivate(t *testing.T) {
	it := miseTest(t)
	path := it.sh.Get("PATH")
	it.write("log.hookenv", "export MISE_TEST='a b'\nexport PATH='/mise/bin:"+path+"'\nunset NOT_SET\nfalse\n")
	it.run("false")
	it.mustRun("integrate mise")

	hookEnv := "mise [hook-env] [--shell-pid] [" + strconv.Itoa(os.Getpid()) + "] [-s] [bash]"
	if calls := it.calls(); !equalLines(calls, []string{hookEnv + " [--force]"}) {
		t.Fatalf("calls %q", calls)
	}
	for name, want := range map[string]string{
		"MISE_SHELL":       "bash",
		"__MISE_EXE":       it.bin + "/mise",
		"__MISE_ORIG_PATH": path,
		"MISE_TEST":        "a b",
		"PATH":             "/mise/bin:" + path,
	} {
		if got := it.sh.Get(name); got != want {
			t.Errorf("%s=%q, want %q", name, got, want)
		}
	}
	env := strings.Join(it.sh.Environ(), "\n")
	for _, kv := range []string{"MISE_SHELL=bash", "__MISE_ORIG_PATH=" + path, "MISE_TEST=a b"} {
		if !strings.Contains(env, kv) {
			t.Errorf("%s not exported", kv)
		}
	}
	if it.sh.CommandKind("mise") != "function" {
		t.Errorf("mise is a %q, want the wrapper function", it.sh.CommandKind("mise"))
	}

	// Like the bash hooks: activation covers the first prompt, a cd runs
	// the hook itself and covers the next prompt.
	steps := []struct {
		name string
		do   func()
		want []string
	}{
		{"first prompt", it.sh.RunPrePrompt, nil},
		{"prompt", it.sh.RunPrePrompt, []string{hookEnv + " [--reason] [precmd]"}},
		{"cd", func() { it.mustRun("cd /") }, []string{hookEnv + " [--reason] [chpwd]"}},
		{"prompt after cd", it.sh.RunPrePrompt, nil},
		{"next prompt", it.sh.RunPrePrompt, []string{hookEnv + " [--reason] [precmd]"}},
	}
	for _, s := range steps {
		s.do()
		if calls := it.calls(); !equalLines(calls, s.want) {
			t.Errorf("%s: calls %q, want %q", s.name, calls, s.want)
		}
	}
	// Re-activation keeps the original PATH and adds no hooks.
	it.mustRun("integrate mise")
	it.calls()
	if got := it.sh.Get("__MISE_ORIG_PATH"); got != path {
		t.Errorf("__MISE_ORIG_PATH=%q after re-activation", got)
	}
	if n := len(it.sh.shared.hooks.dirChange); n != 1 {
		t.Errorf("%d directory hooks", n)
	}
}

func TestMiseWrapper(t *testing.T) {
	it := miseTest(t)
	it.mustRun("integrate mise")
	it.calls()
	cases := []struct {
		src, out, node string
		calls          []string
	}{
		{"mise shell node@20", "", "20", []string{"mise [shell] [node@20]"}},
		{"mise --version", "ran mise --version\n", "20", []string{"mise [--version]"}},
		{"mise", "ran mise \n", "20", []string{"mise"}},
		// Help is shown, not run.
		{"mise shell --help", "export MISE_NODE_VERSION='--help'\n", "20", []string{"mise [shell] [--help]"}},
		{"mise sh -h node@18", "ran mise sh -h node@18\n", "20", []string{"mise [sh] [-h] [node@18]"}},
	}
	for _, c := range cases {
		out := it.mustRun(c.src)
		if out != c.out || it.sh.Get("MISE_NODE_VERSION") != c.node {
			t.Errorf("%s: out %q MISE_NODE_VERSION=%q", c.src, out, it.sh.Get("MISE_NODE_VERSION"))
		}
		if calls := it.calls(); !equalLines(calls, c.calls) {
			t.Errorf("%s: calls %q, want %q", c.src, calls, c.calls)
		}
	}
}

func TestMiseDeactivate(t *testing.T) {
	it := miseTest(t)
	it.mustRun("integrate mise")
	it.calls()
	_, errs, _ := it.run("mise deactivate")
	if errs != "" {
		t.Errorf("deactivate printed %q", errs)
	}
	if it.sh.IsSet("MISE_SHELL") || it.sh.Get("PATH") != "/usr/bin:/bin" {
		t.Errorf("after deactivate: MISE_SHELL=%q PATH=%q", it.sh.Get("MISE_SHELL"), it.sh.Get("PATH"))
	}
	if it.sh.CommandKind("mise") == "function" {
		t.Error("the mise function is still defined")
	}
	it.export("PATH", it.bin+":/usr/bin:/bin")
	it.calls()
	it.sh.RunPrePrompt()
	it.sh.RunPrePrompt()
	it.mustRun("cd /")
	if calls := it.calls(); calls != nil {
		t.Errorf("hooks still run after deactivate: %q", calls)
	}
}

func TestRealMise(t *testing.T) {
	t.Chdir(t.TempDir())
	it := newIntegTest(t)
	it.realTool("mise")
	for _, d := range []string{"DATA", "CACHE", "STATE", "CONFIG"} {
		it.export("MISE_"+d+"_DIR", it.mkdir("mise/"+d))
	}
	it.export("MISE_GLOBAL_CONFIG_FILE", it.home+"/mise/CONFIG/config.toml")
	proj := it.mkdir("proj")
	it.export("MISE_TRUSTED_CONFIG_PATHS", proj)
	it.write("proj/mise.toml", "[env]\nASH_MISE_TEST = \"it's \\\"x\\\" y\"\n")
	it.mustRun("integrate mise")
	if _, errs, st := it.run("cd " + proj); st != 0 {
		t.Fatalf("cd: %s", errs)
	}
	if got, want := it.sh.Get("ASH_MISE_TEST"), `it's "x" y`; got != want {
		t.Errorf("after cd in: ASH_MISE_TEST=%q, want %q", got, want)
	}
	it.mustRun("cd ..")
	if it.sh.IsSet("ASH_MISE_TEST") {
		t.Errorf("after cd out: ASH_MISE_TEST=%q", it.sh.Get("ASH_MISE_TEST"))
	}
	// Everything hook-env printed ran without complaint.
	if errs := it.file("stderr"); errs != "" {
		t.Errorf("stderr: %s", errs)
	}
}
