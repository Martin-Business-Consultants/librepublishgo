package cms

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/secret"
)

func TestMain(m *testing.M) {
	// Every test site hashes a password or two: at the production cost
	// that's most of a test's time, and the tests aren't about bcrypt.
	secret.PasswordCost = bcrypt.MinCost
	os.Exit(m.Run())
}
