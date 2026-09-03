package shell

// TUI file managers and git clients that can leave you in a different
// directory when you quit. They all write that directory somewhere; ash
// points them at $ASH_ACTION_FILE and changes to it after they exit (see
// actions.go). The function names follow each tool's own docs.

func init() {
	registerIntegration(integration{
		name:  "yazi",
		exe:   "yazi",
		about: "y: run yazi and cd to where you quit it",
		setup: func(sh *Shell, io *IO, exe string) string {
			sh.defineIntegrationFunc("y", `function y --description 'yazi, then cd to where you quit'
    command yazi --cwd-file="$ASH_ACTION_FILE" $argv
end`)
			return "y runs yazi; quitting with q changes to the directory you were in"
		},
	})
	registerIntegration(integration{
		name:  "lazygit",
		exe:   "lazygit",
		about: "cd to the repository lazygit was in when it quits",
		setup: func(sh *Shell, io *IO, exe string) string {
			sh.SetVar("LAZYGIT_NEW_DIR_FILE", []string{sh.ActionFile()}, setOpts{scope: setGlobal, export: 1})
			return "after lazygit exits, ash changes to the repository you switched to"
		},
	})
	registerIntegration(integration{
		name:  "nnn",
		exe:   "nnn",
		about: "cd to nnn's directory when you quit with ^G",
		setup: func(sh *Shell, io *IO, exe string) string {
			sh.SetVar("NNN_TMPFILE", []string{sh.ActionFile()}, setOpts{scope: setGlobal, export: 1})
			return "quitting nnn with ^G changes to its directory"
		},
	})
	registerIntegration(integration{
		name:  "lf",
		exe:   "lf",
		about: "lfcd: run lf and cd to where you quit it",
		setup: func(sh *Shell, io *IO, exe string) string {
			sh.defineIntegrationFunc("lfcd", `function lfcd --description 'lf, then cd to where you quit'
    command lf -last-dir-path="$ASH_ACTION_FILE" $argv
end`)
			return "lfcd runs lf and changes to the directory you quit in"
		},
	})
	registerIntegration(integration{
		name:  "ranger",
		exe:   "ranger",
		about: "ranger-cd: run ranger and cd to where you quit it",
		setup: func(sh *Shell, io *IO, exe string) string {
			sh.defineIntegrationFunc("ranger-cd", `function ranger-cd --description 'ranger, then cd to where you quit'
    command ranger --choosedir="$ASH_ACTION_FILE" $argv
end`)
			return "ranger-cd runs ranger and changes to the directory you quit in"
		},
	})
	registerIntegration(integration{
		name:  "broot",
		exe:   "broot",
		about: "br: run broot and let it cd the shell",
		setup: func(sh *Shell, io *IO, exe string) string {
			sh.defineIntegrationFunc("br", `function br --description 'broot, and let it change the directory'
    command broot --outcmd "$ASH_ACTION_FILE" $argv
end`)
			return "br runs broot; alt-enter on a directory changes to it"
		},
	})
}

// defineIntegrationFunc defines a fish-syntax function unless the user
// already has one with that name.
func (sh *Shell) defineIntegrationFunc(name, src string) {
	if sh.lookupFunc(name) != nil {
		return
	}
	sh.RunString(src, "integrate.fish", &IO{In: &Stream{}, Out: &Stream{}, Err: &Stream{}})
}
