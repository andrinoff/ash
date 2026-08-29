package history_test

import (
	"github.com/andrinoff/ash/internal/history"
	"github.com/andrinoff/ash/internal/shell"
)

// The interpreter reaches the history through shell.History.
var _ shell.History = (*history.Store)(nil)
