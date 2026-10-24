//go:build !darwin

package repl

import (
	"os"
	"testing"
)

func openPTY(t *testing.T) (master, slave *os.File) {
	t.Skip("pseudo-terminal tests only run on darwin")
	return nil, nil
}
