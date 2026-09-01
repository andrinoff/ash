package shell

// direnv: before each prompt, `direnv export json` says which variables
// to set and which to unset (null), and we apply that natively.

import (
	"bytes"
	"encoding/json"
	"sort"
)

func init() {
	registerIntegration(integration{"direnv", "direnv", "load .envrc files before each prompt", setupDirenv})
}

func setupDirenv(sh *Shell, io *IO, exe string) string {
	in := sh.shared.integr
	in.mu.Lock()
	in.direnv = exe
	in.mu.Unlock()
	if in.once("direnv") {
		sh.OnPrePrompt(func(sh *Shell) { sh.direnvExport() })
	}
	return ".envrc files are loaded before each prompt"
}

func (sh *Shell) direnvExport() {
	in := sh.shared.integr
	if _, ok := in.on("direnv"); !ok {
		return
	}
	in.mu.Lock()
	exe := in.direnv
	in.mu.Unlock()
	c := sh.Command(exe, "export", "json")
	c.Stderr = sh.Stderr
	var out bytes.Buffer
	c.Stdout = &out
	c.Run() // a blocked .envrc fails but may still print what to unload
	sh.applyDirenv(out.Bytes())
}

// applyDirenv applies direnv's JSON diff: null unsets a variable, a string
// sets and exports it.
func (sh *Shell) applyDirenv(data []byte) {
	if len(bytes.TrimSpace(data)) == 0 {
		return
	}
	var diff map[string]*string
	if err := json.Unmarshal(data, &diff); err != nil {
		return
	}
	names := make([]string, 0, len(diff))
	for k := range diff {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v := diff[k]
		switch {
		case v == nil:
			sh.UnsetVar(k, setGlobal)
		case *v == "":
			// Like `export K=`: an empty value is an empty list.
			sh.SetVar(k, []string{}, setOpts{scope: setGlobal, export: 1})
		default:
			sh.SetVar(k, []string{*v}, setOpts{scope: setGlobal, export: 1})
		}
	}
}
