package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/andrinoff/ash/internal/shell"
)

// interactiveMain is a placeholder REPL until the line editor lands.
func interactiveMain(sh *shell.Shell, initCmds []string) int {
	sh.Init()
	for _, c := range initCmds {
		sh.RunString(c, "", sh.StdIO())
	}
	loadConfig(sh)
	r := bufio.NewReader(os.Stdin)
	for {
		fmt.Fprint(os.Stderr, "ash> ")
		line, err := r.ReadString('\n')
		if err != nil {
			return sh.Status()
		}
		sh.RunTopLevel(line, sh.StdIO())
		sh.ResetControl()
		if exit, st := sh.ShouldExit(); exit {
			return st
		}
	}
}

func loadConfig(sh *shell.Shell) {}
