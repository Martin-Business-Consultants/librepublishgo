package webhook

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/jsonv"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/store"
)

// A webhook's kind. An internal one is a receiver you run: it's sent the
// CMS's envelope, signed. An external one is another service's address
// (a CRM, a mailing list): it's sent the body its fields build from the
// event's data, as that service wants it, unsigned.
const (
	Internal = "internal"
	External = "external"
)

// Transforms are what a field can do to its value: "" leaves it as it is,
// "text" sends the field's Value itself.
var Transforms = []string{"", "slug", "lowercase", "uppercase", "date_us", "number", "text"}

// Field is one key of an external webhook's body. Value is a path into
// the event's data (values.first_name, hook.quote_id, form), or the text
// to send when Transform is "text".
type Field struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Transform string `json:"transform"`
}

// Fields reads a webhook's body_map.
func Fields(j store.JSON) []Field {
	var out []Field
	_ = json.Unmarshal(j.Or("[]"), &out) // written by saveWebhook
	return out
}

// Body is what an external webhook sends for data: its fields' values, or,
// with none, the event's data as it is (without the envelope).
func Body(fields []Field, data any) map[string]any {
	d := plain(data)
	if len(fields) == 0 {
		if m, ok := d.(map[string]any); ok {
			return m
		}
		return map[string]any{"data": d}
	}
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if f.Transform == "text" {
			out[f.Key] = f.Value
			continue
		}
		out[f.Key] = transform(f.Transform, Lookup(d, f.Value))
	}
	return out
}

// Takes is whether h listens for event, and data passes its filters for
// it: {event: {path: [values]}}, each path's value one of its values (a
// list's, any of them).
func (h *Hook) Takes(event string, data any) bool {
	events := []string{}
	for _, e := range h.Events.List() {
		events = append(events, jsonv.String(e))
	}
	if len(events) > 0 && !slices.Contains(events, event) && !slices.Contains(events, "*") {
		return false
	}
	filters, _ := h.EventFilters.Map()[event].(map[string]any)
	if len(filters) == 0 {
		return true
	}
	d := plain(data)
	for path, want := range filters {
		allowed := []string{}
		for _, v := range asList(want) {
			allowed = append(allowed, jsonv.String(v))
		}
		if len(allowed) == 0 {
			continue
		}
		if !slices.ContainsFunc(asList(Lookup(d, path)), func(v any) bool { return slices.Contains(allowed, jsonv.String(v)) }) {
			return false
		}
	}
	return true
}

// Lookup is the value at a dotted path in data decoded from JSON, nil
// when there's none.
func Lookup(data any, path string) any {
	v := data
	for part := range strings.SplitSeq(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[part]
	}
	return v
}

// plain is data as JSON decodes it (maps, lists, strings, numbers), so a
// path can be followed whatever type the event was made with.
func plain(data any) any {
	b, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return []any{v}
}

// transform applies a field's transform to v: to each item of a list, and
// "" for a value the event lacks (a number's is null).
func transform(name string, v any) any {
	if l, ok := v.([]any); ok {
		out := make([]any, len(l))
		for i, x := range l {
			out[i] = transform(name, x)
		}
		return out
	}
	if name == "number" {
		switch x := v.(type) {
		case float64:
			return x
		case string:
			if n, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
				return n
			}
		}
		return nil
	}
	if v == nil {
		return ""
	}
	if name == "" {
		return v
	}
	s := jsonv.String(v)
	switch name {
	case "slug":
		return slugify(s)
	case "lowercase":
		return strings.ToLower(s)
	case "uppercase":
		return strings.ToUpper(s)
	case "date_us":
		return usDate(s)
	}
	return s
}

// usDate is a date (2026-11-01, or a timestamp) as MM/DD/YYYY; anything
// else as it was.
func usDate(s string) string {
	if len(s) >= 10 {
		if t, err := time.Parse(time.DateOnly, s[:10]); err == nil {
			return t.Format("01/02/2006")
		}
	}
	return s
}

// slugify is s lowercased, its runs of anything but letters and digits a
// single dash: "Keep It" is keep-it.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
