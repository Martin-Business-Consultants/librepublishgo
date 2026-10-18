package update

import (
	"os"
	"path/filepath"
	"testing"
)

// A release staged in a data directory, as Stage leaves it.
func stage(t *testing.T, dataDir, version string) string {
	t.Helper()
	bin := binaryIn(dataDir, version)
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// The hand-over: tried, confirmed by the release once it serves, chosen at
// every start while it's newer than the binary starting; a newer image
// leaves it behind.
func TestHandOver(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, ok := Choose(dir, "2.0.3"); ok {
		t.Fatal("nothing staged, but something chosen")
	}
	bin := stage(t, dir, "2.0.4")
	if err := Try(dir, "2.0.4"); err != nil {
		t.Fatal(err)
	}
	// The release starts: it's the one tried, so it isn't failed; it isn't
	// current yet either, so it hands over to nothing.
	if _, ok := Choose(dir, "2.0.4"); ok {
		t.Fatal("the release handed over to itself")
	}
	Confirm(dir, "2.0.4")
	if readMark(dir, "current") != "2.0.4" || readMark(dir, "trying") != "" {
		t.Fatalf("confirm: current %q, trying %q", readMark(dir, "current"), readMark(dir, "trying"))
	}
	// The container restarts on the image's 2.0.3: the release is newer, so it runs.
	if got, ok := Choose(dir, "2.0.3"); !ok || got != bin {
		t.Fatalf("restart: %q %v", got, ok)
	}
	// And it still is, from itself.
	if _, ok := Choose(dir, "2.0.4"); ok {
		t.Fatal("the current release handed over to itself")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("the running release's binary was removed: %v", err)
	}
	// Hoster deploys a 2.0.5 image: it's newer; the release is left behind.
	if _, ok := Choose(dir, "2.0.5"); ok {
		t.Fatal("an older release was chosen over a newer image")
	}
	if readMark(dir, "current") != "" {
		t.Fatal("the older release is still current")
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Fatal("the older release wasn't cleared away")
	}
}

// A release that never confirms itself is a failed one: the next start
// stays where it is and says so.
func TestFailedHandOver(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stage(t, dir, "2.0.4")
	if err := Try(dir, "2.0.4"); err != nil {
		t.Fatal(err)
	}
	// The release crashed before confirming; the container restarts on 2.0.3.
	if _, ok := Choose(dir, "2.0.3"); ok {
		t.Fatal("handed over to a release that never came up")
	}
	if v, at := Failed(dir); v != "2.0.4" || at.IsZero() {
		t.Fatalf("failed: %q %v", v, at)
	}
	// Trying again clears it.
	if err := Try(dir, "2.0.4"); err != nil {
		t.Fatal(err)
	}
	if v, _ := Failed(dir); v != "" {
		t.Fatalf("still failed: %q", v)
	}
}
