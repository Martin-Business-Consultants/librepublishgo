package plugin

import (
	"net/http"
	"strings"
	"testing"
)

// A granted host is dialed as webhooks are: an address that's never
// reachable (a cloud's metadata service) is refused all the same.
func TestHTTPChecksTheAddress(t *testing.T) {
	t.Parallel()
	api := &API{grants: Grants{HTTP: []string{"169.254.169.254"}}}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	res, err := api.HTTP().Do(req)
	if err == nil {
		res.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "may reach") {
		t.Fatalf("metadata: %v", err)
	}
}
