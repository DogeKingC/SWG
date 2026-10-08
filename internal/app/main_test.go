package app

import (
	"os"
	"testing"

	"github.com/Trlydev/SWG/internal/preserve"
	"github.com/Trlydev/SWG/internal/workshop"
)

// Tests never reach the real Open Workshop or pre-worm archive.
func TestMain(m *testing.M) {
	undoP := preserve.SetForTest(&preserve.Archive{Records: map[string]*preserve.Record{}})
	undoW := workshop.SetIndexForTest(&workshop.Index{})
	code := m.Run()
	undoP()
	undoW()
	os.Exit(code)
}
