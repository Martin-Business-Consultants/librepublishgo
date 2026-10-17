package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"2.1.0", "2.0.9", true},
		{"v2.0.10", "2.0.9", true},
		{"2.0.0", "2.0.0", false},
		{"1.9.9", "2.0.0", false},
		{"3.0.0", "2.99.99", true},
		{"2.1.0", "dev", false},
		{"2.1.0", "1.33.2-30-g9ee3d13", true}, // commits after 1.33.2
		{"1.33.2", "1.33.2-30-g9ee3d13", false},
		{"2.1.0-rc.1", "2.0.0", true},
		{"x.y.z", "1.0.0", false},
	} {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// A release's binary replaces the one at exe only when it matches its
// checksum.
func TestInstall(t *testing.T) {
	t.Parallel()
	good := []byte("the new librepublish")
	sum := sha256.Sum256(good)
	for _, tc := range []struct {
		name   string
		served []byte
		ok     bool
	}{
		{"matches", good, true},
		{"tampered", []byte("something else"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/bin":
					w.Write(tc.served)
				case "/sums":
					fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), AssetName())
				}
			}))
			defer srv.Close()
			exe := filepath.Join(t.TempDir(), "librepublish")
			if err := os.WriteFile(exe, []byte("the old one"), 0o755); err != nil {
				t.Fatal(err)
			}
			r := Release{Version: "9.9.9", assets: map[string]string{AssetName(): srv.URL + "/bin", "checksums.txt": srv.URL + "/sums"}}
			err := Install(context.Background(), r, exe)
			got, _ := os.ReadFile(exe)
			if tc.ok && (err != nil || string(got) != string(good)) {
				t.Fatalf("installed %q, %v", got, err)
			}
			if !tc.ok && (err == nil || string(got) != "the old one") {
				t.Fatalf("a binary that doesn't match was installed: %q, %v", got, err)
			}
		})
	}
}
