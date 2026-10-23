package fuse

import (
	"context"
	"fmt"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

// checks are what the plugin adds to the launch checklist.
func checks(ctx context.Context, api *plugin.API) []plugin.Check {
	return checksFor(ctx, api)
}

func checksFor(ctx context.Context, h host) []plugin.Check {
	fix := "#/plugins/fuse/overview"
	out := []plugin.Check{}
	v, err := loadActive(ctx, h)
	switch {
	case err != nil:
		out = append(out, plugin.Check{Key: "pricing.rates", Group: "pricing", Title: "Rates are published", State: "fails", Evidence: "The published rates can't be read: " + err.Error(), Launch: true, Fix: fix})
		return out
	case v == nil:
		out = append(out, plugin.Check{Key: "pricing.rates", Group: "pricing", Title: "Rates are published", State: "fails", Evidence: "Nothing is published, so /api/x/fuse/quote answers 409.", Launch: true, Fix: fix, FixLabel: "Import or publish"})
		return out
	}
	out = append(out, plugin.Check{Key: "pricing.rates", Group: "pricing", Title: "Rates are published", State: "passes",
		Evidence: fmt.Sprintf("Version %s, published %s by %s: %d products, %d fees, %d ZIPs.", v.active.Version, v.active.PublishedAt, v.active.By, v.active.Counts["products"], v.active.Counts["fees"], v.active.Counts["zips"]), Launch: true, Fix: fix})

	s := v.index.Book.Settings
	needs := s.MileageBasedPricing
	for _, f := range v.index.Book.Fees {
		needs = needs || f.MileageBasedPricing
	}
	if !needs {
		out = append(out, plugin.Check{Key: "pricing.distances", Group: "pricing", Title: "Distances can be had", State: "n_a", Evidence: "No mileage pricing in the published rates.", Fix: "#/plugins/fuse/settings"})
	} else {
		state, evidence := "passes", ""
		cfg := loadConfig(ctx, h)
		switch mode := cfg.mode(ctx, h); {
		case strings.TrimSpace(s.Zip) == "":
			state, evidence = "fails", "Mileage pricing is on but there's no business ZIP: every leg falls back to “Starting at”."
		case mode == modeOff:
			state, evidence = "fails", "Mileage pricing is on but distances are off: every leg is “Starting at”."
		case mode == modeFixture:
			state, evidence = "warns", "Distances come from recorded routes only; a ZIP not recorded gets “Starting at”. Set a Google key for live distances."
		case cfg.LastCheck != "" && !strings.HasPrefix(cfg.LastCheck, "OK"):
			state, evidence = "fails", "The Google key's last check: "+cfg.LastCheck
		case cfg.LastCheck == "":
			state, evidence = "unknown", "A Google key is set but hasn't been checked (Settings › Check key)."
		default:
			evidence = "Google Directions, key checked: " + cfg.LastCheck
		}
		out = append(out, plugin.Check{Key: "pricing.distances", Group: "pricing", Title: "Distances can be had", State: state, Evidence: evidence, Launch: true, Fix: "#/plugins/fuse/settings"})
	}
	if has, meta, _ := loadDraftMeta(ctx, h); has {
		out = append(out, plugin.Check{Key: "pricing.draft", Group: "pricing", Title: "The draft rates are published", State: "warns",
			Evidence: "There are unpublished changes (" + meta.UpdatedAt + " by " + meta.By + "); quotes still price from " + v.active.Version + ".", Fix: fix})
	}
	return out
}
