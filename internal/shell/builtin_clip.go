package shell

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// clip copies to and pastes from the clipboard. Over SSH, or where there
// is no clipboard program, the copy goes through the terminal (OSC 52), so
// `git diff | clip` on a server lands on the clipboard of the machine
// you're sitting at.

func init() {
	builtins["clip"] = bClip
	builtinHelp["clip"] = "copy to or paste from the clipboard"
}

const clipUsage = `usage: clip [TEXT...]
       clip -o

Copy TEXT, or standard input, to the clipboard. Over SSH the copy goes
through your terminal (OSC 52), so it reaches the clipboard of the
machine you're at, as long as the terminal allows it.

  -o, --output   print what's on the clipboard
  -h, --help     show this help
`

// clipTTY opens the terminal for OSC 52; tests replace it.
var clipTTY = func() (io.WriteCloser, error) { return os.OpenFile("/dev/tty", os.O_WRONLY, 0) }

// clipMax is more than any terminal takes in one OSC 52 sequence.
const clipMax = 8 << 20

func bClip(sh *Shell, io *IO, args []string) int {
	a := args[1:]
	output := false
opts:
	for len(a) > 0 && len(a[0]) > 1 && a[0][0] == '-' {
		switch a[0] {
		case "-o", "--output", "--paste":
			output = true
		case "-h", "--help":
			fmt.Fprint(io.Out, clipUsage)
			return 0
		case "--":
			a = a[1:]
			break opts
		default:
			fmt.Fprintf(io.Err, "clip: unknown option %s\n", a[0])
			return 2
		}
		a = a[1:]
	}
	if output {
		return sh.clipPaste(io)
	}
	var data []byte
	if len(a) > 0 {
		data = []byte(strings.Join(a, " "))
	} else {
		if io.In.TTY {
			fmt.Fprintln(io.Err, "clip: nothing to copy (give it text or pipe something in)")
			return 2
		}
		var err error
		if data, err = readAllLimited(io.In, clipMax+1); err != nil {
			fmt.Fprintf(io.Err, "clip: %v\n", err)
			return 1
		}
	}
	return sh.clipCopy(io, data)
}

func (sh *Shell) remoteSession() bool {
	return sh.Get("SSH_TTY") != "" || sh.Get("SSH_CONNECTION") != ""
}

// clipProgram finds the clipboard program for copying or pasting.
func (sh *Shell) clipProgram(paste bool) []string {
	type tool struct {
		copy, paste []string
		ok          func() bool
	}
	always := func() bool { return true }
	tools := []tool{
		{[]string{"pbcopy"}, []string{"pbpaste"}, always},
		{[]string{"wl-copy"}, []string{"wl-paste", "--no-newline"}, func() bool { return sh.Get("WAYLAND_DISPLAY") != "" }},
		{[]string{"xclip", "-selection", "clipboard"}, []string{"xclip", "-selection", "clipboard", "-o"}, func() bool { return sh.Get("DISPLAY") != "" }},
		{[]string{"xsel", "--clipboard", "--input"}, []string{"xsel", "--clipboard", "--output"}, func() bool { return sh.Get("DISPLAY") != "" }},
	}
	for _, t := range tools {
		argv := t.copy
		if paste {
			argv = t.paste
		}
		if p, found := sh.LookPath(argv[0]); found && t.ok() {
			return append([]string{p}, argv[1:]...)
		}
	}
	return nil
}

func (sh *Shell) clipRun(argv []string, in io.Reader, out io.Writer) error {
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = sh.Environ()
	c.Stdin, c.Stdout = in, out
	return c.Run()
}

func (sh *Shell) clipCopy(io *IO, data []byte) int {
	if !sh.remoteSession() {
		if argv := sh.clipProgram(false); argv != nil && sh.clipRun(argv, bytes.NewReader(data), nil) == nil {
			return 0
		}
	}
	if sh.Get("TMUX") != "" {
		// tmux ignores OSC 52 from programs unless set-clipboard is on,
		// but passes its own buffers on to the terminal
		if p, ok := sh.LookPath("tmux"); ok && sh.clipRun([]string{p, "load-buffer", "-w", "-"}, bytes.NewReader(data), nil) == nil {
			return 0
		}
	}
	if len(data) > clipMax {
		fmt.Fprintf(io.Err, "clip: too much for the terminal to take (more than %d MB)\n", clipMax>>20)
		return 1
	}
	tty, err := clipTTY()
	if err != nil {
		fmt.Fprintln(io.Err, "clip: no clipboard program, and no terminal to ask")
		return 1
	}
	defer tty.Close()
	if _, err := tty.Write([]byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString(data) + "\x07")); err != nil {
		fmt.Fprintf(io.Err, "clip: %v\n", err)
		return 1
	}
	return 0
}

func (sh *Shell) clipPaste(io *IO) int {
	var argv []string
	if !sh.remoteSession() {
		argv = sh.clipProgram(true)
	}
	if argv == nil && sh.Get("TMUX") != "" {
		if p, ok := sh.LookPath("tmux"); ok {
			argv = []string{p, "save-buffer", "-"}
		}
	}
	if argv == nil {
		fmt.Fprintln(io.Err, "clip: can't read the clipboard from here")
		return 1
	}
	if err := sh.clipRun(argv, nil, integWriter(io.Out)); err != nil {
		fmt.Fprintf(io.Err, "clip: %s: %v\n", argv[0], err)
		return 1
	}
	return 0
}
