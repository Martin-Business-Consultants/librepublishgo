package cms

import (
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/webhook"
)

// A sample for an external webhook has every value its body reads, and
// meets its conditions, so its preview looks like what it will send.
func TestSampleFor(t *testing.T) {
	h := &webhook.Hook{Kind: webhook.External,
		BodyMap:      store.JSON(`[{"key":"service_type","value":"values.service_type","transform":"slug"},{"key":"d","value":"values.delivery_date","transform":"date_us"},{"key":"email","value":"values.email"},{"key":"src","value":"web","transform":"text"}]`),
		EventFilters: store.JSON(`{"submission.created":{"form":["quote","other"]}}`)}
	d := sampleFor(h, "submission.created")
	if !h.Takes("submission.created", d) || d["form"] != "quote" {
		t.Fatalf("conditions: %v", d)
	}
	body := webhook.Body(webhook.Fields(h.BodyMap), d)
	if body["service_type"] != "sample-service-type" || body["email"] != "ada@example.com" || body["d"] != store.Now().Format("01/02/2006") || body["src"] != "web" {
		t.Fatalf("body: %v", body)
	}
}
