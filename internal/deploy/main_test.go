package deploy

import (
	"os"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/outbound"
)

func TestMain(m *testing.M) {
	// The tests' hooks are httptest servers, on 127.0.0.1.
	outbound.AllowPrivate(true)
	os.Exit(m.Run())
}
