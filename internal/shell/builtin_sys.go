package shell

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func bUmask(sh *Shell, io *IO, args []string) int {
	symbolic := false
	a := args[1:]
	if len(a) > 0 && a[0] == "-S" {
		symbolic = true
		a = a[1:]
	}
	if len(a) == 0 {
		m := syscall.Umask(0)
		syscall.Umask(m)
		if symbolic {
			perm := func(shift uint) string {
				v := (^m >> shift) & 7
				s := ""
				if v&4 != 0 {
					s += "r"
				}
				if v&2 != 0 {
					s += "w"
				}
				if v&1 != 0 {
					s += "x"
				}
				return s
			}
			fmt.Fprintf(io.Out, "u=%s,g=%s,o=%s\n", perm(6), perm(3), perm(0))
			return 0
		}
		fmt.Fprintf(io.Out, "%04o\n", m)
		return 0
	}
	v, err := strconv.ParseUint(a[0], 8, 32)
	if err != nil || v > 0o777 {
		cur := syscall.Umask(0)
		syscall.Umask(cur)
		m, ok := symbolicUmask(a[0], cur)
		if !ok {
			fmt.Fprintf(io.Err, "umask: %s: invalid mask\n", a[0])
			return 1
		}
		v = uint64(m)
	}
	syscall.Umask(int(v))
	return 0
}

// symbolicUmask applies a chmod-style spec like u=rwx,g=rx,o= or g-w
// (bash and fish accept these) to the mask cur. The spec says which
// permissions to allow; the mask is the opposite.
func symbolicUmask(spec string, cur int) (int, bool) {
	allowed := ^cur & 0o777
	for _, clause := range strings.Split(spec, ",") {
		i, who := 0, 0
		for ; i < len(clause) && strings.IndexByte("ugoa", clause[i]) >= 0; i++ {
			who |= map[byte]int{'u': 0o700, 'g': 0o070, 'o': 0o007, 'a': 0o777}[clause[i]]
		}
		if who == 0 {
			who = 0o777
		}
		if i == len(clause) {
			return 0, false
		}
		for i < len(clause) {
			op := clause[i]
			if op != '=' && op != '+' && op != '-' {
				return 0, false
			}
			perm := 0
			for i++; i < len(clause) && strings.IndexByte("rwx", clause[i]) >= 0; i++ {
				perm |= map[byte]int{'r': 0o444, 'w': 0o222, 'x': 0o111}[clause[i]]
			}
			perm &= who
			switch op {
			case '=':
				allowed = allowed&^who | perm
			case '+':
				allowed |= perm
			case '-':
				allowed &^= perm
			}
		}
	}
	return ^allowed & 0o777, true
}

func bTimes(sh *Shell, io *IO, args []string) int {
	var self, kids syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &self)
	syscall.Getrusage(syscall.RUSAGE_CHILDREN, &kids)
	f := func(tv syscall.Timeval) string {
		d := tvDur(tv)
		return fmt.Sprintf("%dm%.3fs", int(d.Minutes()), d.Seconds()-float64(int(d.Minutes())*60))
	}
	fmt.Fprintf(io.Out, "%s %s\n%s %s\n", f(self.Utime), f(self.Stime), f(kids.Utime), f(kids.Stime))
	return 0
}

func bUlimit(sh *Shell, io *IO, args []string) int {
	res := map[byte]struct {
		r    int
		name string
		unit int64
	}{
		'c': {unix.RLIMIT_CORE, "core file size (blocks)", 512},
		'd': {unix.RLIMIT_DATA, "data seg size (kbytes)", 1024},
		'f': {unix.RLIMIT_FSIZE, "file size (blocks)", 512},
		'n': {unix.RLIMIT_NOFILE, "open files", 1},
		's': {unix.RLIMIT_STACK, "stack size (kbytes)", 1024},
		't': {unix.RLIMIT_CPU, "cpu time (seconds)", 1},
		'v': {unix.RLIMIT_AS, "virtual memory (kbytes)", 1024},
	}
	which := byte('f')
	hard := false
	all := false
	var val string
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			for i := 1; i < len(a); i++ {
				switch a[i] {
				case 'H':
					hard = true
				case 'S':
					hard = false
				case 'a':
					all = true
				default:
					which = a[i]
				}
			}
			continue
		}
		val = a
	}
	show := func(c byte) int {
		r, ok := res[c]
		if !ok {
			fmt.Fprintf(io.Err, "ulimit: -%c: unsupported\n", c)
			return 1
		}
		var lim unix.Rlimit
		if err := unix.Getrlimit(r.r, &lim); err != nil {
			fmt.Fprintf(io.Err, "ulimit: %v\n", err)
			return 1
		}
		v := lim.Cur
		if hard {
			v = lim.Max
		}
		s := "unlimited"
		if v != unix.RLIM_INFINITY {
			s = strconv.FormatUint(v/uint64(r.unit), 10)
		}
		if all {
			fmt.Fprintf(io.Out, "%-30s (-%c) %s\n", r.name, c, s)
		} else {
			fmt.Fprintln(io.Out, s)
		}
		return 0
	}
	if all {
		for _, c := range []byte("cdfnstv") {
			show(c)
		}
		return 0
	}
	if val == "" {
		return show(which)
	}
	r, ok := res[which]
	if !ok {
		fmt.Fprintf(io.Err, "ulimit: -%c: unsupported\n", which)
		return 1
	}
	var lim unix.Rlimit
	unix.Getrlimit(r.r, &lim)
	var nv uint64
	if val == "unlimited" {
		nv = unix.RLIM_INFINITY
	} else {
		n, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			fmt.Fprintf(io.Err, "ulimit: %s: invalid number\n", val)
			return 1
		}
		nv = n * uint64(r.unit)
	}
	if hard {
		lim.Max = nv
	} else {
		lim.Cur = nv
	}
	if err := unix.Setrlimit(r.r, &lim); err != nil {
		fmt.Fprintf(io.Err, "ulimit: %v\n", err)
		return 1
	}
	return 0
}

func bGetopts(sh *Shell, io *IO, args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(io.Err, "getopts: usage: getopts optstring name [arg...]")
		return 2
	}
	optstring, name := args[1], args[2]
	params := sh.args()
	if len(args) > 3 {
		params = args[3:]
	}
	silent := strings.HasPrefix(optstring, ":")
	if silent {
		optstring = optstring[1:]
	}
	optVar, _ := sh.lookupVar("OPTIND")
	optind := 1
	if optVar != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(optVar.String())); err == nil && n > 1 {
			optind = n
		}
	}
	sub := 0
	if optVar != nil && optVar == sh.getoptsVar {
		sub = sh.getoptsSub
	}
	setVar := func(n, v string) { sh.SetVar(n, []string{v}, setOpts{scope: setShAuto}) }
	unsetOptarg := func() { sh.UnsetVar("OPTARG", setShAuto) }
	// finish records where the next call continues; OPTIND is set on
	// every call, as in bash, so `shift $((OPTIND-1))` always works.
	finish := func(status int) int {
		setVar("OPTIND", strconv.Itoa(optind))
		sh.getoptsVar, _ = sh.lookupVar("OPTIND")
		sh.getoptsSub = sub
		return status
	}
	end := func() int {
		sub = 0
		setVar(name, "?")
		unsetOptarg()
		return finish(1)
	}
	if optind-1 >= len(params) {
		return end()
	}
	arg := params[optind-1]
	if sub >= len(arg) {
		// the arguments changed since the last call
		sub = 0
	}
	if sub == 0 {
		if arg == "--" {
			optind++
			return end()
		}
		if len(arg) < 2 || arg[0] != '-' {
			return end()
		}
		sub = 1
	}
	c := arg[sub]
	opt := string([]byte{c})
	sub++
	next := func() {
		if sub >= len(arg) {
			optind++
			sub = 0
		}
	}
	i := strings.IndexByte(optstring, c)
	if i < 0 || c == ':' {
		if silent {
			setVar("OPTARG", opt)
		} else {
			fmt.Fprintf(io.Err, "getopts: illegal option -- %s\n", opt)
			unsetOptarg()
		}
		setVar(name, "?")
		next()
		return finish(0)
	}
	if i+1 < len(optstring) && optstring[i+1] == ':' {
		switch {
		case sub < len(arg):
			setVar("OPTARG", arg[sub:])
			optind++
		case optind < len(params):
			setVar("OPTARG", params[optind])
			optind += 2
		default:
			optind++
			sub = 0
			if silent {
				setVar(name, ":")
				setVar("OPTARG", opt)
			} else {
				fmt.Fprintf(io.Err, "getopts: option requires an argument -- %s\n", opt)
				setVar(name, "?")
				unsetOptarg()
			}
			return finish(0)
		}
		sub = 0
		setVar(name, opt)
		return finish(0)
	}
	setVar(name, opt)
	unsetOptarg()
	next()
	return finish(0)
}

func bShopt(sh *Shell, io *IO, args []string) int { return 0 }
