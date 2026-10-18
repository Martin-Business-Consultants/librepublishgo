package datalock

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The checks serve makes of its data directory before it uses it, so a
// deploy can't quietly lose a site, or give it another's: in a container,
// the directory must be a volume that outlives the container; and a
// directory a site has used is that site's.

// ErrNotPersistent is a data directory a container would lose: no volume
// mounted on it, or one Docker made for this container alone.
var ErrNotPersistent = errors.New("the data directory isn't on a persistent volume")

// ErrOtherSite is a data directory another site (another SITE_KEY) uses.
var ErrOtherSite = errors.New("the data directory belongs to another site")

// Docker's anonymous volumes: a 64-hex name, made per container.
var anonymousVolume = regexp.MustCompile(`/volumes/[0-9a-f]{64}/_data$`)

// CheckPersistent is whether dir, in a container, is a volume of its own
// (mountinfo is /proc/self/mountinfo's content). A directory that isn't a
// mount point is the container's own layer, and an anonymous volume is made
// afresh with every container: either is gone at the next deploy.
func CheckPersistent(dir string, mountinfo io.Reader) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	sc := bufio.NewScanner(mountinfo)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		// id parent major:minor root mountpoint options … - fstype source superopts
		f := strings.Fields(sc.Text())
		if len(f) < 5 || unescape(f[4]) != abs {
			continue
		}
		if anonymousVolume.MatchString(unescape(f[3])) {
			return fmt.Errorf("%w: %s is an anonymous volume, made for this container alone and left behind by the next one; mount a named volume there (one per site, e.g. -v mysite-data:%s)", ErrNotPersistent, abs, abs)
		}
		return nil
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: nothing is mounted at %s, so everything in it goes with this container at the next deploy; mount a named volume there (one per site, e.g. -v mysite-data:%s), or set EPHEMERAL_DATA=1 for a trial that may lose it", ErrNotPersistent, abs, abs)
}

// mountinfo escapes spaces and kin as \040 and the like.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// siteFile names the site a data directory is for.
const siteFile = ".site"

// ClaimForSite records that dir is site's (SITE_KEY), the first time, and
// refuses it after that to any other site. With no site named, nothing is
// checked or recorded.
func ClaimForSite(dir, site string) error {
	site = strings.TrimSpace(site)
	if site == "" {
		return nil
	}
	path := filepath.Join(dir, siteFile)
	raw, err := os.ReadFile(path) //nolint:gosec // the operator's own data directory
	switch {
	case errors.Is(err, os.ErrNotExist):
		return os.WriteFile(path, []byte(site+"\n"), 0o600)
	case err != nil:
		return err
	}
	if owner := strings.TrimSpace(string(raw)); owner != site {
		return fmt.Errorf("%w: %s is %q's (its %s), and this is %q; give each site its own volume, or remove %s if this directory really is meant for %q now", ErrOtherSite, dir, owner, siteFile, site, path, site)
	}
	return nil
}
