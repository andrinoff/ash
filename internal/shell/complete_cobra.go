package shell

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Programs written with cobra (gh, kubectl, helm, hugo, glow, colima and
// hundreds more) complete their own arguments: `prog __complete ARGS...
// WORD` prints the candidates for WORD. The completion scripts they
// generate for each shell do little more than call that. ash calls it
// directly, so there's nothing to install: when a command has no
// completion file, ash looks at the build information Go writes into
// every binary, and if cobra is one of the program's modules, it asks the
// program. Nothing is run to find out, so a program that isn't cobra never
// gets an odd "__complete" argument.

// The directive on cobra's last line of output (":4" and so on).
const (
	cobraError   = 1
	cobraNoSpace = 2
	cobraNoFiles = 4
	cobraFileExt = 8
	cobraDirs    = 16
)

// cobraTimeout bounds how long a program may take to answer. A program
// that has to ask a server (kubectl listing pods) can be slow, but the
// line editor waits for it.
var cobraTimeout = 3 * time.Second

// isCobra reports whether the program at path was built with cobra. Tests
// replace it.
var isCobra = func(path string) bool {
	return bytes.Contains(goModInfo(path), []byte("\tgithub.com/spf13/cobra\t"))
}

func init() {
	builtins["__ash_cobra_complete"] = bCobraComplete
	builtinHelp["__ash_cobra_complete"] = "ask a cobra program to complete its own arguments"
}

// cobraSpec returns completion code for cmd, under a made-up file name,
// if it is a cobra program; findFile uses it when there is no completion
// file, or when the file is one cobra generated.
func (r *compRun) cobraSpec(cmd string) (string, []byte) {
	p, ok := r.sh.LookPath(cmd)
	if !ok || !isCobra(p) {
		return "", nil
	}
	// The condition asks the program and fails when files should be
	// completed instead; the candidates are what it answered.
	src := "complete -c " + Quote(cmd) + " -n __ash_cobra_complete -f -a '(__ash_cobra_complete --results)'\n"
	return "cobra:" + cmd, []byte(src)
}

// cobraScript reports whether a completion file is a script cobra wrote
// (`prog completion fish`, which package managers install). Those only
// ask the program, which ash does more directly.
func cobraScript(b []byte) bool {
	return bytes.Contains(b, []byte(" __complete ")) && bytes.Contains(b, []byte("_perform_completion"))
}

// cobraAnswer holds the candidates between the condition that asked for
// them and the argument list that uses them, for one completion run.
var cobraAnswer struct {
	sync.Mutex
	run   *compRun
	items []string
}

func bCobraComplete(sh *Shell, io *IO, args []string) int {
	r := sh.shared.complete.active.Load()
	if r == nil {
		fmt.Fprintf(io.Err, "%s: only works while completing\n", args[0])
		return 2
	}
	if len(args) > 1 && args[1] == "--results" {
		cobraAnswer.Lock()
		defer cobraAnswer.Unlock()
		if cobraAnswer.run == r {
			for _, it := range cobraAnswer.items {
				fmt.Fprintln(io.Out, it)
			}
		}
		return 0
	}
	// The words before the token being completed (the first is the
	// command), and the token itself.
	buf, tok := r.ed.buf, r.ed.tok
	if tok > len(buf) {
		tok = len(buf)
	}
	words := CommandTokens(buf[:tok])
	if len(words) == 0 {
		return 1
	}
	cur := strings.Join(CommandTokens(buf[tok:]), " ")
	path, ok := sh.LookPath(words[0])
	if !ok {
		return 1
	}
	items, directive, ok := sh.askCobra(path, append(words[1:len(words):len(words)], cur))
	if !ok {
		return 1
	}
	items, files := cobraDirective(items, directive, cur)
	cobraAnswer.Lock()
	cobraAnswer.run, cobraAnswer.items = r, items
	cobraAnswer.Unlock()
	if files {
		return 1
	}
	return 0
}

// askCobra runs `path __complete args...` and returns the candidates
// ("value" or "value<TAB>description") and the directive.
func (sh *Shell) askCobra(path string, args []string) (items []string, directive int, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), cobraTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, path, append([]string{"__complete"}, args...)...)
	// Active help is for shells that print it above the prompt; cobra's
	// fish script turns it off too.
	c.Env = append(sh.Environ(), "COBRA_ACTIVE_HELP=0")
	c.Dir = sh.dir
	var out bytes.Buffer
	c.Stdout = &cappedWriter{w: &out, n: 1 << 20}
	if c.Run() != nil {
		return nil, 0, false
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, ":") {
		return nil, 0, false // not an answer from cobra
	}
	directive, err := strconv.Atoi(last[1:])
	if err != nil {
		return nil, 0, false
	}
	for _, l := range lines[:len(lines)-1] {
		if l != "" && !strings.HasPrefix(l, "_activeHelp_") {
			items = append(items, l)
		}
	}
	return items, directive, true
}

// cobraDirective applies the directive the way cobra's own fish script
// does. It returns the candidates to offer, or files=true when file names
// should be completed instead.
func cobraDirective(items []string, d int, cur string) (out []string, files bool) {
	if d&cobraError != 0 {
		return nil, true
	}
	if d&(cobraFileExt|cobraDirs) != 0 {
		// Only these files, or only directories: settle for any file.
		return nil, true
	}
	if d&cobraNoSpace == 0 && d&cobraNoFiles != 0 {
		return items, false
	}
	// Files are fine, or there must be no space after a single answer:
	// see what actually matches what was typed.
	for _, it := range items {
		if v, _, _ := strings.Cut(it, "\t"); strings.HasPrefix(v, cur) {
			out = append(out, it)
		}
	}
	if len(out) == 1 && d&cobraNoSpace != 0 {
		v, _, _ := strings.Cut(out[0], "\t")
		if v != "" && strings.IndexByte("/=@:.,-", v[len(v)-1]) < 0 {
			// Two candidates make the common part go in without a space.
			out = []string{v, v + "."}
		}
	}
	if len(out) == 0 && d&cobraNoFiles == 0 {
		return nil, true
	}
	return out, false
}

// cappedWriter keeps the first n bytes and drops the rest.
type cappedWriter struct {
	w io.Writer
	n int
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if c.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > c.n {
		q = q[:c.n]
	}
	c.n -= len(q)
	if _, err := c.w.Write(q); err != nil {
		return 0, err
	}
	return len(p), nil
}

// goModInfo returns the module list Go embeds in the binary at path ("dep
// <TAB>module<TAB>version" lines), or nil for anything else. It reads the
// file's headers and one small section, never the whole binary, which
// matters for the 100 MB ones.
func goModInfo(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return decodeBuildInfo(buildInfoSection(f))
}

// buildInfoSection finds Go's build information section in an ELF or
// Mach-O executable.
func buildInfoSection(f *os.File) []byte {
	head := make([]byte, 4096)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if len(head) < 32 {
		return nil
	}
	switch {
	case bytes.HasPrefix(head, []byte("\x7fELF")):
		ef, err := elf.NewFile(f)
		if err != nil {
			return nil
		}
		s := ef.Section(".go.buildinfo")
		if s == nil || s.Size > 1<<20 {
			return nil
		}
		b, _ := s.Data()
		return b
	case binary.LittleEndian.Uint32(head) == 0xfeedfacf:
		return machoSection(f, 0, head)
	case binary.BigEndian.Uint32(head) == 0xcafebabe:
		// A universal binary: look in the slice for this machine, or the
		// first one. (Java class files share the magic number but would
		// claim dozens of slices.)
		nfat := int(binary.BigEndian.Uint32(head[4:]))
		if nfat < 1 || nfat > 8 || 8+20*nfat > len(head) {
			return nil
		}
		want := uint32(0x0100000c) // arm64
		if runtime.GOARCH == "amd64" {
			want = 0x01000007
		}
		pick := 0
		for i := 0; i < nfat; i++ {
			if binary.BigEndian.Uint32(head[8+20*i:]) == want {
				pick = i
			}
		}
		off := int64(binary.BigEndian.Uint32(head[8+20*pick+8:]))
		sub := make([]byte, 4096)
		n, _ := f.ReadAt(sub, off)
		if n < 32 || binary.LittleEndian.Uint32(sub) != 0xfeedfacf {
			return nil
		}
		return machoSection(f, off, sub[:n])
	}
	return nil
}

// machoSection reads the __go_buildinfo section of the 64-bit Mach-O
// image at offset base, whose first bytes are head.
func machoSection(f *os.File, base int64, head []byte) []byte {
	le := binary.LittleEndian
	ncmds := int(le.Uint32(head[16:]))
	size := int(le.Uint32(head[20:]))
	if size > 1<<20 {
		return nil
	}
	if 32+size > len(head) {
		head = make([]byte, 32+size)
		if n, _ := f.ReadAt(head, base); n < len(head) {
			return nil
		}
	}
	cmds := head[32 : 32+size]
	for i := 0; i < ncmds && len(cmds) >= 8; i++ {
		cmd, cmdsize := le.Uint32(cmds), int(le.Uint32(cmds[4:]))
		if cmdsize < 8 || cmdsize > len(cmds) {
			return nil
		}
		const lcSegment64 = 0x19
		if cmd == lcSegment64 && cmdsize >= 72 {
			seg := cmds[:cmdsize]
			nsects := int(le.Uint32(seg[64:]))
			for j := 0; j < nsects && 72+80*(j+1) <= len(seg); j++ {
				s := seg[72+80*j:]
				if string(bytes.TrimRight(s[:16], "\x00")) != "__go_buildinfo" {
					continue
				}
				n, off := le.Uint64(s[40:]), int64(le.Uint32(s[48:]))
				if n > 1<<20 {
					return nil
				}
				b := make([]byte, n)
				if m, _ := f.ReadAt(b, base+off); m < len(b) {
					return nil
				}
				return b
			}
		}
		cmds = cmds[cmdsize:]
	}
	return nil
}

// decodeBuildInfo returns the module information from a build information
// section (Go 1.18 and later write it inline; older binaries are skipped).
func decodeBuildInfo(b []byte) []byte {
	const magic = "\xff Go buildinf:"
	if len(b) < 32 || string(b[:len(magic)]) != magic || b[15]&2 == 0 {
		return nil
	}
	b = b[32:]
	// the Go version, then the module information, each length-prefixed
	for i := 0; i < 2; i++ {
		n, k := binary.Uvarint(b)
		if k <= 0 || n > uint64(len(b)-k) {
			return nil
		}
		if i == 1 {
			return b[k : k+int(n)]
		}
		b = b[k+int(n):]
	}
	return nil
}
