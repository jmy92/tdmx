package gui

import (
	"os"
	"path/filepath"
)

// DBPath returns the engine database path (~/.tdm/tdm.db), shared with the TUI.
func DBPath() string {
	homeDir, _ := os.UserHomeDir()

	return filepath.Join(homeDir, ".tdm", "tdm.db")
}
