//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package history

import (
	"os"
	"time"
)

// lockFile is a no-op where there is no flock(2): appends are still atomic,
// but a rewrite may lose an entry another shell appends meanwhile.
func lockFile(f *os.File, wait time.Duration) bool { return false }
