package cms

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tetratelabs/wazero"
	"golang.org/x/crypto/bcrypt"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
)

func TestMain(m *testing.M) {
	// Every test site hashes a password or two: at the production cost
	// that's most of a test's time, and the tests aren't about bcrypt.
	secret.PasswordCost = bcrypt.MinCost
	// Compiling a plugin takes seconds (tens, race-checked), and the tests
	// compile the same few: kept on disk, a later run skips it.
	cache, err := wazero.NewCompilationCacheWithDir(filepath.Join(os.TempDir(), "librepublish-test-wazero"))
	if err != nil {
		cache = wazero.NewCompilationCache()
	}
	plugin.CompilationCache = cache
	os.Exit(m.Run())
}
