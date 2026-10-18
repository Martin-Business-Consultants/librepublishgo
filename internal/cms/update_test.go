package cms

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/update"
)

// The dashboard's notice: a release that didn't come up is said, for an
// Administrator, while this is older than it.
func TestUpdateFailedNotice(t *testing.T) {
	t.Parallel()
	s := newSiteWith(t, Config{Version: "2.0.5"})
	app := s.server.App()
	if n := app.updateNotice(); n != nil {
		t.Fatalf("a notice with nothing to say: %v", n)
	}
	if err := os.MkdirAll(update.ReleasesDir(app.cfg.DataDir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(update.ReleasesDir(app.cfg.DataDir), "failed"), []byte("2.0.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, ok := app.updateNotice().(Obj)
	if !ok || n.Get("failed") != "2.0.6" || n.Get("running") != "2.0.5" {
		t.Fatalf("notice: %v", app.updateNotice())
	}
	// Nobody but an Administrator sees it.
	if app.updateNoticeFor(nil) != nil {
		t.Fatal("shown to no one in particular")
	}
	// The update button needs a server that can hand over: tests' can't.
	s.must(501, s.do(s.admin, "POST", "/api/tools/update", map[string]any{}))
}
