//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package shell

import "syscall"

func mtime(s *syscall.Stat_t) int64 { return s.Mtimespec.Sec*1e9 + s.Mtimespec.Nsec }
func atime(s *syscall.Stat_t) int64 { return s.Atimespec.Sec*1e9 + s.Atimespec.Nsec }
