package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const token = "lpu_test"

// fakeSite is a site's API as the CLI uses it: the manifest, the tool
// catalogue, /mcp and the device login. verification is the address the
// device login gives to approve on ("" for the site's own).
func fakeSite(t *testing.T, verification string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	var polls atomic.Int32
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		authed := r.Header.Get("Authorization") == "Bearer "+token
		switch r.URL.Path {
		case "/api/device/code":
			v := verification
			if v == "" {
				v = srv.URL + "/device?code=ABCD"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"user_code": "ABCD", "device_code": "dev", "verification_url": v, "interval": 0.01, "expires_in": 30})
		case "/api/device/token":
			if polls.Add(1) < 2 {
				_, _ = w.Write([]byte(`{"status":"pending"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok","token":"` + token + `"}`))
		case "/api/manifest":
			if !authed {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized","message":"Sign in."}`))
				return
			}
			_, _ = w.Write([]byte(`{"user":"Alice","role":{"name":"Administrator"}}`))
		case "/api/agent/tools":
			_, _ = w.Write([]byte(`{"version":"2.7.0","tools":[{"name":"get_page","summary":"One page.","method":"GET","path":"/api/pages/{path}",
				"params":[{"name":"path","in":"path","type":"string","required":true},{"name":"expand","in":"query","type":"boolean"},{"name":"page","in":"body","type":"object"}]}]}`))
		case "/mcp":
			var msg struct {
				Params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			_ = json.NewDecoder(r.Body).Decode(&msg)
			text, _ := json.Marshal(map[string]any{"status": "ok", "summary": "Page " + msg.Params.Arguments["path"].(string) + ".",
				"data": map[string]any{"title": "About"}, "breadcrumbs": []map[string]any{{"description": "Publish it", "command": "librepublish publish_page about"}}})
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// useConfig points the profiles at a file in a temporary directory.
func useConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.json")
	t.Setenv("LIBREPUBLISH_CONFIG", path)
	t.Setenv("LIBREPUBLISH_URL", "")
	t.Setenv("LIBREPUBLISH_TOKEN", "")
	t.Setenv("LIBREPUBLISH_PROFILE", "")
	return path
}

func run(t *testing.T, stdin string, cmd string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := Run(t.Context(), Env{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errOut, Version: "dev"}, cmd, args)
	return out.String(), errOut.String(), err
}

func TestFlags(t *testing.T) {
	t.Parallel()
	profile, jsonOut, rest := flags([]string{"about", "--profile", "acme", "--json", "--expand"})
	if profile != "acme" || !jsonOut || !reflect.DeepEqual(rest, []string{"about", "--expand"}) {
		t.Fatalf("got %q %v %q", profile, jsonOut, rest)
	}
	if profile, _, _ := flags([]string{"--profile=b"}); profile != "b" {
		t.Fatalf("--profile=: %q", profile)
	}
}

func TestParseArgs(t *testing.T) {
	t.Parallel()
	var tool Tool
	if err := json.Unmarshal([]byte(`{"name":"get_page","params":[{"name":"path","in":"path","type":"string","required":true},
		{"name":"expand","in":"query","type":"boolean"},{"name":"page","in":"body","type":"object"},{"name":"limit","in":"query","type":"integer"}]}`), &tool); err != nil {
		t.Fatal(err)
	}
	env := Env{Stdin: strings.NewReader(`{"title":"About"}`)}
	got, err := parseArgs(env, tool, []string{"about", "--expand", "--page", "-", "--limit=5"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"path": "about", "expand": true, "page": map[string]any{"title": "About"}, "limit": 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, bad := range [][]string{{}, {"about", "--nope", "1"}, {"about", "--limit", "x"}, {"about", "extra"}} {
		if _, err := parseArgs(Env{}, tool, bad); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
}

// A tool's answer, in the envelope: for a person, the summary, the data
// and the next steps; with --json, as the site sent it.
func TestToolEnvelope(t *testing.T) {
	srv := fakeSite(t, "")
	useConfig(t)
	t.Setenv("LIBREPUBLISH_URL", srv.URL)
	t.Setenv("LIBREPUBLISH_TOKEN", token)
	out, _, err := run(t, "", "get-page", "about")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Page about.", `"title": "About"`, "Next:", "librepublish publish_page about"} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in:\n%s", want, out)
		}
	}
	out, _, err = run(t, "", "get_page", "about", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil || env["status"] != "ok" || env["breadcrumbs"] == nil {
		t.Fatalf("--json: %v %s", err, out)
	}
	if _, _, err := run(t, "", "no_such_tool"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unknown tool: %v", err)
	}
}

// A token piped in (--token -) signs in without showing up in argv, and the
// profiles file is its owner's alone, even one that was looser before.
func TestLoginTokenFromStdin(t *testing.T) {
	srv := fakeSite(t, "")
	path := useConfig(t)
	if err := os.WriteFile(path, []byte(`{"profiles":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := run(t, token+"\n", "login", srv.URL, "--profile", "local", "--token", "-")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `profile "local"`) || strings.Contains(errOut, "Warning") {
		t.Fatalf("out %q, stderr %q", out, errOut)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("profiles file mode %v (%v), want 0600", st.Mode().Perm(), err)
	}
	p, name, err := resolve("")
	if err != nil || name != "local" || p.Token != token || p.URL != srv.URL {
		t.Fatalf("resolved %q %+v %v", name, p, err)
	}
	if _, _, err := run(t, "", "login", srv.URL, "--profile", "x", "--token", "-"); err == nil {
		t.Fatal("signed in with nothing on stdin")
	}
}

// A token on the command line still works, with a warning.
func TestLoginTokenOnArgvWarns(t *testing.T) {
	srv := fakeSite(t, "")
	useConfig(t)
	_, errOut, err := run(t, "", "login", srv.URL, "--token="+token)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "--token -") {
		t.Fatalf("no warning: %q", errOut)
	}
}

// Login wants https, but for this machine.
func TestLoginRefusesPlainHTTP(t *testing.T) {
	useConfig(t)
	for _, site := range []string{"http://cms.example.com", "http://10.0.0.5:8080", "ftp://cms.example.com"} {
		if _, _, err := run(t, "", "login", site, "--token", token); err == nil || !strings.Contains(err.Error(), "https") {
			t.Fatalf("%s: %v", site, err)
		}
	}
	for host, want := range map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, "app.localhost": true, "example.com": false, "10.0.0.5": false} {
		if loopback(host) != want {
			t.Fatalf("loopback(%q) = %v", host, !want)
		}
	}
}

// The device login opens and shows only the site's own approval page.
func TestDeviceLoginChecksVerificationURL(t *testing.T) {
	srv := fakeSite(t, "")
	useConfig(t)
	out, _, err := run(t, "", "login", srv.URL, "--no-browser")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, srv.URL+"/device?code=ABCD") || !strings.Contains(out, "Signed in") {
		t.Fatalf("device login: %s", out)
	}

	for _, v := range []string{"https://evil.example/device", "http://127.0.0.1:1/device", "javascript:alert(1)"} {
		bad := fakeSite(t, v)
		useConfig(t)
		out, _, err := run(t, "", "login", bad.URL, "--no-browser")
		if err == nil || strings.Contains(out, v) {
			t.Fatalf("%s: err %v, out %q", v, err, out)
		}
	}
}

func TestSameSite(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		site, v string
		ok      bool
	}{
		{"https://cms.example.com", "https://cms.example.com/device?code=1", true},
		{"https://cms.example.com/sub", "https://CMS.example.com/device", true},
		{"https://cms.example.com", "http://cms.example.com/device", false},
		{"https://cms.example.com", "https://cms.example.com.evil.test/device", false},
		{"https://cms.example.com", "https://cms.example.com:8443/device", false},
		{"http://localhost:8080", "http://localhost:8080/device", true},
		{"https://cms.example.com", "/device", false},
	} {
		if err := sameSite(tc.site, tc.v); (err == nil) != tc.ok {
			t.Fatalf("sameSite(%s, %s) = %v", tc.site, tc.v, err)
		}
	}
}
