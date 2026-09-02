package main

import (
	"github.com/andrinoff/ash/internal/repl"
	"github.com/andrinoff/ash/internal/shell"
)

func interactiveMain(sh *shell.Shell, initCmds []string) int {
	return repl.Run(sh, initCmds)
}

func loadConfig(sh *shell.Shell) {
	sh.LoadUniversal()
	repl.LoadConfig(sh)
}
