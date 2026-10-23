package main

import (
	"github.com/andrinoff/ash/internal/repl"
	"github.com/andrinoff/ash/internal/shell"
)

func interactiveMain(sh *shell.Shell, initCmds []string) int {
	return repl.Run(sh, initCmds)
}

// loadConfig reads the config for `ash -ic CMD`.
func loadConfig(sh *shell.Shell) { repl.LoadConfig(sh) }
