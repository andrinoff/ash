//go:build !unix

package history

import "os"

// fileOwner reports that files have no owner to keep here.
func fileOwner(fi os.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }
