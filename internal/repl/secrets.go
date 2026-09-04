package repl

import (
	"regexp"
	"strings"
)

// Commands that look like they carry credentials are not written to
// history. The patterns are the shapes real tokens have, so ordinary
// commands that merely mention "token" or "password" are kept.
var secretPatterns = regexp.MustCompile(strings.Join([]string{
	`AKIA[0-9A-Z]{16}`,                                                 // AWS access key
	`\bgh[pousr]_[A-Za-z0-9]{30,}`,                                     // GitHub tokens
	`\bgithub_pat_[A-Za-z0-9_]{30,}`,                                   // GitHub fine-grained token
	`\bglpat-[A-Za-z0-9_-]{20,}`,                                       // GitLab
	`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}`,                           // OpenAI, Anthropic
	`\bxox[abprs]-[A-Za-z0-9-]{10,}`,                                   // Slack
	`\b[rs]k_live_[0-9A-Za-z]{20,}`,                                    // Stripe
	`\bAIza[0-9A-Za-z_-]{35}`,                                          // Google API key
	`\bnpm_[A-Za-z0-9]{36}`,                                            // npm
	`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`, // JWT
	`-----BEGIN [A-Z ]*PRIVATE KEY-----`,
	`(?i)\b[a-z][a-z0-9+.-]*://[^\s/:@]+:[^\s/@]{3,}@`, // URL with a password
	`(?i)(?:api[_-]?key|secret|token|passw(?:or)?d|auth)[A-Za-z_]*\s*[=:]\s*['"]?[^\s'"$]{8,}`, // NAME=value
}, "|"))

// secret reports whether a command line should be kept out of history.
// $ash_history_ignore adds regular expressions; setting $ash_history_secrets
// to 0 turns the built-in patterns off.
func (r *REPL) secret(line string) bool {
	if r.sh.Get("ash_history_secrets") != "0" && secretPatterns.MatchString(line) {
		return true
	}
	for _, p := range r.sh.GetList("ash_history_ignore") {
		if re, err := regexp.Compile(p); err == nil && re.MatchString(line) {
			return true
		}
	}
	return false
}

// fixLine rewrites line with the first whole-word occurrence of bad
// replaced by good.
func fixLine(line, bad, good string) string {
	isSep := func(c byte) bool { return strings.IndexByte(" \t\n;|&()", c) >= 0 }
	for i := 0; i+len(bad) <= len(line); i++ {
		if line[i:i+len(bad)] != bad {
			continue
		}
		if i > 0 && !isSep(line[i-1]) {
			continue
		}
		if j := i + len(bad); j < len(line) && !isSep(line[j]) {
			continue
		}
		return line[:i] + good + line[i+len(bad):]
	}
	return ""
}
