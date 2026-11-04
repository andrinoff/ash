package shell

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/andrinoff/ash/internal/syntax"
)

// lookPath finds a command in $PATH.
var errIsDir = errors.New("is a directory")

func (sh *Shell) lookPath(name string) (string, error) {
	if name == "" {
		return "", errNotFound
	}
	if strings.ContainsRune(name, '/') {
		p := sh.absPath(name)
		st, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		if st.IsDir() {
			return "", errIsDir
		}
		if st.Mode()&0o111 == 0 {
			return "", syscall.EACCES
		}
		return p, nil
	}
	return sh.shared.paths.lookup(name, sh.GetList("PATH"), sh.dir)
}

var errNotFound = errors.New("not found")

type pathCache struct {
	mu   sync.Mutex
	key  string
	hits map[string]string
	miss map[string]bool
}

func newPathCache() *pathCache {
	return &pathCache{hits: map[string]string{}, miss: map[string]bool{}}
}

func (c *pathCache) lookup(name string, path []string, dir string) (string, error) {
	key := strings.Join(path, "\x00")
	for _, d := range path {
		if !filepath.IsAbs(d) {
			// a relative entry ("." or "bin") finds different commands
			// in different directories
			key += "\x00\x00" + dir
			break
		}
	}
	c.mu.Lock()
	if key != c.key {
		c.key = key
		c.hits = map[string]string{}
		c.miss = map[string]bool{}
	}
	if p, ok := c.hits[name]; ok {
		c.mu.Unlock()
		if isExecutable(p) {
			return p, nil
		}
		// gone (an upgrade moved it, say): look again
		c.mu.Lock()
		delete(c.hits, name)
		c.mu.Unlock()
	} else if c.miss[name] {
		c.mu.Unlock()
		return "", errNotFound
	} else {
		c.mu.Unlock()
	}
	for _, d := range path {
		if d == "" {
			d = "."
		}
		if !filepath.IsAbs(d) {
			d = filepath.Join(dir, d)
		}
		p := filepath.Join(d, name)
		if !isExecutable(p) {
			continue
		}
		c.mu.Lock()
		c.hits[name] = p
		c.mu.Unlock()
		return p, nil
	}
	c.mu.Lock()
	c.miss[name] = true
	c.mu.Unlock()
	return "", errNotFound
}

// ranCommand forgets negative lookups: a new program may have appeared.
func (c *pathCache) ranCommand() {
	c.mu.Lock()
	if len(c.miss) > 0 {
		c.miss = map[string]bool{}
	}
	c.mu.Unlock()
}

func (c *pathCache) clear() {
	c.mu.Lock()
	c.hits = map[string]string{}
	c.miss = map[string]bool{}
	c.mu.Unlock()
}

// HasCommand reports whether name resolves to something runnable.
func (sh *Shell) HasCommand(name string) bool {
	if _, ok := sh.aliases[name]; ok {
		return true
	}
	if sh.lookupFunc(name) != nil {
		return true
	}
	if _, ok := sh.builtinFunc(name); ok {
		return true
	}
	_, err := sh.lookPath(name)
	return err == nil
}

// CommandKind describes what a name resolves to: alias, function,
// builtin, file or "".
func (sh *Shell) CommandKind(name string) string {
	if _, ok := sh.aliases[name]; ok {
		return "alias"
	}
	if _, ok := sh.funcs[name]; ok {
		return "function"
	}
	if sh.shared.autoload.has(sh, name) {
		return "function"
	}
	if _, ok := sh.builtinFunc(name); ok {
		return "builtin"
	}
	if _, err := sh.lookPath(name); err == nil {
		return "file"
	}
	return ""
}

// IsKeyword exposes the parser's keyword set.
func IsKeyword(s string) bool { return syntax.IsKeyword(s) }
