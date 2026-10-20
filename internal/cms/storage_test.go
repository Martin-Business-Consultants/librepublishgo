package cms

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uploadAs posts a file with the Content-Type its part claims.
func (s *site) uploadAs(name, contentType string, content []byte) resp {
	s.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	h.Set("Content-Type", contentType)
	pw, _ := mw.CreatePart(h)
	pw.Write(content)
	mw.Close()
	return s.do(s.admin, "POST", "/api/assets", body.Bytes(), "Content-Type", mw.FormDataContentType())
}

// A blob row whose key is a path (from an import, say) can't be used to
// read or delete a file outside storage/.
func TestBlobKeysCantLeaveStorage(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	id := jsonID(s.upload("mill.png", pngOf(t, 2000, 100)))
	// Where the key below would have led: storage/ab/./ab/../../../victim.
	victim := filepath.Join(s.app.cfg.DataDir, "victim")
	if err := os.WriteFile(victim, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.app.store.ExecContext(t.Context(), `UPDATE active_storage_blobs SET key = 'ab/../../../victim'`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/files/ab%2F..%2F..%2F..%2Fvictim/mill.png", "/files/ab%2F..%2F..%2F..%2Fvictim/w640/mill.png"} {
		r := s.do("", "GET", p, nil)
		if r.Status != http.StatusNotFound || strings.Contains(string(r.Raw), "private") {
			t.Fatalf("%s: %d %s", p, r.Status, r.Raw)
		}
	}
	s.must(204, s.do(s.admin, "DELETE", "/api/assets/"+id+"?permanent=1", nil))
	if got, err := os.ReadFile(victim); err != nil || string(got) != "private" {
		t.Fatalf("victim: %q %v", got, err)
	}
}

// HTML is refused however it's named or typed; what can carry script (SVG)
// downloads rather than shows.
func TestUploadTypesAndInlineFiles(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	for _, c := range []struct{ name, ct string }{
		{"page.htm", "application/octet-stream"},
		{"page.xhtml", ""},
		{"page.HTML", "image/png"},
		{"notes.txt", "Text/HTML; charset=utf-8"},
		{"notes.txt", "application/xhtml+xml"},
	} {
		if r := s.uploadAs(c.name, c.ct, []byte("<script>alert(1)</script>")); r.Status != 422 {
			t.Errorf("%s as %q: %d %s", c.name, c.ct, r.Status, r.Raw)
		}
	}

	fetch := func(r resp) http.Header {
		t.Helper()
		res, err := get(t, str(dig(s.must(201, r).Body, "asset", "url")))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.Header
	}
	svg := fetch(s.uploadAs("logo.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)))
	if !strings.HasPrefix(svg.Get("Content-Disposition"), "attachment") || !strings.Contains(svg.Get("Content-Security-Policy"), "sandbox") ||
		svg.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("svg: %v", svg)
	}
	png := fetch(s.uploadAs("mill.png", "IMAGE/PNG", pngOf(t, 10, 10)))
	if png.Get("Content-Disposition") != "" || png.Get("Content-Type") != "image/png" {
		t.Fatalf("png: %v", png)
	}
}

// An asset's alt text and caption show wherever live content uses it: a
// role that can't publish that content can't change them either.
func TestLiveAssetEditsNeedPublish(t *testing.T) {
	t.Parallel()
	s := newSite(t)
	used, draft := jsonID(s.upload("hero.png", pngOf(t, 10, 10))), jsonID(s.upload("draft.png", pngOf(t, 10, 10)))
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "home", "title": "Home", "status": "published",
		"blocks": []any{map[string]any{"type": "hero", "data": map[string]any{"heading": "Hi", "image_id": used}}}}}))
	s.must(201, s.do(s.admin, "POST", "/api/pages", map[string]any{"page": map[string]any{"slug": "soon", "title": "Soon", "status": "draft",
		"blocks": []any{map[string]any{"type": "hero", "data": map[string]any{"heading": "Hi", "image_id": draft}}}}}))

	edit := func(token, id string, p map[string]any) resp {
		return s.do(token, "PATCH", "/api/assets/"+id, map[string]any{"asset": p})
	}
	r := s.must(403, edit(s.agent, used, map[string]any{"alt": "Rude words"}))
	if r.Body["capability"] != "pages:publish" {
		t.Fatalf("refusal: %s", r.Raw)
	}
	s.must(403, edit(s.agent, used, map[string]any{"caption": "Rude words"}))
	s.must(403, edit(s.agent, used, map[string]any{"focal_x": 0.1}))
	// Library housekeeping, and an unchanged alt, aren't what the site shows.
	s.must(200, edit(s.agent, used, map[string]any{"folder": "/heroes", "name": "Hero", "alt": ""}))
	s.must(200, edit(s.agent, draft, map[string]any{"alt": "A draft's"}))
	s.must(200, edit(s.admin, used, map[string]any{"alt": "The mill"}))
	if r := s.must(200, s.do(s.admin, "GET", "/api/assets/"+used, nil)); dig(r.Body, "asset", "alt") != "The mill" || dig(r.Body, "asset", "folder") != "/heroes" {
		t.Fatalf("asset: %s", r.Raw)
	}
}
