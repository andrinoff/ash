package repl

import (
	"os"
	"path/filepath"
	"sync"
)

// repoCache remembers which repository each directory is in, so that the
// history search doesn't look again each time it opens. A command can
// make or remove a repository, so the REPL forgets it all after each one.
type repoCache struct {
	mu    sync.Mutex
	roots map[string]string // directory -> repository root, "" for none
}

// root returns the root of the repository dir is in, or "" if it isn't in
// one.
func (c *repoCache) root(dir string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if root, ok := c.roots[dir]; ok {
		return root
	}
	if c.roots == nil || len(c.roots) >= 64 {
		c.roots = map[string]string{}
	}
	root := findRepo(dir)
	c.roots[dir] = root
	return root
}

// forget drops everything the cache knows.
func (c *repoCache) forget() {
	c.mu.Lock()
	c.roots = nil
	c.mu.Unlock()
}

// findRepo returns the nearest directory at or above dir that has a .git
// directory, or a .git file as worktrees and submodules do, or "" if there
// is none. It only looks at the file system: no git, and one stat per
// directory up to /.
func findRepo(dir string) string {
	if !filepath.IsAbs(dir) {
		return ""
	}
	for d := filepath.Clean(dir); ; {
		if fi, err := os.Stat(filepath.Join(d, ".git")); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
			return d
		}
		up := filepath.Dir(d)
		if up == d {
			return ""
		}
		d = up
	}
}
