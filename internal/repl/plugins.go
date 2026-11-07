package repl

import (
	"fmt"
	"os"
	"strings"

	"github.com/andrinoff/ash/internal/term"
)

// Plugins send actions whenever they like. The shell applies them on the
// main goroutine at safe points only: before each prompt, and while the
// line editor waits for input (the editor runs our callback on its
// goroutine, which is the main one). A running command is never
// interrupted; what arrives during it waits for the next prompt.

// pluginState is the REPL's side of plugins.
type pluginState struct {
	atPrompt  bool     // reading a command line, no key binding running
	messages  []string // to print before the next prompt
	buffer    string   // to put on the next command line, if hasBuffer
	hasBuffer bool
	start     func() // starts the plugins; nil once that is done
}

// startPlugins connects plugins to the session and starts them: the ones
// the config loaded (held until now, see HoldPlugins) and the ones in
// ~/.config/ash/plugins. On a terminal that happens once the first
// prompt is on the screen, since starting processes would compete with
// drawing it.
func (r *REPL) startPlugins() {
	sh := r.sh
	sh.SetPluginWake(func() { r.ed.Do(r.applyPluginsIdle) })
	r.plug.start = func() {
		sh.ReleasePlugins()
		if !sh.NoConfig {
			sh.AutoloadPlugins()
		}
	}
	if r.tty && term.IsTerminal(int(os.Stdin.Fd())) {
		// The editor runs it after its first render. Its queue is
		// bounded, though, so the loop makes sure once a line is read.
		r.ed.Do(r.startPendingPlugins)
	} else {
		r.startPendingPlugins()
	}
}

// startPendingPlugins starts the plugins if startPlugins left that for
// later and it hasn't happened yet.
func (r *REPL) startPendingPlugins() {
	if start := r.plug.start; start != nil {
		r.plug.start = nil
		start()
	}
}

// applyPlugins applies plugin actions between commands. Messages wait for
// showPluginMessages, so they start on a line of their own.
func (r *REPL) applyPlugins() {
	u := r.sh.ApplyPluginActions()
	r.plug.messages = append(r.plug.messages, u.Messages...)
	if u.HasBuffer {
		r.plug.buffer, r.plug.hasBuffer = u.Buffer, true
	}
}

func (r *REPL) showPluginMessages() {
	for _, m := range r.plug.messages {
		fmt.Fprintln(os.Stderr, m)
	}
	r.plug.messages = nil
}

// takePluginBuffer returns the text a plugin asked to put on the next
// command line.
func (r *REPL) takePluginBuffer() string {
	b := r.plug.buffer
	r.plug.buffer, r.plug.hasBuffer = "", false
	return b
}

// nextBuffer is the text to start the next command line with: what the
// last command asked for through $ASH_ACTION_FILE, or else what a plugin
// did (a plugin's text then waits for a later prompt).
func (r *REPL) nextBuffer() string {
	if b := r.sh.TakeNextBuffer(); b != "" {
		return b
	}
	return r.takePluginBuffer()
}

// applyPluginsIdle applies plugin actions while the editor waits at the
// prompt: messages go above it, the prompt is redrawn when variables or
// the directory changed, and text for the command line goes in now if
// the line is empty (otherwise on the next one). Actions that may run
// shell code run with the terminal handed back, like a key binding's
// command, so that the programs they start find it in its normal mode.
func (r *REPL) applyPluginsIdle() {
	if !r.plug.atPrompt {
		return
	}
	u := r.sh.ApplyPluginActionsIdle(r.ed.Suspend)
	if len(u.Messages) > 0 {
		// one entry in the editor's bounded queue, however many
		r.ed.Notify(strings.Join(u.Messages, "\n"))
	}
	if u.HasBuffer {
		if buf, _ := r.ed.Buffer(); buf == "" {
			r.ed.SetBuffer(u.Buffer, len(u.Buffer))
		} else {
			r.plug.buffer, r.plug.hasBuffer = u.Buffer, true
		}
	}
	if u.DirChanged {
		r.setTitle("")
		r.reportCwd()
	}
	if u.Redraw {
		r.ed.Repaint()
	}
}

// runBinding runs a key binding's command with plugin actions held back,
// then applies what came in meanwhile.
func (r *REPL) runBinding(run func()) {
	idle := r.plug.atPrompt
	r.plug.atPrompt = false
	run()
	r.plug.atPrompt = idle
	r.applyPluginsIdle()
}
