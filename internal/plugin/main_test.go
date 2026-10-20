package plugin

import (
	"os"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/outbound"
)

func TestMain(m *testing.M) {
	// The tests' servers are httptest's, on 127.0.0.1.
	outbound.AllowPrivate(true)
	os.Exit(m.Run())
}
