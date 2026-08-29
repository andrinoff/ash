package shell

import (
	"sync"
	"time"
)

// hooks are callbacks used by the interactive frontend and integrations.
type hooks struct {
	mu        sync.Mutex
	prePrompt []func(*Shell)
	preExec   []func(*Shell, string)
	postExec  []func(*Shell, string, int, time.Duration)
	dirChange []func(*Shell, string)
	prompt    string // prompt provider: "" (built in), "starship", "function"
}

// OnPrePrompt registers f to run before each prompt is drawn.
func (sh *Shell) OnPrePrompt(f func(*Shell)) {
	h := sh.shared.hooks
	h.mu.Lock()
	h.prePrompt = append(h.prePrompt, f)
	h.mu.Unlock()
}

// OnPreExec registers f to run before an interactive command line runs.
func (sh *Shell) OnPreExec(f func(sh *Shell, line string)) {
	h := sh.shared.hooks
	h.mu.Lock()
	h.preExec = append(h.preExec, f)
	h.mu.Unlock()
}

// OnPostExec registers f to run after an interactive command line ran.
func (sh *Shell) OnPostExec(f func(sh *Shell, line string, status int, d time.Duration)) {
	h := sh.shared.hooks
	h.mu.Lock()
	h.postExec = append(h.postExec, f)
	h.mu.Unlock()
}

// OnDirChanged registers f to run after the main shell changes directory.
func (sh *Shell) OnDirChanged(f func(sh *Shell, dir string)) {
	h := sh.shared.hooks
	h.mu.Lock()
	h.dirChange = append(h.dirChange, f)
	h.mu.Unlock()
}

func (sh *Shell) hookList() *hooks { return sh.shared.hooks }

// RunPrePrompt runs the pre-prompt hooks and the prompt event.
func (sh *Shell) RunPrePrompt() {
	h := sh.shared.hooks
	h.mu.Lock()
	fs := append([]func(*Shell){}, h.prePrompt...)
	h.mu.Unlock()
	saved, savedPipe := sh.status, sh.pipestatus
	for _, f := range fs {
		f(sh)
	}
	sh.status, sh.pipestatus = saved, savedPipe
}

// RunPreExec runs the pre-exec hooks for a command line.
func (sh *Shell) RunPreExec(line string) {
	h := sh.shared.hooks
	h.mu.Lock()
	fs := append([]func(*Shell, string){}, h.preExec...)
	h.mu.Unlock()
	for _, f := range fs {
		f(sh, line)
	}
}

// RunPostExec runs the post-exec hooks.
func (sh *Shell) RunPostExec(line string, status int, d time.Duration) {
	h := sh.shared.hooks
	h.mu.Lock()
	fs := append([]func(*Shell, string, int, time.Duration){}, h.postExec...)
	h.mu.Unlock()
	saved, savedPipe := sh.status, sh.pipestatus
	for _, f := range fs {
		f(sh, line, status, d)
	}
	sh.status, sh.pipestatus = saved, savedPipe
}

func (sh *Shell) runDirHooks(dir string) {
	h := sh.shared.hooks
	h.mu.Lock()
	fs := append([]func(*Shell, string){}, h.dirChange...)
	h.mu.Unlock()
	for _, f := range fs {
		f(sh, dir)
	}
}

// SetPromptProvider selects who draws the prompt ("starship", or "" for
// the built-in prompt / user-defined prompt functions).
func (sh *Shell) SetPromptProvider(name string) {
	h := sh.shared.hooks
	h.mu.Lock()
	h.prompt = name
	h.mu.Unlock()
}

// PromptProvider returns the selected prompt provider.
func (sh *Shell) PromptProvider() string {
	h := sh.shared.hooks
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.prompt
}
