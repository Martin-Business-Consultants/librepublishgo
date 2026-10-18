package datalock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckPersistent(t *testing.T) {
	t.Parallel()
	const root = "1 0 0:50 / / rw - overlay rootfs rw\n"
	for _, tc := range []struct {
		name, mountinfo string
		ok              bool
	}{
		{"a named volume", root + "2 1 8:1 /var/lib/docker/volumes/oldmill-data/_data /data rw - ext4 /dev/sda1 rw\n", true},
		{"a bind mount", root + "2 1 8:1 /srv/sites/oldmill /data rw - ext4 /dev/sda1 rw\n", true},
		{"nothing mounted", root, false},
		{"something mounted elsewhere", root + "2 1 8:1 /var/lib/docker/volumes/x/_data /other rw - ext4 /dev/sda1 rw\n", false},
		{"an anonymous volume", root + "2 1 8:1 /var/lib/docker/volumes/" + strings.Repeat("ab", 32) + "/_data /data rw - ext4 /dev/sda1 rw\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := CheckPersistent("/data", strings.NewReader(tc.mountinfo))
			if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrNotPersistent)) {
				t.Fatalf("CheckPersistent = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	// A mount point with a space, escaped as mountinfo does.
	if err := CheckPersistent("/my data", strings.NewReader(root+`2 1 8:1 /x /my\040data rw - ext4 /dev/sda1 rw`+"\n")); err != nil {
		t.Fatalf("escaped mount point: %v", err)
	}
}

func TestClaimForSite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := ClaimForSite(dir, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, siteFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("no site named, but one was recorded")
	}
	if err := ClaimForSite(dir, "oldmill"); err != nil {
		t.Fatal(err)
	}
	if err := ClaimForSite(dir, "oldmill"); err != nil {
		t.Fatalf("the same site again: %v", err)
	}
	if err := ClaimForSite(dir, "acme"); !errors.Is(err, ErrOtherSite) {
		t.Fatalf("another site: %v", err)
	}
	if err := ClaimForSite(dir, ""); err != nil {
		t.Fatalf("no site named: %v", err)
	}
}
