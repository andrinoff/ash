// Package plugintest is a fake ash plugin for tests. A test binary runs
// itself as the plugin; the package that wants one adds
//
//	func TestPluginHelperProcess(t *testing.T) { plugintest.Main() }
//
// and starts Command() with Env(mode) in the plugin's environment.
//
// Modes:
//
//	basic     says hello to every event, echoes events back as messages,
//	          and has the commands and completions the tests use
//	silent    never says hello
//	deaf      says hello to every event but never reads its input
//	exit      says hello, then exits with status 5
//	spam      sends thousands of segment updates and messages
//	stubborn  ignores SIGTERM and the end of its input
//	sink      reads and discards events, answers greet
package plugintest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// EnvVar selects the mode.
const EnvVar = "ASH_PLUGINTEST_MODE"

// Command returns the executable and argv that run the fake plugin.
func Command() (string, []string) {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return exe, []string{exe, "-test.run=^TestPluginHelperProcess$"}
}

// Env returns the environment entry that selects mode.
func Env(mode string) string { return EnvVar + "=" + mode }

// Main runs the fake plugin and exits, if the environment asks for it;
// otherwise it returns at once.
func Main() {
	mode := os.Getenv(EnvVar)
	if mode == "" {
		return
	}
	run(mode)
	os.Exit(0)
}

var outMu sync.Mutex

// send writes one JSON line.
func send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	outMu.Lock()
	defer outMu.Unlock()
	os.Stdout.Write(append(b, '\n'))
}

func sendRaw(s string) {
	outMu.Lock()
	defer outMu.Unlock()
	os.Stdout.WriteString(s + "\n")
}

type obj = map[string]any

var allEvents = []string{"prompt", "preexec", "postexec", "cd", "exit"}

func hello(name string, events []string, commands []string, complete []string) {
	var cmds []obj
	for _, c := range commands {
		cmds = append(cmds, obj{"name": c, "description": "the " + c + " command"})
	}
	send(obj{"hello": obj{"name": name, "events": events, "commands": cmds, "complete": complete}})
}

func run(mode string) {
	switch mode {
	case "basic":
		hello("basic", allEvents,
			[]string{"greet", "cat", "pwd", "where", "getenv", "fail", "slow", "crash", "act", "spawn", "rehello", "later", "noise"},
			[]string{"fruit", "slowfruit", "greet", "where"})
		serve(true)
	case "sink":
		hello("sink", allEvents, []string{"greet"}, []string{"fruit"})
		serve(false)
	case "silent":
		readAll()
	case "deaf":
		hello("deaf", allEvents, []string{"deafcmd"}, nil)
		sleepForever()
	case "exit":
		hello("exit", allEvents, []string{"gone"}, nil)
		time.Sleep(50 * time.Millisecond)
		os.Exit(5)
	case "spam":
		hello("spam", nil, nil, nil)
		for i := 0; i < 3000; i++ {
			send(obj{"segment": obj{"name": "count", "text": strconv.Itoa(i)}})
		}
		for i := 0; i < 2000; i++ {
			send(obj{"message": "message " + strconv.Itoa(i)})
		}
		send(obj{"segment": obj{"name": "count", "text": "done"}})
		readAll()
	case "stubborn":
		signal.Ignore(syscall.SIGTERM)
		hello("stubborn", []string{"exit"}, []string{"stubborncmd"}, nil)
		readAll()
		sleepForever()
	default:
		fmt.Fprintln(os.Stderr, "plugintest: unknown mode", mode)
		os.Exit(2)
	}
}

// sleepForever blocks until the process is killed. (A bare select{}
// would make the runtime report a deadlock and exit.)
func sleepForever() {
	for {
		time.Sleep(time.Hour)
	}
}

func readAll() {
	r := bufio.NewReader(os.Stdin)
	for {
		if _, err := r.ReadString('\n'); err != nil {
			return
		}
	}
}

// serve answers requests until stdin ends. With echo, events come back
// as messages: "event " followed by the line ash sent.
func serve(echo bool) {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Text()
		var m obj
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			fmt.Fprintln(os.Stderr, "plugintest: bad line:", line)
			continue
		}
		if _, ok := m["event"]; ok {
			if echo {
				send(obj{"message": "event " + line})
			}
			continue
		}
		id := m["id"]
		switch m["request"] {
		case "run":
			runCommand(id, m)
		case "complete":
			complete(id, m)
		case "cancel":
			send(obj{"message": fmt.Sprintf("cancel %v", id)})
		}
	}
}

func strs(v any) []string {
	l, _ := v.([]any)
	out := make([]string, len(l))
	for i, x := range l {
		out[i], _ = x.(string)
	}
	return out
}

func answer(id any, stdout, stderr string, status int) {
	send(obj{"id": id, "stdout": stdout, "stderr": stderr, "status": status})
}

func runCommand(id any, m obj) {
	name, _ := m["name"].(string)
	args := strs(m["args"])
	switch name {
	case "greet":
		answer(id, "hello "+strings.Join(args, " ")+"\n", "", 0)
	case "cat":
		if s, ok := m["stdin"].(string); ok {
			answer(id, s, "", 0)
		} else {
			answer(id, "(no stdin)\n", "", 0)
		}
	case "pwd", "where":
		cwd, _ := m["cwd"].(string)
		answer(id, cwd+"\n", "", 0)
	case "getenv":
		var out string
		for _, a := range args {
			out += os.Getenv(a) + "\n"
		}
		answer(id, out, "", 0)
	case "fail":
		answer(id, "", "it failed\n", 3)
	case "slow":
		// never answers
	case "crash":
		os.Exit(7)
	case "act":
		act(args)
		answer(id, "", "", 0)
	case "spawn":
		c := exec.Command("sleep", "300")
		if err := c.Start(); err != nil {
			answer(id, "", err.Error(), 1)
			return
		}
		answer(id, strconv.Itoa(c.Process.Pid)+"\n", "", 0)
	case "rehello":
		hello("basic", []string{"prompt"}, []string{"greet2"}, nil)
		answer(id, "", "", 0)
	case "later":
		ms := 100
		if len(args) > 0 {
			ms, _ = strconv.Atoi(args[0])
		}
		go func() {
			time.Sleep(time.Duration(ms) * time.Millisecond)
			answer(id, "later "+strconv.Itoa(ms)+"\n", "", 0)
		}()
	case "noise":
		sendRaw("this is not json")
		sendRaw("{}")
		sendRaw(`{"segment":{"name":"bad name","text":"x"}}`)
		answer(id, "", "", 0)
	default:
		answer(id, "", "unknown command "+name+"\n", 127)
	}
}

func act(args []string) {
	if len(args) == 0 {
		return
	}
	rest := args[1:]
	arg := func(i int) string {
		if i < len(rest) {
			return rest[i]
		}
		return ""
	}
	switch args[0] {
	case "set":
		send(obj{"set": obj{"name": arg(0), "value": rest[1:]}})
	case "export":
		send(obj{"set": obj{"name": arg(0), "value": rest[1:], "export": true}})
	case "unset":
		send(obj{"unset": arg(0)})
	case "segment":
		send(obj{"segment": obj{"name": arg(0), "text": arg(1)}})
	case "cd":
		send(obj{"cd": arg(0)})
	case "buffer":
		send(obj{"buffer": arg(0)})
	case "message":
		send(obj{"message": arg(0)})
	case "eval":
		send(obj{"eval": arg(0)})
	case "raw":
		sendRaw(arg(0))
	}
}

func complete(id any, m obj) {
	name, _ := m["name"].(string)
	line, _ := m["line"].(string)
	cursor := 0
	if c, ok := m["cursor"].(float64); ok {
		cursor = int(c)
	}
	if cursor > len(line) {
		cursor = len(line)
	}
	word := line[:cursor]
	if i := strings.LastIndexByte(word, ' '); i >= 0 {
		word = word[i+1:]
	}
	var words []string
	switch name {
	case "fruit":
		words = []string{"apple", "apricot", "banana", "cherry"}
	case "greet":
		words = []string{"world", "there"}
	case "where":
		cwd, _ := m["cwd"].(string)
		words = []string{cwd}
	case "slowfruit":
		go func() {
			time.Sleep(500 * time.Millisecond)
			send(obj{"id": id, "candidates": []obj{{"text": "late", "description": "slow"}}})
		}()
		return
	}
	cands := []obj{}
	for _, w := range words {
		if strings.HasPrefix(w, word) {
			cands = append(cands, obj{"text": w, "description": name})
		}
	}
	send(obj{"id": id, "candidates": cands})
}
