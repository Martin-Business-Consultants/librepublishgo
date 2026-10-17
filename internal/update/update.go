// Package update knows Libre Publish's releases: which is the newest (the
// admin's "a new version is available"), and how to install one over the
// running binary (`librepublish update`), checked against the release's
// checksums.txt. Releases are GitHub's, on the repository below.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is where releases are published.
const Repo = "Martin-Business-Consultants/librepublishgo"

// API is GitHub's API root (a test points it elsewhere).
var API = "https://api.github.com"

// Release is one published version.
type Release struct {
	Version     string    `json:"version"` // without the "v": 2.1.0
	URL         string    `json:"url"`     // its page, with the notes
	PublishedAt time.Time `json:"published_at"`
	assets      map[string]string
}

var client = &http.Client{Timeout: 30 * time.Second}

// Latest is the newest release.
func Latest(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, API+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("asking for the latest release: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return Release{}, errors.New("there are no releases yet")
	}
	if res.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("asking for the latest release: GitHub answered %s", res.Status)
	}
	var body struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("reading the latest release: %w", err)
	}
	r := Release{Version: strings.TrimPrefix(body.TagName, "v"), URL: body.HTMLURL, PublishedAt: body.PublishedAt, assets: map[string]string{}}
	for _, a := range body.Assets {
		r.assets[a.Name] = a.URL
	}
	return r, nil
}

// Newer is whether version a is after b ("2.1.0" > "2.0.3"). A build from
// between releases (git describe's "2.0.3-4-gabc") counts as the release it
// follows; one that isn't a version at all (dev) is never older.
func Newer(a, b string) bool {
	pa, okA := parse(a)
	pb, okB := parse(b)
	if !okA || !okB {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

// parse reads major.minor.patch, ignoring a leading v and anything after a
// "-" or "+" (pre-releases and build data aren't compared).
func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// AssetName is this machine's binary in a release.
func AssetName() string {
	name := "librepublish-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// InContainer is whether this runs in a container, where the image, not the
// binary, is what's updated.
func InContainer() bool {
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

// Install downloads r's binary for this machine, checks it against r's
// checksums.txt, and puts it in place of the binary at exe (the old one is
// kept beside it as <exe>.old until the next update).
func Install(ctx context.Context, r Release, exe string) error {
	name := AssetName()
	binURL, sumsURL := r.assets[name], r.assets["checksums.txt"]
	if binURL == "" || sumsURL == "" {
		return fmt.Errorf("release %s has no %s (or no checksums.txt)", r.Version, name)
	}
	sums, err := fetch(ctx, sumsURL, 1<<20)
	if err != nil {
		return err
	}
	want := ""
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("checksums.txt doesn't list %s", name)
	}
	bin, err := fetch(ctx, binURL, 512<<20)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("%s doesn't match its checksum (got %s, want %s): not installed", name, got, want)
	}
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".librepublish-update-*")
	if err != nil {
		return fmt.Errorf("writing next to %s: %w", exe, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil { //nolint:gosec // an executable
		return err
	}
	// The running binary can't be overwritten on every OS, but it can be
	// moved: aside first, then the new one in its place.
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("moving %s aside: %w", exe, err)
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return errors.Join(fmt.Errorf("putting the new binary in place: %w", err), os.Rename(old, exe))
	}
	return nil
}

func fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
}
