package repl

import (
	"strings"
	"testing"

	"github.com/andrinoff/ash/internal/shell"
)

func TestPromptSegments(t *testing.T) {
	ps := promptState{env: []string{"HOME=/home/me", "ash_plugin_git=stale"}, dir: "/tmp", segments: []shell.PluginSegment{{Name: "git", Text: "main +1"}, {Name: "took", Text: "3s\n"}}}
	p := builtinPrompt(ps, "insert")
	first := strings.SplitN(p.Left, "\n", 2)[0]
	if !strings.Contains(first, " main +1\x1b[0m 3s \x1b[0m") {
		t.Fatalf("segments missing from the prompt's first line: %q", first)
	}
	env := strings.Join(starshipEnv(ps), "\n")
	if !strings.Contains(env, "ash_plugin_git=main +1\n") || strings.Contains(env, "stale") || !strings.Contains(env, "ash_plugin_took=3s\n") {
		t.Fatalf("starship's environment: %q", env)
	}
}
