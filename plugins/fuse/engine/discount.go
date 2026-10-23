package engine

import (
	"fmt"
	"strings"
)

// discountedAmount is Discount#discounted_amount (fuse/app/models/discount.rb):
// fixed takes the amount off but never below 0; percentage takes
// price*amount/100 off.
//
// Fuse quirk: the percentage is Integer division, so it floors — 15% of
// 14,999 cents is 2,249, not 2,250 — and floors toward negative infinity on
// a negative amount (a credit), which Go's / wouldn't.
func discountedAmount(d *Discount, price int64) int64 {
	if d == nil {
		return price
	}
	switch d.DiscountType {
	case "fixed":
		return max(price-d.Amount, 0)
	case "percentage":
		return price - floorDiv(price*d.Amount, 100)
	}
	return price
}

// durationLabel is Discount#duration_label: "Indefinite", "First Month Only",
// "3 Months Only".
func durationLabel(d *Discount) string {
	if d.DurationUnit == "indefinite" || d.DurationUnit == "" || d.DurationValue == nil {
		return "Indefinite"
	}
	unit := titleize(d.DurationUnit) // "Months"
	if *d.DurationValue == 1 {
		return "First " + strings.TrimSuffix(unit, "s") + " Only" // singularize: weeks, months, years
	}
	return fmt.Sprintf("%d %s Only", *d.DurationValue, unit)
}

func titleize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// discountLabel is FormsController#discount_label: "15% OFF", "$50 OFF",
// "$25.50 OFF".
func discountLabel(d *Discount) string {
	if d.DiscountType == "percentage" {
		return fmt.Sprintf("%d%% OFF", d.Amount)
	}
	return formatCents(d.Amount) + " OFF"
}

// formatCents is FormsController#format_cents: "$#{format('%.2f', cents / 100.0)}"
// with a trailing ".00" dropped. Go's %.2f rounds the binary value exactly,
// as C's printf (and so Ruby's format) does.
func formatCents(cents int64) string {
	return "$" + strings.TrimSuffix(fmt.Sprintf("%.2f", float64(cents)/100.0), ".00")
}

// applicableDiscount is Product#applicable_discount (product.rb): the first
// discount found, in this order —
//
//	the product's own
//	the ZIP's, then the ZIP's zone's
//	the service type's (by slug)
//	the product type's: the slug's when one was given, else the product's own type's
//	the ZIP's location's
//
// Fuse quirk: when a product type slug is given, only that type's discount
// is looked at; the product's own type's isn't, even when the slug names no
// type or a type without one.
//
// zip is the record when the caller has it (the catalog), else nil and
// zipCode is looked up (the preview).
func (x *Index) applicableDiscount(p *Product, zip *ZipCode, zipCode string, zips ZipSource, serviceSlug, productTypeSlug string) *Discount {
	if d := x.discount(p.DiscountID); d != nil {
		return d
	}
	if zip == nil && zipCode != "" {
		zip = zips.Zip(zipCode)
	}
	if zip != nil {
		if d := x.discount(zip.DiscountID); d != nil {
			return d
		}
		if z := x.zone(zip.ZoneID); z != nil {
			if d := x.discount(z.DiscountID); d != nil {
				return d
			}
		}
	}
	if serviceSlug != "" {
		if st := x.stBySlug[serviceSlug]; st != nil {
			if d := x.discount(st.DiscountID); d != nil {
				return d
			}
		}
	}
	if productTypeSlug != "" {
		if pt := x.ptBySlug[productTypeSlug]; pt != nil {
			if d := x.discount(pt.DiscountID); d != nil {
				return d
			}
		}
	} else if pt := x.productTypes[p.ProductTypeID]; pt != nil {
		if d := x.discount(pt.DiscountID); d != nil {
			return d
		}
	}
	if zip != nil {
		if l := x.locations[zip.LocationID]; l != nil {
			if d := x.discount(l.DiscountID); d != nil {
				return d
			}
		}
	}
	return nil
}
