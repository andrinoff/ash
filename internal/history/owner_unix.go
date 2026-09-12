//go:build unix

package history

import (
	"os"
	"syscall"
)

// fileOwner returns the user and group that own a file.
func fileOwner(fi os.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
