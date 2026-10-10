package shell

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andrinoff/ash/internal/syntax"
)

func bSource(sh *Shell, io *IO, args []string) int {
	var src, name string
	if len(args) < 2 || args[1] == "-" {
		if len(args) < 2 && io.In.TTY {
			fmt.Fprintln(io.Err, "source: missing file name")
			return 2
		}
		b, err := readAll(io.In)
		if err != nil {
			fmt.Fprintf(io.Err, "source: %v\n", err)
			return 1
		}
		src, name = string(b), "-"
	} else {
		name = args[1]
		path := name
		if !strings.ContainsRune(path, '/') {
			if _, err := os.Stat(sh.absPath(path)); err != nil {
				for _, d := range sh.GetList("PATH") {
					p := filepath.Join(d, path)
					if st, err := os.Stat(p); err == nil && !st.IsDir() {
						path = p
						break
					}
				}
			}
		}
		if s := sh.devStream(path, io); s != nil {
			b, err := readAll(s)
			if err != nil {
				fmt.Fprintf(io.Err, "source: %v\n", err)
				return 1
			}
			src = string(b)
		} else {
			b, err := os.ReadFile(sh.absPath(path))
			if err != nil {
				fmt.Fprintf(io.Err, "source: %s\n", errText(err))
				return 1
			}
			src = string(b)
		}
	}
	file := name
	if file == "-" || strings.HasPrefix(file, "/dev/fd/") {
		// Piped input: guess fish syntax from what it looks like.
		if looksLikeFish(src) {
			file = "stdin.fish"
		} else {
			file = "stdin"
		}
	}
	f, err := parse(src, file)
	if err != nil {
		fmt.Fprint(io.Err, "source: "+FormatParseError(src, name, err))
		return 2
	}
	sh.srcStack = append(sh.srcStack, name)
	// Arguments become $argv for the file; afterwards the scope gets
	// back exactly what it had (nothing, if argv lived elsewhere).
	argScope := sh.scope
	var saved *Var
	if len(args) > 2 {
		saved = argScope.get("argv")
		argScope.put("argv", &Var{Vals: args[2:]})
	}
	prev := sh.curSrc
	sh.curSrc = src
	var status int
	if strings.HasSuffix(file, ".fish") {
		status = sh.inBlockScope(func() int { return sh.runStmts(f.Stmts, io) })
	} else {
		status = sh.runStmts(f.Stmts, io)
	}
	sh.curSrc = prev
	if len(args) > 2 {
		argScope.put("argv", saved)
	}
	sh.srcStack = sh.srcStack[:len(sh.srcStack)-1]
	if sh.ctrl == ctrlReturn {
		sh.ctrl = ctrlNone
		status = sh.status
	}
	return status
}

// looksLikeFish guesses whether piped source is fish (so `foo init fish
// | source` gets fish quoting rules).
func looksLikeFish(src string) bool {
	for _, kw := range []string{"\nend\n", "\nend", "set -g", "set -l", "set -x", "set -gx", "set -U", "function ", "; end"} {
		if strings.Contains(src, kw) {
			return true
		}
	}
	return false
}

func readAll(r io.Reader) ([]byte, error) {
	var b strings.Builder
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err == io.EOF {
			return []byte(b.String()), nil
		}
		if err != nil {
			return []byte(b.String()), err
		}
	}
}

// ExecHook runs before `exec` replaces the process (restore terminal,
// save history...).
var ExecHook func()

func bExec(sh *Shell, io *IO, args []string) int {
	if len(args) < 2 {
		// redirections only: apply them to the shell itself
		return sh.execRedirs(io)
	}
	if sh.subshell {
		st := sh.runExternal(args[1:], io, nil, nil)
		sh.ctrl = ctrlExit
		sh.exitStatus = st
		return st
	}
	path, err := sh.lookPath(args[1])
	if err != nil {
		sh.errorMsg(io.Err, "exec: %s: command not found", args[1])
		return 127
	}
	if ExecHook != nil {
		ExecHook()
	}
	err = sh.execProgram(path, args[1:], io)
	sh.errorMsg(io.Err, "exec: %s: %s", args[1], errText(err))
	return 126
}

func bAlias(sh *Shell, io *IO, args []string) int {
	if len(args) == 1 {
		names := make([]string, 0, len(sh.aliases))
		for n := range sh.aliases {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(io.Out, "alias %s %s\n", n, Quote(sh.aliases[n]))
		}
		return 0
	}
	a := args[1:]
	if a[0] == "-s" || a[0] == "--save" {
		a = a[1:]
	}
	if len(a) == 0 {
		return 0
	}
	// bash: alias name=value [name=value...]; fish: alias name value...
	if i := strings.IndexByte(a[0], '='); i > 0 {
		status := 0
		for _, kv := range a {
			j := strings.IndexByte(kv, '=')
			if j <= 0 {
				if v, ok := sh.aliases[kv]; ok {
					fmt.Fprintf(io.Out, "alias %s %s\n", kv, Quote(v))
				} else {
					fmt.Fprintf(io.Err, "alias: %s: not found\n", kv)
					status = 1
				}
				continue
			}
			sh.setAlias(kv[:j], kv[j+1:], false)
		}
		return status
	}
	if len(a) == 1 {
		if v, ok := sh.aliases[a[0]]; ok {
			fmt.Fprintf(io.Out, "alias %s %s\n", a[0], Quote(v))
			return 0
		}
		fmt.Fprintf(io.Err, "alias: %s: not found\n", a[0])
		return 1
	}
	sh.setAlias(a[0], strings.Join(a[1:], " "), false)
	return 0
}

func bUnalias(sh *Shell, io *IO, args []string) int {
	status := 0
	for _, n := range args[1:] {
		if n == "-a" {
			for k := range sh.aliases {
				sh.setAlias(k, "", true)
			}
			continue
		}
		if _, ok := sh.aliases[n]; !ok {
			fmt.Fprintf(io.Err, "unalias: %s: not found\n", n)
			status = 1
			continue
		}
		sh.setAlias(n, "", true)
	}
	return status
}

func bType(sh *Shell, io *IO, args []string) int {
	quiet, all, typeOnly, pathOnly, forcePath, noFuncs := false, false, false, false, false, false
	var names []string
	for _, a := range args[1:] {
		switch a {
		case "-q", "--query":
			quiet = true
		case "-a", "--all":
			all = true
		case "-t", "--type":
			typeOnly = true
		case "-p", "--path":
			pathOnly = true
		case "-P", "--force-path":
			forcePath = true
		case "-f", "--no-functions":
			noFuncs = true
		default:
			if strings.HasPrefix(a, "-") && len(a) > 1 && names == nil {
				// combined flags like -qa
				ok := true
				for _, c := range a[1:] {
					switch c {
					case 'q':
						quiet = true
					case 'a':
						all = true
					case 't':
						typeOnly = true
					case 'p':
						pathOnly = true
					case 'P':
						forcePath = true
					case 'f':
						noFuncs = true
					default:
						ok = false
					}
				}
				if ok {
					continue
				}
			}
			names = append(names, a)
		}
	}
	status := 0
	for _, name := range names {
		found := false
		report := func(kind, desc string) bool {
			found = true
			if quiet {
				return false
			}
			switch {
			case typeOnly:
				fmt.Fprintln(io.Out, kind)
			case pathOnly || forcePath:
				if kind == "file" {
					fmt.Fprintln(io.Out, desc)
				}
			default:
				fmt.Fprintln(io.Out, describeCmd(name, kind, desc))
			}
			return all
		}
		cont := true
		if !forcePath {
			if v, ok := sh.aliases[name]; ok && cont {
				cont = report("alias", v)
			}
			if !noFuncs && cont {
				if fn := sh.lookupFunc(name); fn != nil {
					cont = report("function", fn.Src)
				}
			}
			if _, ok := builtins[name]; ok && cont {
				cont = report("builtin", "")
			}
			if syntax.IsKeyword(name) && cont && !found {
				cont = report("keyword", "")
			}
		}
		if cont {
			if all {
				for _, d := range sh.GetList("PATH") {
					p := filepath.Join(d, name)
					if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
						report("file", p)
					}
				}
			} else if p, err := sh.lookPath(name); err == nil {
				report("file", p)
			}
		}
		if !found {
			status = 1
			if !quiet && !typeOnly && !pathOnly {
				fmt.Fprintf(io.Err, "type: could not find '%s'\n", name)
			}
		}
	}
	return status
}

func describeCmd(name, kind, desc string) string {
	switch kind {
	case "alias":
		return fmt.Sprintf("%s is an alias for %s", name, desc)
	case "function":
		return fmt.Sprintf("%s is a function with definition\n%s", name, strings.TrimRight(desc, "\n"))
	case "builtin":
		return fmt.Sprintf("%s is a builtin", name)
	case "keyword":
		return fmt.Sprintf("%s is a keyword", name)
	}
	return fmt.Sprintf("%s is %s", name, desc)
}

func bCommand(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	query, verbose, all := false, false, false
	for len(a) > 0 && strings.HasPrefix(a[0], "-") && len(a[0]) > 1 {
		opt := a[0]
		if opt == "--" {
			a = a[1:]
			break
		}
		switch opt {
		case "-q", "--query", "--quiet":
			query = true
		case "-v", "-s", "--search":
			verbose = true
		case "-V":
			verbose = true
		case "-a", "--all":
			all = true
		case "-p":
		default:
			ok := true
			for _, c := range opt[1:] {
				switch c {
				case 'q':
					query = true
				case 'v', 's', 'V':
					verbose = true
				case 'a':
					all = true
				case 'p':
				default:
					ok = false
				}
			}
			if !ok {
				fmt.Fprintf(io.Err, "command: unknown option %s\n", opt)
				return 2
			}
		}
		a = a[1:]
	}
	if len(a) == 0 {
		return 0
	}
	if query || verbose {
		status := 1
		for _, n := range a {
			if verbose && !query {
				// POSIX command -v also reports builtins, functions and aliases
				if v, ok := sh.aliases[n]; ok {
					fmt.Fprintf(io.Out, "alias %s=%s\n", n, Quote(v))
					status = 0
					continue
				}
				if sh.lookupFunc(n) != nil || builtins[n] != nil {
					fmt.Fprintln(io.Out, n)
					status = 0
					continue
				}
			}
			if all {
				for _, d := range sh.GetList("PATH") {
					p := filepath.Join(d, n)
					if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
						if !query {
							fmt.Fprintln(io.Out, p)
						}
						status = 0
					}
				}
				continue
			}
			if p, err := sh.lookPath(n); err == nil {
				if !query {
					fmt.Fprintln(io.Out, p)
				}
				status = 0
			}
		}
		return status
	}
	return sh.runExternal(a, io, nil, sh.takeProcFds())
}

func bBuiltin(sh *Shell, io *IO, args []string) int {
	if len(args) < 2 {
		return 0
	}
	switch args[1] {
	case "-n", "--names":
		for _, n := range BuiltinNames() {
			fmt.Fprintln(io.Out, n)
		}
		return 0
	case "-q", "--query":
		for _, n := range args[2:] {
			if _, ok := builtins[n]; ok {
				return 0
			}
		}
		return 1
	}
	b, ok := builtins[args[1]]
	if !ok {
		fmt.Fprintf(io.Err, "builtin: %s: not a builtin\n", args[1])
		return 1
	}
	return b(sh, io, args[1:])
}

func bHash(sh *Shell, io *IO, args []string) int {
	sh.shared.paths.clear()
	return 0
}
