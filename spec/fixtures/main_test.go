package fixtures_test

import (
	"os"
	"testing"

	"github.com/writtendev/writ/internal/gittest"
)

func TestMain(m *testing.M) {
	cleanup := gittest.DisableAutoMaintenance()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
