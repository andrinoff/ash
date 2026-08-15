//go:build linux

package shell

import "syscall"

func mtime(s *syscall.Stat_t) int64 { return s.Mtim.Sec*1e9 + s.Mtim.Nsec }
func atime(s *syscall.Stat_t) int64 { return s.Atim.Sec*1e9 + s.Atim.Nsec }
