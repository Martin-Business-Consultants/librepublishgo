package webhook

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

var submission = map[string]any{
	"form": "quote", "submission_id": "s1",
	"values": map[string]any{"service_type": "Move It", "delivery_date": "2026-11-01", "zip": "32207", "guests": "4",
		"interests": []any{"News", "Big Events"}},
	"hook": map[string]any{"quote_id": "Q-42"},
}

func TestBody(t *testing.T) {
	got := Body([]Field{
		{Key: "service_type", Value: "values.service_type", Transform: "slug"},
		{Key: "initial_start_date", Value: "values.delivery_date", Transform: "date_us"},
		{Key: "initial_zip", Value: "values.zip"},
		{Key: "fd_zip", Value: "values.destination_zip"},
		{Key: "guests", Value: "values.guests", Transform: "number"},
		{Key: "none", Value: "values.missing", Transform: "number"},
		{Key: "interests", Value: "values.interests", Transform: "uppercase"},
		{Key: "quote", Value: "hook.quote_id", Transform: "lowercase"},
		{Key: "source", Value: "website", Transform: "text"},
		{Key: "form", Value: "form"},
	}, submission)
	want := map[string]any{"service_type": "move-it", "initial_start_date": "11/01/2026", "initial_zip": "32207", "fd_zip": "",
		"guests": 4.0, "none": nil, "interests": []any{"NEWS", "BIG EVENTS"}, "quote": "q-42", "source": "website", "form": "quote"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("body\n got %#v\nwant %#v", got, want)
	}
	// With no fields, the data as it is.
	if got := Body(nil, submission); got["submission_id"] != "s1" {
		t.Fatalf("no fields: %#v", got)
	}
}

func TestUSDate(t *testing.T) {
	for in, want := range map[string]string{"2026-11-01": "11/01/2026", "2026-11-01T09:00:00Z": "11/01/2026", "soon": "soon", "": ""} {
		if got := usDate(in); got != want {
			t.Errorf("usDate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTakes(t *testing.T) {
	hook := func(events, filters string) *Hook {
		return &Hook{Events: store.JSON(events), EventFilters: store.JSON(filters)}
	}
	cases := []struct {
		name  string
		h     *Hook
		event string
		takes bool
	}{
		{"not its event", hook(`["page.published"]`, `{}`), "submission.created", false},
		{"its event", hook(`["submission.created"]`, `{}`), "submission.created", true},
		{"every event", hook(`["*"]`, `{}`), "submission.created", true},
		{"the form", hook(`["submission.created"]`, `{"submission.created":{"form":["contact","quote"]}}`), "submission.created", true},
		{"another form", hook(`["submission.created"]`, `{"submission.created":{"form":["contact"]}}`), "submission.created", false},
		{"a nested value", hook(`["submission.created"]`, `{"submission.created":{"values.service_type":["Move It"]}}`), "submission.created", true},
		{"one of a list", hook(`["submission.created"]`, `{"submission.created":{"values.interests":["News"]}}`), "submission.created", true},
		{"every filter must pass", hook(`["submission.created"]`, `{"submission.created":{"form":["quote"],"values.zip":["00000"]}}`), "submission.created", false},
		{"another event's filter", hook(`["*"]`, `{"page.published":{"slug":["about"]}}`), "submission.created", true},
	}
	for _, c := range cases {
		if got := c.h.Takes(c.event, submission); got != c.takes {
			t.Errorf("%s: Takes = %v", c.name, got)
		}
	}
}

func TestFields(t *testing.T) {
	want := []Field{{Key: "a", Value: "values.a", Transform: "slug"}}
	b, _ := json.Marshal(want)
	if got := Fields(store.JSON(b)); !reflect.DeepEqual(got, want) {
		t.Fatalf("Fields: %#v", got)
	}
	if got := Fields(nil); len(got) != 0 {
		t.Fatalf("empty: %#v", got)
	}
}
