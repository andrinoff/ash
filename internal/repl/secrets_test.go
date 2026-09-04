package repl

import (
	"testing"

	"github.com/andrinoff/ash/internal/shell"
)

func TestSecretFilter(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	r := &REPL{sh: shell.New()}
	secret := []string{
		"export GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"curl -H 'Authorization: Bearer sk-ant-api03-abcdefghijklmnopqrstuvwx'",
		"aws configure set aws_access_key_id AKIAABCDEFGHIJKLMNOP",
		"export API_KEY=0123456789abcdef",
		"git clone https://drew:hunter2pass@example.com/repo.git",
		"mysql --password='correcthorsebattery'",
	}
	plain := []string{
		"git status",
		"echo $GITHUB_TOKEN",
		"export API_KEY=$(pass show api)",
		"grep -r token src/",
		"ssh-keygen -t ed25519",
		"open https://example.com/login",
	}
	for _, s := range secret {
		if !r.secret(s) {
			t.Errorf("should be kept out of history: %q", s)
		}
	}
	for _, s := range plain {
		if r.secret(s) {
			t.Errorf("should be kept: %q", s)
		}
	}
}

func TestFixLine(t *testing.T) {
	cases := [][4]string{
		{"gti status", "gti", "git", "git status"},
		{"ls | gerp foo", "gerp", "grep", "ls | grep foo"},
		{"cd Documnets", "Documnets", "Documents", "cd Documents"},
		{"echo gtis; gti", "gti", "git", "echo gtis; git"},
	}
	for _, c := range cases {
		if got := fixLine(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("fixLine(%q) = %q, want %q", c[0], got, c[3])
		}
	}
}
