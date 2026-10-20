package deploy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/outbound"
)

// A purge is signed as webhooks are: the body, and the timestamp with it.
func TestPurgeIsSigned(t *testing.T) {
	t.Parallel()
	type post struct {
		header http.Header
		body   []byte
	}
	got := make(chan post, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- post{r.Header.Clone(), b}
	}))
	t.Cleanup(srv.Close)
	settings := &memSettings{data: map[string]map[string]any{
		"frontend": {"render": "server", "webhook_url": srv.URL + "/purge", "secret": "s"},
	}}
	d := New(settings, slog.New(slog.DiscardHandler), func(context.Context, string, func(context.Context)) bool { return false })
	if _, err := d.Trigger(t.Context(), "publish"); err != nil {
		t.Fatal(err)
	}
	p := <-got
	mac := func(parts ...string) string {
		m := hmac.New(sha256.New, []byte("s"))
		for _, s := range parts {
			m.Write([]byte(s))
		}
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}
	ts := p.header.Get(outbound.TimestampHeader)
	if p.header.Get(outbound.SignatureHeader) != mac(string(p.body)) || ts == "" ||
		p.header.Get(outbound.SignatureV2Header) != mac(ts, ".", string(p.body)) || p.header.Get(outbound.DeliveryHeader) == "" {
		t.Fatalf("headers %v", p.header)
	}
}
