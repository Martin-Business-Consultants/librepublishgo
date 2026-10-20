// Package secret is the install's key and what it's used for: sealing
// values that must be shown again, random tokens, digests, and password
// hashes.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/scrypt"
)

// Key is the install's key (DATA_DIR/secret.key, made on first run). It
// encrypts what has to be shown again: token plaintexts, settings'
// secrets. A Rails database's encrypted values can't be read without Rails'
// key; they still authenticate (by digest) and show as unreadable.
type Key struct {
	key []byte
	// legacy is what a SECRET_KEY passphrase was before it was stretched
	// (its SHA-256): values sealed then still open with it.
	legacy []byte
}

// Load uses key (SECRET_KEY) when it's set, dir/secret.key
// otherwise.
func Load(dir, key string) (*Key, error) {
	path := filepath.Join(dir, "secret.key")
	if key != "" {
		return FromPassphrase(key), nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the data directory's own key file
	if errors.Is(err, os.ErrNotExist) {
		raw = []byte(hex.EncodeToString(RandomBytes(32)))
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	k, err := FromHex(string(raw))
	if err != nil {
		return nil, errors.New("secret.key: expected 64 hex characters")
	}
	return k, nil
}

// FromHex is the key a secret.key file holds: 64 hex characters.
func FromHex(s string) (*Key, error) {
	sealKey, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(sealKey) != 32 {
		return nil, errors.New("a key is 64 hex characters")
	}
	return &Key{key: sealKey}, nil
}

// passphraseSalt is scrypt's salt for a SECRET_KEY: fixed, since the same
// passphrase must make the same key on every start and every machine. What
// it buys is the work factor: a guess at the passphrase costs scrypt's, not
// one SHA-256.
var passphraseSalt = []byte("librepublish SECRET_KEY v2")

// FromPassphrase is the key SECRET_KEY=s stands for: s stretched with
// scrypt. What was sealed under the key it stood for before (its SHA-256)
// still opens; Seal only ever uses the stretched one.
func FromPassphrase(s string) *Key {
	sum := sha256.Sum256([]byte(s))
	stretched, err := scrypt.Key([]byte(s), passphraseSalt, 1<<15, 8, 1, 32)
	if err != nil {
		panic(err) // only for parameters out of range, which these aren't
	}
	return &Key{key: stretched, legacy: sum[:]}
}

// Legacy is the key a SECRET_KEY stood for before it was stretched, to
// re-seal what it sealed; nil for any other key.
func (s *Key) Legacy() *Key {
	if s.legacy == nil {
		return nil
	}
	return &Key{key: s.legacy}
}

// Current is the key without its legacy one: what Seal uses, and the only
// one that opens.
func (s *Key) Current() *Key { return &Key{key: s.key} }

// Candidates are the keys s could mean: a secret.key file's contents, or
// a SECRET_KEY value (which may itself look like hex).
func Candidates(s string) []*Key {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []*Key
	if k, err := FromHex(s); err == nil {
		out = append(out, k)
	}
	return append(out, FromPassphrase(s))
}

// Hex is the key as a secret.key file holds it.
func (s *Key) Hex() string { return hex.EncodeToString(s.key) }

// SealedPrefix starts every value Seal makes.
const SealedPrefix = "go1:"

// Seal encrypts plain with the key (AES-GCM).
func (s *Key) Seal(plain string) string {
	block, _ := aes.NewCipher(s.key)
	gcm, _ := cipher.NewGCM(block)
	nonce := RandomBytes(gcm.NonceSize())
	return SealedPrefix + base64.RawStdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil))
}

// Sealed is whether v is a value Seal made, under any key.
func Sealed(v string) bool { return strings.HasPrefix(v, SealedPrefix) }

// Reveal is v's plaintext: v itself when it isn't sealed (a value kept
// before it was), "" when another key sealed it.
func (s *Key) Reveal(v string) string {
	if !Sealed(v) {
		return v
	}
	plain, _ := s.Open(v)
	return plain
}

// Open returns the plaintext, ok false for a value this key didn't seal.
// GCM authenticates, so a value tried under the wrong key fails rather
// than opening as noise: the legacy key is tried after the current one.
func (s *Key) Open(sealed string) (string, bool) {
	if !strings.HasPrefix(sealed, SealedPrefix) {
		return "", false
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, SealedPrefix))
	if err != nil {
		return "", false
	}
	if plain, ok := open(s.key, raw); ok {
		return plain, true
	}
	if s.legacy != nil {
		return open(s.legacy, raw)
	}
	return "", false
}

func open(key, raw []byte) (string, bool) {
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	if len(raw) < gcm.NonceSize() {
		return "", false
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", false
	}
	return string(plain), true
}

// RandomBytes is n bytes from crypto/rand.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// Token is n random bytes, base64url without padding.
func Token(n int) string { return base64.RawURLEncoding.EncodeToString(RandomBytes(n)) }

// Digest is s's SHA-256, in hex: how tokens are stored.
func Digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// PasswordCost is bcrypt's cost for the hashes HashPassword makes. Tests
// lower it (before any run): at 12 a hash takes a quarter of a second.
var PasswordCost = 12

// MaxPassword is the longest password bcrypt takes, in bytes.
const MaxPassword = 72

// ErrPasswordTooLong is a password over MaxPassword bytes.
var ErrPasswordTooLong = bcrypt.ErrPasswordTooLong

// slots caps how many bcrypt hashes run at once: one per CPU. A flood of
// sign-ins waits its turn instead of starving every other request.
var slots = make(chan struct{}, runtime.NumCPU())

// HashPassword is pw's bcrypt hash; ErrPasswordTooLong over 72 bytes.
func HashPassword(pw string) (string, error) {
	if len(pw) > MaxPassword {
		return "", ErrPasswordTooLong
	}
	slots <- struct{}{}
	defer func() { <-slots }()
	b, err := bcrypt.GenerateFromPassword([]byte(pw), PasswordCost)
	return string(b), err
}

// dummyHash is what an unknown email's password is checked against, so it
// takes as long to refuse as a wrong password does and timing doesn't say
// which addresses have accounts.
var dummyHash = sync.OnceValue(func() string {
	h, err := HashPassword(Token(16))
	if err != nil {
		panic(err)
	}
	return h
})

// DummyHash is the hash an unknown email's password is checked against.
func DummyHash() string { return dummyHash() }

// CheckPassword reads Rails' has_secure_password digests as they are: both
// are bcrypt.
func CheckPassword(digest, pw string) bool {
	slots <- struct{}{}
	defer func() { <-slots }()
	return bcrypt.CompareHashAndPassword([]byte(digest), []byte(pw)) == nil
}
