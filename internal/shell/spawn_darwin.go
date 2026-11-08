package shell

import (
	"runtime"
	"syscall"
	"unsafe"
)

// On macOS, fork has to copy the shell's page tables, and for a Go
// program those are big: starting /usr/bin/true took about 1.6ms that
// way, and half of that with posix_spawn, which doesn't copy anything.
// posix_spawn isn't in syscall or x/sys, so it's called in libc the way
// x/sys calls everything else (the trampolines are in spawn_darwin_*.s).

//go:cgo_import_dynamic libc_posix_spawn posix_spawn "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawnattr_init posix_spawnattr_init "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawnattr_destroy posix_spawnattr_destroy "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawnattr_setflags posix_spawnattr_setflags "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawnattr_setpgroup posix_spawnattr_setpgroup "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawnattr_setsigmask posix_spawnattr_setsigmask "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawn_file_actions_init posix_spawn_file_actions_init "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawn_file_actions_destroy posix_spawn_file_actions_destroy "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawn_file_actions_adddup2 posix_spawn_file_actions_adddup2 "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_posix_spawn_file_actions_addchdir_np posix_spawn_file_actions_addchdir_np "/usr/lib/libSystem.B.dylib"

var (
	libc_posix_spawn_trampoline_addr                          uintptr
	libc_posix_spawnattr_init_trampoline_addr                 uintptr
	libc_posix_spawnattr_destroy_trampoline_addr              uintptr
	libc_posix_spawnattr_setflags_trampoline_addr             uintptr
	libc_posix_spawnattr_setpgroup_trampoline_addr            uintptr
	libc_posix_spawnattr_setsigmask_trampoline_addr           uintptr
	libc_posix_spawn_file_actions_init_trampoline_addr        uintptr
	libc_posix_spawn_file_actions_destroy_trampoline_addr     uintptr
	libc_posix_spawn_file_actions_adddup2_trampoline_addr     uintptr
	libc_posix_spawn_file_actions_addchdir_np_trampoline_addr uintptr
)

//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

const (
	spawnSetPgroup      = 0x0002
	spawnSetSigmask     = 0x0008
	spawnCloexecDefault = 0x4000
)

// libc calls a posix_spawn* function; they return the error instead of
// setting errno.
func libc(fn uintptr, a ...uintptr) syscall.Errno {
	var args [6]uintptr
	copy(args[:], a)
	r, _, _ := syscall_syscall6(fn, args[0], args[1], args[2], args[3], args[4], args[5])
	return syscall.Errno(r)
}

// startProcess starts path like syscall.ForkExec. A job that has to take
// the terminal in the child (Foreground) still forks: posix_spawn can't
// call tcsetpgrp.
func startProcess(path string, argv []string, attr *syscall.ProcAttr) (int, error) {
	if sys := attr.Sys; sys != nil && (sys.Foreground || sys.Setsid || sys.Setctty || sys.Chroot != "" || sys.Credential != nil || sys.Noctty || sys.Ptrace) {
		return syscall.ForkExec(path, argv, attr)
	}
	return posixSpawn(path, argv, attr)
}

func posixSpawn(path string, argv []string, attr *syscall.ProcAttr) (pid int, err error) {
	pathp, err := syscall.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}
	argvp, err := syscall.SlicePtrFromStrings(argv)
	if err != nil {
		return 0, err
	}
	envp, err := syscall.SlicePtrFromStrings(attr.Env)
	if err != nil {
		return 0, err
	}
	var dirp *byte
	if attr.Dir != "" {
		if dirp, err = syscall.BytePtrFromString(attr.Dir); err != nil {
			return 0, err
		}
	}

	// The child's descriptor i is attr.Files[i]. The dup2s run in
	// order, so a source that is also an earlier target would be
	// overwritten first; those are copied out of the way here, and so is
	// one already in place: with POSIX_SPAWN_CLOEXEC_DEFAULT a dup2 onto
	// itself leaves it close-on-exec.
	n := len(attr.Files)
	src := make([]int, n)
	var copies []int
	defer func() {
		for _, fd := range copies {
			syscall.Close(fd)
		}
	}()
	for i, f := range attr.Files {
		src[i] = int(f)
		if f == ^uintptr(0) || int(f) >= n {
			continue
		}
		fd, _, e := syscall.Syscall(syscall.SYS_FCNTL, f, syscall.F_DUPFD_CLOEXEC, uintptr(n))
		if e != 0 {
			return 0, e
		}
		copies = append(copies, int(fd))
		src[i] = int(fd)
	}

	var fa, sa uintptr // posix_spawn_file_actions_t, posix_spawnattr_t: pointers
	if e := libc(libc_posix_spawn_file_actions_init_trampoline_addr, uintptr(unsafe.Pointer(&fa))); e != 0 {
		return 0, e
	}
	defer libc(libc_posix_spawn_file_actions_destroy_trampoline_addr, uintptr(unsafe.Pointer(&fa)))
	if e := libc(libc_posix_spawnattr_init_trampoline_addr, uintptr(unsafe.Pointer(&sa))); e != 0 {
		return 0, e
	}
	defer libc(libc_posix_spawnattr_destroy_trampoline_addr, uintptr(unsafe.Pointer(&sa)))

	if dirp != nil {
		if e := libc(libc_posix_spawn_file_actions_addchdir_np_trampoline_addr, uintptr(unsafe.Pointer(&fa)), uintptr(unsafe.Pointer(dirp))); e != 0 {
			return 0, e
		}
	}
	for i, fd := range src {
		if fd == -1 || uintptr(fd) == ^uintptr(0) {
			continue // closed in the child: everything not dup2'd is
		}
		if e := libc(libc_posix_spawn_file_actions_adddup2_trampoline_addr, uintptr(unsafe.Pointer(&fa)), uintptr(fd), uintptr(i)); e != 0 {
			return 0, e
		}
	}
	flags := uintptr(spawnCloexecDefault | spawnSetSigmask)
	if sys := attr.Sys; sys != nil && sys.Setpgid {
		flags |= spawnSetPgroup
		if e := libc(libc_posix_spawnattr_setpgroup_trampoline_addr, uintptr(unsafe.Pointer(&sa)), uintptr(sys.Pgid)); e != 0 {
			return 0, e
		}
	}
	var empty uint32 // sigset_t
	if e := libc(libc_posix_spawnattr_setsigmask_trampoline_addr, uintptr(unsafe.Pointer(&sa)), uintptr(unsafe.Pointer(&empty))); e != 0 {
		return 0, e
	}
	if e := libc(libc_posix_spawnattr_setflags_trampoline_addr, uintptr(unsafe.Pointer(&sa)), flags); e != 0 {
		return 0, e
	}
	var cpid int32
	e := libc(libc_posix_spawn_trampoline_addr, uintptr(unsafe.Pointer(&cpid)), uintptr(unsafe.Pointer(pathp)),
		uintptr(unsafe.Pointer(&fa)), uintptr(unsafe.Pointer(&sa)),
		uintptr(unsafe.Pointer(&argvp[0])), uintptr(unsafe.Pointer(&envp[0])))
	runtime.KeepAlive(pathp)
	runtime.KeepAlive(argvp)
	runtime.KeepAlive(envp)
	runtime.KeepAlive(dirp)
	if e != 0 {
		return 0, e
	}
	return int(cpid), nil
}
