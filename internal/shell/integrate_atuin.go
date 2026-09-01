package shell

// atuin, following its bash integration: each command line is recorded
// with `atuin history start` / `atuin history end`, and ctrl-r opens
// `atuin search -i`. Recording runs in the background so commands never
// wait for atuin's database.

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func init() {
	registerIntegration(integration{"atuin", "atuin", "record history in atuin; ctrl-r searches it", setupAtuin})
	builtins["__ash_atuin_search"] = bAtuinSearch
	builtinHelp["__ash_atuin_search"] = "search history with atuin (ctrl-r)"
}

type atuinState struct {
	exe     string
	session string
	started chan string // id from the running `history start`, if any
	env     []string    // environment of the command being recorded
}

func setupAtuin(sh *Shell, io *IO, exe string) string {
	in := sh.shared.integr
	a := &in.atuin
	a.exe = exe
	// atuin's scripts run `export ATUIN_SESSION=$(atuin uuid)`; a UUIDv7
	// made here is the same thing without starting a process.
	if a.session == "" {
		a.session = atuinUUID(time.Now())
	}
	sh.SetVar("ATUIN_SESSION", []string{a.session}, setOpts{scope: setGlobal, export: 1})
	if in.once("atuin") {
		sh.OnPreExec(func(sh *Shell, line string) { sh.atuinStart(line) })
		sh.OnPostExec(func(sh *Shell, line string, status int, d time.Duration) { sh.atuinEnd(status, d) })
	}
	sh.integrateBind(integBinding{"ctrl-r", "__ash_atuin_search"})
	return "commands are recorded by " + exe + "; ctrl-r searches them"
}

// atuinUUID returns a UUIDv7 in the form `atuin uuid` prints (32 hex
// digits, no dashes).
func atuinUUID(now time.Time) string {
	var u [16]byte
	rand.Read(u[6:])
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(now.UnixMilli()))
	copy(u[:6], ms[2:])
	u[6] = u[6]&0x0f | 0x70 // version 7
	u[8] = u[8]&0x3f | 0x80 // RFC 4122 variant
	return hex.EncodeToString(u[:])
}

// atuinStart is the preexec half: `atuin history start -- LINE`, whose
// output is the id that atuinEnd needs.
func (sh *Shell) atuinStart(line string) {
	in := sh.shared.integr
	exe, ok := in.on("atuin")
	if !ok || sh.privateMode() || strings.TrimSpace(line) == "" {
		return
	}
	a := &in.atuin
	env := sh.Environ()
	dir := sh.dir
	ch := make(chan string, 1)
	a.started, a.env = ch, env
	in.background(func() {
		c := exec.Command(exe, "history", "start", "--", line)
		c.Env, c.Dir = env, dir
		out, st := integRun(c)
		if st != 0 {
			out = ""
		}
		ch <- strings.TrimSpace(out)
	})
}

// atuinEnd is the postexec half: once the start has given its id, run
// `atuin history end --exit STATUS --duration NANOS -- ID`.
func (sh *Shell) atuinEnd(status int, d time.Duration) {
	in := sh.shared.integr
	a := &in.atuin
	ch, env := a.started, a.env
	a.started, a.env = nil, nil
	exe, ok := in.on("atuin")
	if ch == nil || !ok {
		return
	}
	in.background(func() {
		id := <-ch
		if id == "" {
			return
		}
		c := exec.Command(exe, "history", "end", "--exit", strconv.Itoa(status),
			"--duration", strconv.FormatInt(d.Nanoseconds(), 10), "--", id)
		c.Env = append(env[:len(env):len(env)], "ATUIN_LOG=error")
		c.Dir = "/"
		c.Run()
	})
}

// bAtuinSearch is ctrl-r: `atuin search -i -- BUFFER`. atuin draws its
// interface on stdout and prints the pick on stderr (the bash script swaps
// them with 3>&1 1>&2 2>&3), so stdout goes to the terminal and stderr is
// captured. A pick made with enter_accept arrives as
// "__atuin_accept__:CMD" and runs at once.
func bAtuinSearch(sh *Shell, io *IO, args []string) int {
	exe, ok := sh.shared.integr.on("atuin")
	if !ok {
		var found bool
		if exe, found = sh.LookPath("atuin"); !found {
			fmt.Fprintf(io.Err, "%s: atuin not found\n", args[0])
			return 127
		}
	}
	buf := ""
	if sh.Editor != nil {
		buf, _ = sh.Editor.Buffer()
	}
	c := sh.Command(exe, append(append([]string{"search"}, args[1:]...), "-i", "--", buf)...)
	c.Env = append(c.Env, "ATUIN_SHELL=bash", "ATUIN_SHELL_BASH=t", "ATUIN_LOG=error", "ATUIN_QUERY="+buf)
	c.Stdin = integReader(io.In)
	c.Stdout = integWriter(io.Err)
	var pick bytes.Buffer
	c.Stderr = &pick
	st := integStatus(c.Run())
	out := strings.TrimRight(pick.String(), "\n")
	if out == "" {
		if st == 0 || st == 1 || st == 130 {
			return 1
		}
		return st
	}
	accept := strings.HasPrefix(out, "__atuin_accept__:")
	out = strings.TrimPrefix(out, "__atuin_accept__:")
	if sh.Editor == nil {
		fmt.Fprintln(io.Out, out)
		return 0
	}
	sh.Editor.SetBuffer(out, len(out))
	if accept {
		sh.Editor.QueueFunctions("execute")
	}
	return 0
}
