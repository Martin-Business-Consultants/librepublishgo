package secret

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestMain(m *testing.M) {
	PasswordCost = bcrypt.MinCost
	os.Exit(m.Run())
}

func TestLoad(t *testing.T) {
	t.Parallel()
	t.Run("makes a key file once, and reads it back", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		k1, err := Load(dir, "")
		if err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(filepath.Join(dir, "secret.key"))
		if err != nil {
			t.Fatal(err)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("secret.key is %o, want 600", perm)
		}
		k2, err := Load(dir, "")
		if err != nil {
			t.Fatal(err)
		}
		if plain, ok := k2.Open(k1.Seal("x")); !ok || plain != "x" {
			t.Errorf("a reloaded key can't open what the first sealed: %q %v", plain, ok)
		}
	})
	t.Run("SECRET_KEY wins, the same every time", func(t *testing.T) {
		t.Parallel()
		k1, err := Load(t.TempDir(), "hunter2")
		if err != nil {
			t.Fatal(err)
		}
		k2, err := Load(t.TempDir(), "hunter2")
		if err != nil {
			t.Fatal(err)
		}
		if plain, ok := k2.Open(k1.Seal("x")); !ok || plain != "x" {
			t.Errorf("the same SECRET_KEY can't open its own seal: %q %v", plain, ok)
		}
	})
	t.Run("a damaged key file is an error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte("not hex"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir, ""); err == nil {
			t.Error("loaded a key file that isn't 64 hex characters")
		}
	})
}

func testKey(t *testing.T) *Key {
	t.Helper()
	k, err := Load(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpen(t *testing.T) {
	t.Parallel()
	k := testKey(t)
	for _, plain := range []string{"", "lp_live_abc", "ünïcødé ✓", strings.Repeat("x", 1<<16)} {
		sealed := k.Seal(plain)
		if !strings.HasPrefix(sealed, SealedPrefix) {
			t.Errorf("Seal(%.20q) = %.40q", plain, sealed)
		}
		if got, ok := k.Open(sealed); !ok || got != plain {
			t.Errorf("Open(Seal(%.20q)) = %.20q, %v", plain, got, ok)
		}
	}
	if a, b := k.Seal("x"), k.Seal("x"); a == b {
		t.Error("two seals of the same value are the same: the nonce isn't random")
	}

	sealed := k.Seal("secret")
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, SealedPrefix))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	tampered := SealedPrefix + base64.RawStdEncoding.EncodeToString(raw)
	for name, in := range map[string]string{
		"another key's":        testKey(t).Seal("secret"),
		"tampered":             tampered,
		"unsealed":             "secret",
		"a Rails ciphertext":   `{"p":"abc","h":{"iv":"x","at":"y"}}`,
		"not base64":           SealedPrefix + "!!!",
		"shorter than a nonce": SealedPrefix + base64.RawStdEncoding.EncodeToString([]byte("short")),
	} {
		if got, ok := k.Open(in); ok || got != "" {
			t.Errorf("Open(%s) = %q, %v; want refused", name, got, ok)
		}
	}
}

func TestReveal(t *testing.T) {
	t.Parallel()
	k := testKey(t)
	for name, c := range map[string]struct{ in, want string }{
		"sealed":                {k.Seal("hunter22"), "hunter22"},
		"kept before sealing":   {"hunter22", "hunter22"},
		"another key's":         {testKey(t).Seal("hunter22"), ""},
		"empty":                 {"", ""},
		"masked, as it's shown": {"***r22", "***r22"},
	} {
		if got := k.Reveal(c.in); got != c.want {
			t.Errorf("Reveal(%s) = %q, want %q", name, got, c.want)
		}
	}
	if !Sealed(k.Seal("")) || Sealed("plain") {
		t.Error("Sealed doesn't tell a sealed value from a plain one")
	}
}

// FuzzSealOpen: any value comes back from its seal as it went in, and
// nothing a database row holds makes Open panic.
func FuzzSealOpen(f *testing.F) {
	k, err := Load(f.TempDir(), "fuzz")
	if err != nil {
		f.Fatal(err)
	}
	f.Add("x", k.Seal("x"))
	f.Add("", SealedPrefix)
	f.Add("\x00\xff", SealedPrefix+"AAAA")
	f.Fuzz(func(t *testing.T, plain, stored string) {
		if got, ok := k.Open(k.Seal(plain)); !ok || got != plain {
			t.Fatalf("Open(Seal(%q)) = %q, %v", plain, got, ok)
		}
		k.Open(stored)
	})
}

func TestToken(t *testing.T) {
	t.Parallel()
	urlSafe := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	a, b := Token(32), Token(32)
	if len(a) != 43 || !urlSafe.MatchString(a) || a == b {
		t.Errorf("Token(32) = %q, %q", a, b)
	}
}

func TestDigest(t *testing.T) {
	t.Parallel()
	// SHA-256 of "abc" (FIPS 180-2's example).
	if got := Digest("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("Digest(abc) = %s", got)
	}
}

func TestPasswords(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$2a$") {
		t.Errorf("hash %q isn't bcrypt", hash)
	}
	b, err := bcrypt.GenerateFromPassword([]byte("password1234"), bcrypt.MinCost+1)
	if err != nil {
		t.Fatal(err)
	}
	otherCost := string(b)
	tests := []struct {
		name, digest, pw string
		want             bool
	}{
		{"right", hash, "correct horse", true},
		{"wrong", hash, "correct horse ", false},
		{"empty", hash, "", false},
		{"not a hash", "plain", "plain", false},
		// A hash made at another cost (as Rails' are) still checks.
		{"another cost", otherCost, "password1234", true},
		{"the dummy", DummyHash(), "anything", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CheckPassword(tt.digest, tt.pw); got != tt.want {
				t.Errorf("CheckPassword = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKeyForms(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	k, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	sealed := k.Seal("hello")
	// A secret.key's contents, or the SECRET_KEY it stands for, as
	// Candidates reads either.
	fromFile, err := FromHex(k.Hex() + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := fromFile.Open(sealed); !ok || p != "hello" {
		t.Fatal("FromHex didn't round-trip Hex")
	}
	if _, err := FromHex("not hex"); err == nil {
		t.Fatal("FromHex took a non-key")
	}
	opened := 0
	for _, c := range Candidates(k.Hex()) {
		if _, ok := c.Open(sealed); ok {
			opened++
		}
	}
	if opened != 1 {
		t.Fatalf("candidates of a key file opened %d", opened)
	}
	env, _ := Load(t.TempDir(), "passphrase")
	cands := Candidates("passphrase")
	if len(cands) != 1 {
		t.Fatalf("a passphrase has %d candidates", len(cands))
	}
	if _, ok := cands[0].Open(env.Seal("x")); !ok {
		t.Fatal("FromPassphrase isn't SECRET_KEY's key")
	}
	if Candidates("  ") != nil {
		t.Fatal("blank has candidates")
	}
}

func TestPassphraseIsStretched(t *testing.T) {
	t.Parallel()
	k := FromPassphrase("passphrase")
	sum := sha256.Sum256([]byte("passphrase"))
	old := &Key{key: sum[:]}
	if k.Hex() == old.Hex() {
		t.Fatal("the passphrase's key is its bare SHA-256")
	}
	// What was sealed before still opens; what's sealed now doesn't need
	// the old key, and the old key alone can't open it.
	sealedBefore := old.Seal("before")
	if p, ok := k.Open(sealedBefore); !ok || p != "before" {
		t.Fatal("a value sealed under the old key doesn't open")
	}
	if _, ok := k.Current().Open(sealedBefore); ok {
		t.Fatal("the current key alone opened the old key's value")
	}
	now := k.Seal("now")
	if _, ok := old.Open(now); ok {
		t.Fatal("a new seal opened with the old key")
	}
	if p, ok := k.Current().Open(now); !ok || p != "now" {
		t.Fatal("a new seal needs the old key")
	}
	if k.Legacy().Hex() != old.Hex() || FromPassphrase("passphrase").Hex() != k.Hex() {
		t.Fatal("Legacy isn't the old key, or the stretch isn't the same twice")
	}
	if hexKey, _ := FromHex(k.Hex()); hexKey.Legacy() != nil {
		t.Fatal("a key file's key has a legacy key")
	}
}

func TestPasswordLimits(t *testing.T) {
	t.Parallel()
	if _, err := HashPassword(strings.Repeat("a", MaxPassword+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("a 73-byte password: %v", err)
	}
	if _, err := HashPassword(strings.Repeat("a", MaxPassword)); err != nil {
		t.Fatalf("a 72-byte password: %v", err)
	}
}
