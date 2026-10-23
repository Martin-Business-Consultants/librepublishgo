package engine

import (
	"fmt"
	"slices"
	"strings"
)

// Validate is what a book must hold to be published: Fuse's own model
// validations (discount.rb, fee.rb, zip_code.rb, product.rb, …) and every
// link resolving. Errors are keyed by where they are ("fees[3].tiers",
// "zip_codes[12].postal_code").
func Validate(b *Book) map[string][]string {
	errs := map[string][]string{}
	add := func(at, msg string) { errs[at] = append(errs[at], msg) }
	name := func(at, n string) {
		if l := len([]rune(strings.TrimSpace(n))); l < 2 || l > 100 {
			add(at+".name", "must be 2 to 100 characters")
		}
	}
	uniqueIDs := func(kind string, ids []int64) {
		seen := map[int64]bool{}
		for i, id := range ids {
			if id <= 0 {
				add(fmt.Sprintf("%s[%d].id", kind, i), "must be a positive id")
			}
			if seen[id] {
				add(fmt.Sprintf("%s[%d].id", kind, i), fmt.Sprintf("%d is used twice", id))
			}
			seen[id] = true
		}
	}

	var ids []int64
	slugs := map[string]bool{}
	for i, s := range b.ServiceTypes {
		ids = append(ids, s.ID)
		name(fmt.Sprintf("service_types[%d]", i), s.Name)
		if blank(s.Slug) {
			add(fmt.Sprintf("service_types[%d].slug", i), "can't be blank")
		} else if slugs[s.Slug] {
			add(fmt.Sprintf("service_types[%d].slug", i), s.Slug+" is used twice")
		}
		slugs[s.Slug] = true
	}
	uniqueIDs("service_types", ids)

	ids, slugs = nil, map[string]bool{}
	for i, p := range b.ProductTypes {
		ids = append(ids, p.ID)
		name(fmt.Sprintf("product_types[%d]", i), p.Name)
		if blank(p.Slug) {
			add(fmt.Sprintf("product_types[%d].slug", i), "can't be blank")
		} else if slugs[p.Slug] {
			add(fmt.Sprintf("product_types[%d].slug", i), p.Slug+" is used twice")
		}
		slugs[p.Slug] = true
	}
	uniqueIDs("product_types", ids)

	ids = nil
	for i, d := range b.Discounts {
		at := fmt.Sprintf("discounts[%d]", i)
		ids = append(ids, d.ID)
		name(at, d.Name)
		if d.DiscountType != "fixed" && d.DiscountType != "percentage" {
			add(at+".discount_type", "must be fixed or percentage")
		}
		if d.Amount <= 0 {
			add(at+".amount", "must be greater than 0")
		}
		if d.DiscountType == "percentage" && d.Amount > 100 {
			add(at+".amount", "cannot be greater than 100 for percentage discounts")
		}
		switch d.DurationUnit {
		case "indefinite", "":
			if d.DurationValue != nil {
				add(at+".duration_value", "must be blank for an indefinite discount")
			}
		case "weeks", "months", "years":
			if d.DurationValue == nil || *d.DurationValue <= 0 {
				add(at+".duration_value", "must be a whole number greater than 0")
			}
		default:
			add(at+".duration_unit", "must be indefinite, weeks, months or years")
		}
	}
	uniqueIDs("discounts", ids)

	ids = nil
	for i, l := range b.Locations {
		ids = append(ids, l.ID)
		name(fmt.Sprintf("locations[%d]", i), l.Name)
	}
	uniqueIDs("locations", ids)
	ids = nil
	for i, z := range b.Zones {
		ids = append(ids, z.ID)
		name(fmt.Sprintf("zones[%d]", i), z.Name)
	}
	uniqueIDs("zones", ids)

	tiers := func(at string, ts []Tier) {
		for j, t := range ts {
			tat := fmt.Sprintf("%s[%d]", at, j)
			if !slices.Contains([]string{"flat", "per_mile", "per_hour"}, t.RateType) {
				add(tat+".rate_type", "must be flat, per_mile or per_hour")
			}
			if t.MaxMiles < t.MinMiles {
				add(tat+".max_miles", "is below min_miles")
			}
		}
	}
	tiers("mileage_tiers", b.MileageTiers)

	ids = nil
	for i, f := range b.Fees {
		at := fmt.Sprintf("fees[%d]", i)
		ids = append(ids, f.ID)
		name(at, f.Name)
		if !slices.Contains([]string{pickupLocation, deliveryLocation, zoneToZone}, f.AppliesAt) {
			add(at+".applies_at", "must be pickup_location, delivery_location or zone_to_zone")
		}
		if !slices.Contains([]string{modeIncluded, modeStartingAt, modeExcluded, modeStartingAtIncluded, modeExcludedShown}, f.TotalMode) {
			add(at+".total_mode", "must be included, starting_at, excluded, starting_at_included or excluded_shown")
		}
		// fee.rb: mileage_tiers_present_if_mileage_pricing
		if f.MileageBasedPricing && len(f.Tiers) == 0 && len(b.MileageTiers) == 0 {
			add(at+".tiers", "a mileage-based fee needs tiers of its own or global mileage tiers")
		}
		tiers(at+".tiers", f.Tiers)
	}
	uniqueIDs("fees", ids)

	ids = nil
	for i, p := range b.Products {
		at := fmt.Sprintf("products[%d]", i)
		ids = append(ids, p.ID)
		name(at, p.Name)
		if p.Price == nil {
			add(at+".price", "can't be blank")
		}
		for _, d := range []struct {
			n string
			v *int64
		}{{"length", p.Length}, {"width", p.Width}, {"height", p.Height}} {
			if d.v != nil && *d.v <= 0 {
				add(at+"."+d.n, "must be greater than 0")
			}
		}
	}
	uniqueIDs("products", ids)

	ids = nil
	codes := map[string]bool{}
	for i, z := range b.ZipCodes {
		at := fmt.Sprintf("zip_codes[%d]", i)
		ids = append(ids, z.ID)
		switch {
		case !ZipFormat.MatchString(z.PostalCode):
			add(at+".postal_code", "must be a valid ZIP code (e.g., 12345 or 12345-6789)")
		case codes[z.PostalCode]:
			add(at+".postal_code", z.PostalCode+" is in the table twice")
		}
		codes[z.PostalCode] = true
	}
	uniqueIDs("zip_codes", ids)

	if b.Settings.Zip != "" && !ZipFormat.MatchString(b.Settings.Zip) {
		add("settings.zip", "must be a ZIP code (12345 or 12345-6789)")
	}
	if _, err := NewIndex(b); err != nil {
		add("book", err.Error())
	}
	return errs
}
