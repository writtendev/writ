package codec_test

import (
	"os"
	"testing"

	"github.com/writtendev/writ/internal/gittest"
)

func TestMain(m *testing.M) {
	gittest.DisableAutoMaintenance()
	os.Exit(m.Run())
}
