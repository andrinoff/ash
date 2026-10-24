package repl

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrinoff/ash/internal/editor"
	"github.com/andrinoff/ash/internal/shell"
	"github.com/andrinoff/ash/internal/term"
)

// promptState is a snapshot of everything a prompt needs, taken on the
// main goroutine so rendering can happen in the background.
type promptState struct {
	env        []string
	dir        string
	status     int
	pipestatus []int
	duration   time.Duration
	jobs       int
	starship   string // path to starship, "" if not used
	session    string
	self       string
}

func (r *REPL) snapshot() promptState {
	sh := r.sh
	ps := promptState{
		env:        sh.Environ(),
		dir:        sh.Dir(),
		status:     sh.Status(),
		pipestatus: append([]int(nil), sh.PipeStatus()...),
		duration:   sh.CmdDuration(),
		jobs:       sh.Jobs(),
		session:    sh.SessionKey(),
		self:       sh.Self(),
	}
	if sh.PromptProvider() == "starship" {
		if p, ok := sh.LookPath("starship"); ok {
			ps.starship = p
		}
	}
	return ps
}

// promptFunc builds the editor's prompt callback. User-defined prompt
// functions run synchronously on the main goroutine; starship runs in the
// background, left and right prompts in parallel.
func (r *REPL) promptFunc() func(keymap string) func() editor.Prompt {
	sh := r.sh
	return func(keymap string) func() editor.Prompt {
		// fish compatibility: prompts can look at $fish_bind_mode
		mode := map[string]string{"vicmd": "default", "insert": "insert", "visual": "visual", "replace": "replace"}[keymap]
		if mode == "" {
			mode = "default"
		}
		if r.ed.Keymap() == "vi" {
			sh.SetString("fish_bind_mode", mode)
		}
		// user prompt functions win; they must run on this goroutine
		for _, names := range [][2]string{{"ash_prompt", "ash_right_prompt"}, {"fish_prompt", "fish_right_prompt"}} {
			if sh.Function(names[0]) != nil {
				left, _ := sh.CaptureFunction(names[0])
				right, _ := sh.CaptureFunction(names[1])
				if mp, ok := sh.CaptureFunction("fish_mode_prompt"); ok && r.ed.Keymap() == "vi" {
					left = mp + left
				}
				p := editor.Prompt{Left: trimPrompt(left), Right: trimPrompt(right)}
				return func() editor.Prompt { return p }
			}
		}
		ps := r.snapshot()
		r.state = ps
		return func() editor.Prompt { return r.renderPrompt(ps, keymap) }
	}
}

func trimPrompt(s string) string {
	return strings.TrimSuffix(s, "\n")
}

// renderPrompt renders starship or the built-in prompt from a snapshot.
// It is safe to call from any goroutine.
func (r *REPL) renderPrompt(ps promptState, keymap string) editor.Prompt {
	if ps.starship != "" {
		if p, ok := r.starship(ps, keymap); ok {
			return p
		}
	}
	return builtinPrompt(ps, keymap)
}

func (r *REPL) starshipArgs(ps promptState, keymap string, extra ...string) []string {
	cols, _ := term.Size(int(os.Stdout.Fd()))
	pipe := make([]string, len(ps.pipestatus))
	for i, s := range ps.pipestatus {
		pipe[i] = strconv.Itoa(s)
	}
	args := append([]string{}, extra...)
	args = append(args,
		"--terminal-width="+strconv.Itoa(cols),
		"--status="+strconv.Itoa(ps.status),
		"--pipestatus="+strings.Join(pipe, " "),
		"--keymap="+keymap,
		"--cmd-duration="+strconv.FormatInt(ps.duration.Milliseconds(), 10),
		"--jobs="+strconv.Itoa(ps.jobs),
	)
	return args
}

func starshipEnv(ps promptState) []string {
	env := make([]string, 0, len(ps.env)+3)
	for _, kv := range ps.env {
		if strings.HasPrefix(kv, "STARSHIP_SHELL=") || strings.HasPrefix(kv, "STARSHIP_SESSION_KEY=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "STARSHIP_SHELL="+ps.self, "STARSHIP_SESSION_KEY="+ps.session)
}

func runStarship(ps promptState, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps.starship, args...)
	cmd.Dir = ps.dir
	cmd.Env = starshipEnv(ps)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	err := cmd.Run()
	return out.String(), err
}

func (r *REPL) starship(ps promptState, keymap string) (editor.Prompt, bool) {
	var wg sync.WaitGroup
	var left, right string
	var errL, errR error
	wg.Add(2)
	go func() {
		defer wg.Done()
		left, errL = runStarship(ps, r.starshipArgs(ps, keymap, "prompt"))
	}()
	go func() {
		defer wg.Done()
		right, errR = runStarship(ps, r.starshipArgs(ps, keymap, "prompt", "--right"))
	}()
	wg.Wait()
	if errL != nil {
		return editor.Prompt{}, false
	}
	if errR != nil {
		right = ""
	}
	return editor.Prompt{Left: left, Right: strings.TrimRight(right, "\n")}, true
}

// continuation returns the prompt for the second and later lines. It is
// computed once per prompt provider. readOpts asks for it before the first
// prompt is rendered, so it can't use r.state, which is still empty then.
func (r *REPL) continuation() string {
	star := ""
	if r.sh.PromptProvider() == "starship" {
		star, _ = r.sh.LookPath("starship")
	}
	if r.contDone && r.contFor == star {
		return r.cont
	}
	r.contDone, r.contFor = true, star
	if star != "" {
		if out, err := runStarship(r.snapshot(), []string{"prompt", "--continuation"}); err == nil {
			r.cont = out
			return r.cont
		}
	}
	r.cont = "\x1b[2m∙\x1b[0m "
	return r.cont
}

// transient renders the prompt kept in scrollback for an accepted line.
func (r *REPL) transient(line string) (editor.Prompt, bool) {
	sh := r.sh
	if !sh.Transient() {
		return editor.Prompt{}, false
	}
	ps := r.state
	args := r.starshipArgs(ps, "insert")
	var leftName, rightName string
	for _, n := range []string{"ash_transient_prompt", "starship_transient_prompt_func"} {
		if sh.Function(n) != nil {
			leftName = n
			break
		}
	}
	for _, n := range []string{"ash_transient_rprompt", "starship_transient_rprompt_func"} {
		if sh.Function(n) != nil {
			rightName = n
			break
		}
	}
	var right string
	var wg sync.WaitGroup
	if rightName != "" {
		fork := sh.Fork()
		wg.Add(1)
		go func() {
			defer wg.Done()
			right, _ = fork.CaptureFunction(rightName, args...)
		}()
	}
	left := "\x1b[1;32m❯\x1b[0m "
	if leftName != "" {
		left, _ = sh.CaptureFunction(leftName, args...)
	}
	wg.Wait()
	return editor.Prompt{Left: trimPrompt(left), Right: trimPrompt(right)}, true
}

// builtinPrompt is ash's own prompt for people without starship: the
// directory, the git branch (read from .git, no subprocess), the exit
// status when it failed, the duration of slow commands, and a chevron.
func builtinPrompt(ps promptState, keymap string) editor.Prompt {
	home := ""
	for _, kv := range ps.env {
		if strings.HasPrefix(kv, "HOME=") {
			home = kv[5:]
			break
		}
	}
	dir := ps.dir
	if home != "" && (dir == home || strings.HasPrefix(dir, home+"/")) {
		dir = "~" + dir[len(home):]
	}
	dir = shortenPath(dir, 3)
	var b strings.Builder
	b.WriteString("\x1b[1;36m" + dir + "\x1b[0m")
	if br := gitBranch(ps.dir); br != "" {
		b.WriteString(" \x1b[35m" + br + "\x1b[0m")
	}
	if ps.duration >= 2*time.Second {
		b.WriteString(" \x1b[33m" + fmtDuration(ps.duration) + "\x1b[0m")
	}
	if ps.jobs > 0 {
		b.WriteString(" \x1b[2m✦" + strconv.Itoa(ps.jobs) + "\x1b[0m")
	}
	b.WriteString("\n")
	color := "\x1b[1;32m"
	if ps.status != 0 {
		color = "\x1b[1;31m"
	}
	sym := "❯"
	if keymap == "vicmd" {
		sym = "❮"
	}
	b.WriteString(color + sym + "\x1b[0m ")
	right := ""
	if ps.status != 0 {
		right = "\x1b[31m" + strconv.Itoa(ps.status) + "\x1b[0m"
	}
	return editor.Prompt{Left: b.String(), Right: right}
}

func shortenPath(p string, keep int) string {
	parts := strings.Split(p, "/")
	if len(parts) <= keep+1 {
		return p
	}
	return "…/" + strings.Join(parts[len(parts)-keep:], "/")
}

func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m" + strconv.Itoa(int(d.Seconds())%60) + "s"
	}
	return strconv.Itoa(int(d.Hours())) + "h" + strconv.Itoa(int(d.Minutes())%60) + "m"
}

// gitBranch finds the current branch by reading .git/HEAD.
func gitBranch(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		g := filepath.Join(d, ".git")
		if st, err := os.Stat(g); err == nil {
			head := filepath.Join(g, "HEAD")
			if !st.IsDir() {
				// worktree or submodule: "gitdir: path"
				b, err := os.ReadFile(g)
				if err != nil {
					return ""
				}
				gd := strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:"))
				if !filepath.IsAbs(gd) {
					gd = filepath.Join(d, gd)
				}
				head = filepath.Join(gd, "HEAD")
			}
			b, err := os.ReadFile(head)
			if err != nil {
				return ""
			}
			s := strings.TrimSpace(string(b))
			if strings.HasPrefix(s, "ref: refs/heads/") {
				return strings.TrimPrefix(s, "ref: refs/heads/")
			}
			if len(s) >= 7 {
				return s[:7]
			}
			return s
		}
		if d == "/" || d == "." {
			return ""
		}
	}
}

var _ = shell.Version
