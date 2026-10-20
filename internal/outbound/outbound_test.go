package outbound

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The tests here set AllowPrivate, which is the process's: none of them
// runs in parallel.

// allowing runs fn with AllowPrivate on, putting it back after.
func allowing(t *testing.T, on bool) {
	t.Helper()
	was := allowPrivate.Load()
	AllowPrivate(on)
	t.Cleanup(func() { AllowPrivate(was) })
}

func TestBlocked(t *testing.T) {
	never := []string{
		"169.254.169.254", "169.254.0.1", "fe80::1", "fe80::1%eth0", "0.0.0.0", "0.1.2.3", "::",
		"224.0.0.1", "ff02::1", "255.255.255.255", "240.0.0.1",
		"fd00:ec2::254", "100.100.100.200",
		"::ffff:169.254.169.254", "::ffff:100.100.100.200", "::ffff:0.0.0.0",
		"64:ff9b::a9fe:a9fe", // NAT64 for 169.254.169.254
		"2002:a9fe:a9fe::1",  // 6to4 for it
	}
	private := []string{
		"127.0.0.1", "127.1.2.3", "::1", "10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"100.64.0.1", "100.127.255.254", "fc00::1", "fd12:3456::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:192.168.1.1", "::ffff:100.64.0.1",
		"64:ff9b::a00:1", "64:ff9b::808:808", "64:ff9b:1::1",
	}
	public := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "::ffff:8.8.8.8", "100.128.0.1", "172.32.0.1"}
	check := func(addrs []string, want bool) {
		t.Helper()
		for _, a := range addrs {
			if got := blocked(netip.MustParseAddr(a)); got != want {
				t.Errorf("allowPrivate %v: blocked(%s) = %v", allowPrivate.Load(), a, got)
			}
		}
	}
	allowing(t, false)
	check(never, true)
	check(private, true)
	check(public, false)

	AllowPrivate(true)
	check(never, true)
	check(private[:len(private)-3], false)
	// NAT64 goes where the address it embeds does.
	check([]string{"64:ff9b::a00:1", "64:ff9b::808:808"}, false)
	check(public, false)
}

func TestCheckURL(t *testing.T) {
	allowing(t, false)
	for _, u := range []string{"http://169.254.169.254/latest/meta-data/", "http://[fe80::1]/", "http://0.0.0.0:8080/",
		"http://127.0.0.1/", "http://[::1]/", "http://10.1.2.3/", "http://[fd00:ec2::254]/", "ftp://example.com/"} {
		if err := CheckURL(u); err == nil {
			t.Errorf("CheckURL(%q) allowed it", u)
		}
	}
	// A private address says how it's allowed; metadata doesn't.
	if err := CheckURL("http://127.0.0.1/"); err == nil || !strings.Contains(err.Error(), "OUTBOUND_ALLOW_PRIVATE") {
		t.Errorf("loopback: %v", err)
	}
	if err := CheckURL("http://169.254.169.254/"); err == nil || strings.Contains(err.Error(), "OUTBOUND_ALLOW_PRIVATE") {
		t.Errorf("metadata: %v", err)
	}
	if err := CheckURL("https://example.com/hook"); err != nil {
		t.Errorf("a public host: %v", err)
	}
	AllowPrivate(true)
	if err := CheckURL("http://127.0.0.1:4321/hook"); err != nil {
		t.Errorf("loopback, allowed: %v", err)
	}
	if err := CheckURL("http://169.254.169.254/"); err == nil {
		t.Errorf("metadata, allowed private: allowed")
	}
}

func get(t *testing.T, u string) (int, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Client.Do(req)
	if err != nil {
		return 0, err
	}
	res.Body.Close()
	return res.StatusCode, nil
}

// What the dialer sees decides, whatever the URL said: loopback by name is
// refused unless the operator allows it.
func TestDialerChecksTheAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	byName := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)

	allowing(t, false)
	if _, err := get(t, byName); err == nil || !strings.Contains(err.Error(), "may reach") {
		t.Fatalf("loopback by name: %v", err)
	}
	AllowPrivate(true)
	if _, err := get(t, byName); err != nil {
		t.Fatalf("loopback by name, allowed: %v", err)
	}
}

// A redirect stays on the host first asked, never goes from https to
// http, and never to an address that's refused.
func TestRedirects(t *testing.T) {
	allowing(t, true)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/ok", http.StatusTemporaryRedirect)
		case "/metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		case "/elsewhere":
			http.Redirect(w, r, strings.Replace("http://"+r.Host+"/ok", "127.0.0.1", "localhost", 1), http.StatusTemporaryRedirect)
		default:
			hits.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	if status, err := get(t, srv.URL+"/same"); err != nil || status != http.StatusNoContent {
		t.Fatalf("same host: %v", err)
	}
	if _, err := get(t, srv.URL+"/metadata"); err == nil {
		t.Fatalf("to link-local: followed")
	}
	// Same host or not, an address that's refused is.
	if err := CheckRedirect(&http.Request{URL: &url.URL{Scheme: "http", Host: "169.254.169.254"}},
		[]*http.Request{{URL: &url.URL{Scheme: "http", Host: "169.254.169.254:8080"}}}); err == nil || !strings.Contains(err.Error(), "may reach") {
		t.Fatalf("to link-local on its own host: %v", err)
	}
	before := hits.Load()
	if _, err := get(t, srv.URL+"/elsewhere"); err == nil || !strings.Contains(err.Error(), "another host") {
		t.Fatalf("to another host: %v", err)
	}
	if hits.Load() != before {
		t.Fatalf("the other host was reached")
	}

	// https to http is refused, whatever the host.
	from, _ := url.Parse("https://hooks.example/a")
	to, _ := url.Parse("http://hooks.example/b")
	err := CheckRedirect(&http.Request{URL: to}, []*http.Request{{URL: from}})
	if err == nil || !strings.Contains(err.Error(), "https to http") {
		t.Fatalf("downgrade: %v", err)
	}
	up, _ := url.Parse("https://hooks.example/b")
	if err := CheckRedirect(&http.Request{URL: up}, []*http.Request{{URL: to}}); err != nil {
		t.Fatalf("http to https on the same host: %v", err)
	}
}

// HTTP(S)_PROXY isn't used: through a proxy, the dialer would check only
// the proxy's address.
func TestNoProxy(t *testing.T) {
	allowing(t, true)
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	status, err := get(t, srv.URL)
	if err != nil || status != http.StatusOK || proxied.Load() != 0 {
		t.Fatalf("status %d, err %v, proxied %d", status, err, proxied.Load())
	}
}

// The v2 signature is over "<timestamp>.<body>"; the original one stays
// over the body.
func TestSign(t *testing.T) {
	body := []byte(`{"event":"page.published"}`)
	h := http.Header{}
	SignAt(h, "s3cret", body, "d-1", time.Unix(1700000000, 0))
	mac := func(msg string) string {
		m := hmac.New(sha256.New, []byte("s3cret"))
		m.Write([]byte(msg))
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}
	if h.Get(SignatureHeader) != mac(string(body)) {
		t.Errorf("signature %s", h.Get(SignatureHeader))
	}
	if h.Get(TimestampHeader) != "1700000000" || h.Get(DeliveryHeader) != "d-1" {
		t.Errorf("timestamp %s, delivery %s", h.Get(TimestampHeader), h.Get(DeliveryHeader))
	}
	if h.Get(SignatureV2Header) != mac("1700000000."+string(body)) {
		t.Errorf("v2 %s", h.Get(SignatureV2Header))
	}
	if a, b := NewDeliveryID(), NewDeliveryID(); len(a) != 32 || a == b {
		t.Errorf("ids %s %s", a, b)
	}
}
