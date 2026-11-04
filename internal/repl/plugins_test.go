package repl

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/plugin/plugintest"
	"github.com/andrinoff/ash/internal/shell"
)

// The test binary doubles as the plugin (see plugintest).
func TestPluginHelperProcess(t *testing.T) { plugintest.Main() }

// pluginREPL is a REPL with an editor that isn't on a terminal, and the
// fake plugin loaded.
func pluginREPL(t *testing.T) *REPL {
	t.Helper()
	home := t.TempDir()
	t.Chdir(home)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")
	t.Setenv(plugintest.EnvVar, "basic")
	sh := shell.New()
	sh.Init()
	t.Cleanup(sh.ClosePlugins)
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { null.Close() })
	r := &REPL{sh: sh}
	r.ed = editor.New(null, null, editor.Config{})
	path, args := plugintest.Command()
	r.run(t, "plugin load -w basic "+shell.Quote(path)+" "+shell.Quote(args[1]))
	// the plugin echoes the prompt event it gets with its hello
	deadline := time.Now().Add(5 * time.Second)
	for {
		u := sh.ApplyPluginActions()
		if len(u.Messages) > 0 && strings.Contains(u.Messages[0], `"event":"prompt"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no prompt event")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return r
}

func (r *REPL) run(t *testing.T, src string) {
	t.Helper()
	null, _ := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	defer null.Close()
	io := &shell.IO{In: &shell.Stream{File: null, Inherited: true}, Out: &shell.Stream{File: null}, Err: &shell.Stream{File: null}}
	if st := r.sh.RunString(src, "", io); st != 0 {
		t.Fatalf("%s: status %d", src, st)
	}
}

func TestPluginsBetweenCommands(t *testing.T) {
	r := pluginREPL(t)
	r.run(t, "act message 'one'; act buffer 'git push'; act segment git main; act message 'two'")
	r.applyPlugins()
	if got := strings.Join(r.plug.messages, "|"); got != "one|two" {
		t.Fatalf("messages: %q", got)
	}
	if r.sh.Get("ash_plugin_git") != "main" {
		t.Fatal("the segment wasn't applied")
	}
	r.showPluginMessages()
	if r.plug.messages != nil {
		t.Fatal("messages are shown once")
	}
	if o := r.readOpts(); o.Initial != "git push" {
		t.Fatalf("the next command line starts with %q", o.Initial)
	}
	if o := r.readOpts(); o.Initial != "" {
		t.Fatalf("only the next one: %q", o.Initial)
	}
}

func TestPluginsAtThePrompt(t *testing.T) {
	r := pluginREPL(t)
	// not at the prompt (a command is running): nothing happens
	r.run(t, "act buffer 'make test'")
	r.applyPluginsIdle()
	if buf, _ := r.ed.Buffer(); buf != "" {
		t.Fatalf("applied while a command runs: %q", buf)
	}
	// at the prompt, an empty line gets the text right away
	r.plug.atPrompt = true
	r.applyPluginsIdle()
	if buf, _ := r.ed.Buffer(); buf != "make test" {
		t.Fatalf("buffer: %q", buf)
	}
	// a line being typed is left alone; the text waits for the next one
	r.run(t, "act buffer 'git push'")
	r.applyPluginsIdle()
	if buf, _ := r.ed.Buffer(); buf != "make test" {
		t.Fatalf("buffer: %q", buf)
	}
	if r.takePluginBuffer() != "git push" {
		t.Fatal("the text wasn't kept for the next prompt")
	}
	// a key binding's command holds actions back until it's done
	r.runBinding(func() {
		r.run(t, "act segment took 3s")
		r.applyPluginsIdle()
		if r.sh.IsSet("ash_plugin_took") {
			t.Fatal("applied while a binding runs")
		}
	})
	if r.sh.Get("ash_plugin_took") != "3s" {
		t.Fatal("not applied after the binding")
	}
}
