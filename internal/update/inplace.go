package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// An in-place update, the admin's Update button: a release's binary is put
// in the site's own data directory (DATA_DIR/releases/<version>/), so it's
// that site's and survives the container, and the running CMS hands over to
// it. Every start then runs the newer of the binary it was started as (the
// image's, say) and the release there; a newer image deployed later simply
// takes over. A release is tried before it's trusted: the start that hands
// over marks it (trying); the release confirms itself once it's serving
// (current); a start that finds it still marked, unconfirmed, knows it
// failed, and stays on what it is.
//
//	DATA_DIR/releases/
//	  2.0.4/librepublish          the binary
//	  2.0.4/librepublish.sha256   its checksum, checked before each hand-over
//	  current                     the version confirmed to run
//	  trying                      the version handed over to, not confirmed yet
//	  failed                      a version that didn't come up, and when

// ReleasesDir is where a data directory's releases are.
func ReleasesDir(dataDir string) string { return filepath.Join(dataDir, "releases") }

func binaryIn(dataDir, version string) string {
	name := "librepublish"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(ReleasesDir(dataDir), version, name)
}

func readMark(dataDir, name string) string {
	b, err := os.ReadFile(filepath.Join(ReleasesDir(dataDir), name)) //nolint:gosec // the site's own data directory
	if err != nil {
		return ""
	}
	return strings.Fields(string(b) + " ")[0]
}

func writeMark(dataDir, name, value string) error {
	return os.WriteFile(filepath.Join(ReleasesDir(dataDir), name), []byte(value+"\n"), 0o600)
}

func removeMark(dataDir, name string) {
	_ = os.Remove(filepath.Join(ReleasesDir(dataDir), name))
}

// Stage downloads r's binary for this machine into dataDir's releases,
// checked against r's checksums.txt, and checks that it runs and is the
// version it says: the path to hand over to.
func Stage(ctx context.Context, r Release, dataDir string) (string, error) {
	bin, err := Download(ctx, r)
	if err != nil {
		return "", err
	}
	path := binaryIn(dataDir, r.Version)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", err
	}
	if err := place(path, bin); err != nil {
		return "", err
	}
	if err := Check(ctx, path, r.Version); err != nil {
		_ = os.RemoveAll(filepath.Dir(path))
		return "", err
	}
	return path, nil
}

// sumFile is where a staged binary's SHA-256 is recorded, beside it.
func sumFile(bin string) string { return bin + ".sha256" }

// place writes bin (checked against checksums.txt) at path, and its
// checksum beside it, which Choose checks the binary against before every
// hand-over: a binary changed or put there by something other than Stage
// isn't run.
func place(path string, bin []byte) error {
	sum := sha256.Sum256(bin)
	if err := writeAtomic(sumFile(path), []byte(hex.EncodeToString(sum[:])+"\n"), 0o600); err != nil {
		return err
	}
	return writeAtomic(path, bin, 0o755)
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// verify is whether the binary at bin is the one Stage put there: its
// SHA-256 is the one recorded beside it.
func verify(bin string) error {
	want, err := os.ReadFile(sumFile(bin))
	if err != nil {
		return fmt.Errorf("no checksum recorded for %s: %w", bin, err)
	}
	f, err := os.Open(bin) //nolint:gosec // the site's own data directory
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != strings.TrimSpace(string(want)) {
		return fmt.Errorf("%s doesn't match the checksum recorded when it was staged", bin)
	}
	return nil
}

// Check runs bin's `version` and wants version: a binary that can run here
// (not a mount that forbids it, not another machine's), and the one it says.
func Check(ctx context.Context, bin, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return fmt.Errorf("the new binary doesn't run here: %w", err)
	}
	if f := strings.Fields(string(out)); len(f) < 2 || strings.TrimPrefix(f[1], "v") != strings.TrimPrefix(version, "v") {
		return fmt.Errorf("the new binary says it's %q, not %s", strings.TrimSpace(string(out)), version)
	}
	return nil
}

// Try marks version as the one being handed over to.
func Try(dataDir, version string) error {
	removeMark(dataDir, "failed")
	return writeMark(dataDir, "trying", version)
}

// Confirm is what a release calls once it's serving: if it's the one being
// tried, it's current now. Any other start does nothing.
func Confirm(dataDir, self string) {
	if readMark(dataDir, "trying") != self {
		return
	}
	if err := writeMark(dataDir, "current", self); err == nil {
		removeMark(dataDir, "trying")
	}
}

// Failed is a release that was handed over to and didn't come up, and when
// that was found; "" when none.
func Failed(dataDir string) (version string, at time.Time) {
	path := filepath.Join(ReleasesDir(dataDir), "failed")
	st, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}
	}
	return readMark(dataDir, "failed"), st.ModTime()
}

// Choose is the binary a start should hand over to, if any: the current
// release when it's newer than self (the binary starting). A release tried
// and never confirmed is a failed one: it's recorded and passed over. A
// current release no newer than self (a newer image was deployed) is left
// behind, and releases older than self cleared away.
func Choose(dataDir, self string) (string, bool) {
	if trying := readMark(dataDir, "trying"); trying != "" && trying != self {
		_ = writeMark(dataDir, "failed", trying)
		removeMark(dataDir, "trying")
	}
	current := readMark(dataDir, "current")
	if current == "" {
		return "", false
	}
	if !Newer(current, self) {
		if current != self {
			removeMark(dataDir, "current")
			prune(dataDir, self)
		}
		return "", false
	}
	if failed := readMark(dataDir, "failed"); failed == current {
		return "", false
	}
	bin := binaryIn(dataDir, current)
	if _, err := os.Stat(bin); err != nil {
		return "", false
	}
	// Not one Stage verified, or changed since: recorded as failed, and
	// not run.
	if err := verify(bin); err != nil {
		_ = writeMark(dataDir, "failed", current)
		return "", false
	}
	return bin, true
}

// prune removes the releases older than self.
func prune(dataDir, self string) {
	entries, err := os.ReadDir(ReleasesDir(dataDir))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && Newer(self, e.Name()) {
			_ = os.RemoveAll(filepath.Join(ReleasesDir(dataDir), e.Name()))
		}
	}
}

// ErrNoExec is a platform where a process can't become another binary.
var ErrNoExec = errors.New("this platform can't hand over to another binary in place")
