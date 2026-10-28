// ash is an interactive shell that speaks both sh and fish.
package main

import (
	"embed"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/andrinoff/ash/internal/shell"
	"github.com/andrinoff/ash/internal/term"
)

const usage = `usage: ash [options] [script [args...]]

  -c, --command CMD     run CMD and exit
  -i, --interactive     force interactive mode
  -l, --login           act as a login shell
  -N, --no-config       don't read config files
  -C, --init-command C  run C before the config
  -n, --no-execute      only check the syntax of a script
      --profile         time each statement of the config (or script)
                        and print a report to stderr
  -v, --version         print the version
  -h, --help            show this help
`

//go:embed docs/*.md
var docs embed.FS

// started is as close to the start of the process as Go code gets.
var started = time.Now()

// profileStart is where --profile measures from: when the process started
// if the system can say, else started. The kernel's time is when the
// process forked, so it is only used when the exec followed right away,
// not for a shell that ran `exec ash`.
func profileStart() time.Time {
	if t, ok := processStart(); ok && !t.After(started) && started.Sub(t) < time.Second {
		return t
	}
	return started
}

func main() {
	shell.Docs = docs
	os.Exit(run())
}

func run() int {
	args := os.Args[1:]
	// A login shell's argv[0] starts with "-": "-ash" from login(1), or
	// "-/path/to/ash" from `exec -l` (how some terminals start it).
	var (
		command     string
		hasCommand  bool
		interactive bool
		login       = strings.HasPrefix(os.Args[0], "-")
		noConfig    bool
		noExec      bool
		readStdin   bool // -s: commands come from stdin, all args are $1...
		initCmds    []string
		profile     bool
	)
	// ASH_PROFILE=1 is --profile for when you can't pass flags (a
	// terminal's shell setting). It is taken out of the environment so the
	// programs and shells this one starts don't print reports too.
	if v, ok := os.LookupEnv("ASH_PROFILE"); ok {
		profile = v != "" && v != "0"
		os.Unsetenv("ASH_PROFILE")
	}
	for len(args) > 0 {
		a := args[0]
		if a == "--" {
			args = args[1:]
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		args = args[1:]
		switch a {
		case "-c", "--command":
			if len(args) == 0 {
				fmt.Fprintln(os.Stderr, "ash: -c needs an argument")
				return 2
			}
			command, hasCommand = args[0], true
			args = args[1:]
		case "-i", "--interactive":
			interactive = true
		case "-l", "--login":
			login = true
		case "-N", "--no-config":
			noConfig = true
		case "-n", "--no-execute":
			noExec = true
		case "--profile":
			profile = true
		case "-C", "--init-command":
			if len(args) > 0 {
				initCmds = append(initCmds, args[0])
				args = args[1:]
			}
		case "-v", "--version":
			fmt.Printf("ash, version %s\n", shell.Version)
			return 0
		case "-h", "--help":
			fmt.Print(usage)
			return 0
		case "-s", "--stdin":
			readStdin = true
		case "-P", "--private":
		default:
			// combined short flags such as -lc or -ic
			if !strings.HasPrefix(a, "--") && len(a) > 2 {
				ok := true
				for _, c := range a[1:] {
					switch c {
					case 'l':
						login = true
					case 'i':
						interactive = true
					case 'N':
						noConfig = true
					case 'n':
						noExec = true
					case 's':
						readStdin = true
					case 'c':
						if len(args) == 0 {
							fmt.Fprintln(os.Stderr, "ash: -c needs an argument")
							return 2
						}
						command, hasCommand = args[0], true
						args = args[1:]
					default:
						ok = false
					}
				}
				if ok {
					continue
				}
			}
			if strings.HasPrefix(a, "--init-command=") {
				initCmds = append(initCmds, strings.TrimPrefix(a, "--init-command="))
				continue
			}
			if strings.HasPrefix(a, "--command=") {
				command, hasCommand = strings.TrimPrefix(a, "--command="), true
				continue
			}
			fmt.Fprintf(os.Stderr, "ash: unknown option %s\n%s", a, usage)
			return 2
		}
	}

	sh := shell.New()
	if profile {
		sh.StartProfile(profileStart())
	}
	sh.Login = login
	if login {
		sh.LoginSetup()
	}
	sh.NoConfig = noConfig
	if readStdin && len(args) > 0 && args[0] == "-" {
		args = args[1:]
	}
	stdinTTY := term.IsTerminal(int(os.Stdin.Fd()))
	if !hasCommand && len(args) == 0 && stdinTTY {
		interactive = true
	}
	sh.Interactive = interactive

	switch {
	case hasCommand:
		if len(args) > 0 {
			sh.SetArg0(args[0])
			sh.SetArgs(args[1:])
		}
		sh.Init()
		if interactive {
			// set up like the interactive shell (repl.Run) would
			sh.LoadUniversal()
		}
		for _, c := range initCmds {
			sh.RunString(c, "", sh.StdIO())
		}
		if interactive && !noConfig {
			loadConfig(sh)
		}
		if noExec {
			return checkSyntax(command, "")
		}
		sh.ProfileAs("-c")
		status := sh.RunString(command, "", sh.StdIO())
		return finish(sh, status)
	case len(args) > 0 && args[0] != "-" && !readStdin:
		script := args[0]
		sh.SetArg0(script)
		sh.SetArgs(args[1:])
		sh.Init()
		for _, c := range initCmds {
			sh.RunString(c, "", sh.StdIO())
		}
		if noExec {
			b, err := os.ReadFile(script)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ash: %v\n", err)
				return 127
			}
			return checkSyntax(string(b), script)
		}
		status := sh.RunFile(script, nil, sh.StdIO())
		return finish(sh, status)
	case interactive:
		if readStdin {
			sh.SetArgs(args)
		}
		return interactiveMain(sh, initCmds)
	default:
		// Commands on stdin (e.g. starship's custom modules, `curl | ash`,
		// `curl | ash -s -- args`).
		if readStdin {
			sh.SetArgs(args)
		} else if len(args) > 1 {
			sh.SetArgs(args[1:])
		}
		sh.Init()
		for _, c := range initCmds {
			sh.RunString(c, "", sh.StdIO())
		}
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ash: %v\n", err)
			return 1
		}
		if noExec {
			return checkSyntax(string(b), "")
		}
		status := sh.RunString(string(b), "stdin", sh.StdIO())
		return finish(sh, status)
	}
}

func checkSyntax(src, file string) int {
	if err := shell.CheckSyntax(src, file); err != nil {
		fmt.Fprint(os.Stderr, "ash: "+shell.FormatParseError(src, file, err))
		return 2
	}
	return 0
}

func finish(sh *shell.Shell, status int) int {
	if exit, st := sh.ShouldExit(); exit {
		status = st
	}
	sh.RunExitTrap()
	if exit, st := sh.ShouldExit(); exit {
		status = st
	}
	if p := sh.StopProfile(); p != nil {
		p.WriteReport(os.Stderr, "exit", time.Since(p.Start))
	}
	return status
}
