package engine

import (
	"testing"
)

// The quirks the port keeps on purpose, each pinned with a small book and
// figures worked by hand from Fuse's Ruby (the goldens cover them too, on
// the Austin book; these say which line of Ruby each one is).

func i64(v int64) *int64 { return new(v) }

// fee.rb#effective_amount: effective_mileage_tiers is already
// order(:position), so .order(:min_miles).first is the lowest *position*;
// quote_fee_calculator.rb:190 (no distance, move-it) takes
// MileageTier.order(:min_miles).first instead.
func TestEffectiveAmountOrdering(t *testing.T) {
	t.Parallel()
	b := &Book{MileageTiers: []Tier{
		{ID: 1, MinMiles: 15, MaxMiles: 30, Amount: 9900, RateType: "flat", Position: i64(1)},
		{ID: 2, MinMiles: 0, MaxMiles: 15, Amount: 7900, RateType: "flat", Position: i64(2)},
	}}
	x, err := NewIndex(b)
	if err != nil {
		t.Fatal(err)
	}
	f := &Fee{ID: 1, Name: "Mileage", Amount: 100, MileageBasedPricing: true}
	if got := x.effectiveAmount(f); got != 9900 {
		t.Errorf("effective_amount %d, Fuse 9900 (lowest position)", got)
	}
	if tier, _ := x.firstTierByMinMiles(); tier.Amount != 7900 {
		t.Errorf("order(:min_miles).first %d, Fuse 7900", tier.Amount)
	}
	// A nil position sorts first in SQL.
	b.MileageTiers[1].Position = nil
	x, _ = NewIndex(b)
	if got := x.effectiveAmount(f); got != 7900 {
		t.Errorf("with a NULL position: %d, SQLite puts NULL first so 7900", got)
	}
	// Not mileage-based: its own amount.
	if got := x.effectiveAmount(&Fee{Amount: 123}); got != 123 {
		t.Errorf("flat fee %d", got)
	}
}

// discount.rb#discounted_amount: Integer division floors.
func TestPercentageFloors(t *testing.T) {
	t.Parallel()
	pct := &Discount{DiscountType: "percentage", Amount: 15}
	for _, c := range [][2]int64{
		{14999, 14999 - 2249}, // 14999*15/100 = 2249.85 → 2249
		{-1001, -1001 + 151},  // -1001*15/100 = -150.15 → -151 (floor), so -850
		{0, 0},
	} {
		if got := discountedAmount(pct, c[0]); got != c[1] {
			t.Errorf("15%% off %d: %d, Fuse %d", c[0], got, c[1])
		}
	}
	fixed := &Discount{DiscountType: "fixed", Amount: 5000}
	if got := discountedAmount(fixed, 3000); got != 0 {
		t.Errorf("$50 off $30: %d, never below 0", got)
	}
}

// calculate_total_fee_from_tiers.
func TestTierFee(t *testing.T) {
	t.Parallel()
	tiers := []Tier{
		{ID: 1, MinMiles: 0, MaxMiles: 15, Amount: 7900, RateType: "flat"},
		{ID: 2, MinMiles: 15, MaxMiles: 30, Amount: 9900, RateType: "flat"},
		{ID: 3, MinMiles: 30, MaxMiles: 60, Amount: 325, RateType: "per_mile"},
		{ID: 4, MinMiles: 60, MaxMiles: 400, Amount: 8500, RateType: "per_hour"},
	}
	for _, c := range []struct {
		miles, hours float64
		want         int64
	}{
		{10, 0.3, 7900},
		{15, 0.4, 7900 + 9900},      // on the edge of two flat bands: both
		{30, 0.8, 9900},             // the per-mile band holds 30-30 = 0 miles
		{31.74, 0.9, 565},           // 31.74-30 is 1.7399999999999984 in floats: 325× that is 565.49999…, not 565.5
		{45.5, 0, 5038},             // 325×15.5 = 5037.5, rounded half away from zero
		{80, 0.5, 9750 + 8500},      // per-mile capped at its 30 miles; per-hour bills its 1-hour minimum
		{80, 2.25, 9750 + 19125},    // 8500×2.25
		{100, 1.04999, 9750 + 8925}, // 8500×1.04999 = 8924.915
		{60, 0, 9750},               // per-hour skipped with no hours
		{500, 9, 9750},              // past the last band: only the per-mile slice
		{-1, 1, 0},                  // in no band
		{0, 0, 7900},
		{14.999, 0.1, 7900},
		{15.001, 0.1, 9900},
	} {
		if got := tierFee(tiers, c.miles, c.hours); got != c.want {
			t.Errorf("%v mi %v h: %d, Fuse %d", c.miles, c.hours, got, c.want)
		}
	}
}

func TestLabels(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		miles, hours float64
		types        []string
		want         string
	}{
		{18.94, 0.5, []string{"flat"}, " (18.9 mi)"},
		{20, 0.5, []string{"per_mile", "per_hour"}, " (20.0 mi)"},
		{20, 0.66, []string{"per_hour"}, " (0.7 hr)"},
		{20, 0.66, nil, " (20.0 mi)"}, // no tiers: miles
		{20, 0.66, []string{"weird"}, ""},
	} {
		if got := mileageLabelSuffix(c.miles, c.hours, c.types); got != c.want {
			t.Errorf("%v: %q, want %q", c, got, c.want)
		}
	}
	tiers := []Tier{{MinMiles: 0, MaxMiles: 100, Amount: 8500, RateType: "per_hour"}}
	for _, c := range []struct {
		miles, hours float64
		want         string
	}{{10, 0.9, "1 Hour Route"}, {10, 1.25, "1.3 Hour Route"}, {10, 2, "2 Hour Route"}, {10, 0, ""}, {150, 2, ""}} {
		if got := rateDescription(tiers, c.miles, c.hours); got != c.want {
			t.Errorf("rate_description(%v, %v) = %q, want %q", c.miles, c.hours, got, c.want)
		}
	}
	d := &Discount{DurationUnit: "months", DurationValue: i64(1)}
	if durationLabel(d) != "First Month Only" {
		t.Error(durationLabel(d))
	}
	d.DurationValue = i64(3)
	if durationLabel(d) != "3 Months Only" {
		t.Error(durationLabel(d))
	}
	if durationLabel(&Discount{DurationUnit: "indefinite"}) != "Indefinite" {
		t.Error("indefinite")
	}
	for c, want := range map[int64]string{5000: "$50", 2550: "$25.50", 2505: "$25.05", 1: "$0.01", 0: "$0"} {
		if got := formatCents(c); got != want {
			t.Errorf("format_cents(%d) = %q, want %q", c, got, want)
		}
	}
}

func TestPlanRoutes(t *testing.T) {
	t.Parallel()
	rt := Settings{Zip: "78747", MileageRoundTrip: true}
	got := PlanRoutes(rt, "78701", "78626")
	want := []string{"78747, USA → 78747, USA → 78701", "78747, USA → 78747, USA → 78701|78626", "78747, USA → 78747, USA → 78626"}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("round trip %d: %s", i, got[i])
		}
	}
	if got := PlanRoutes(rt, "78701", ""); len(got) != 2 || got[0].String() != got[1].String() {
		t.Errorf("round trip without a destination: %v", got)
	}
	ow := Settings{Zip: "78747"}
	if got := PlanRoutes(ow, "78701", "78626"); len(got) != 1 || got[0].String() != "78747, USA → 78626, USA → 78701" {
		t.Errorf("one way: %v", got)
	}
	if got := PlanRoutes(ow, "78701", ""); got[0].String() != "78747, USA → 78701, USA → " {
		t.Errorf("one way, no destination: %v", got)
	}
	if PlanRoutes(Settings{}, "78701", "") != nil || PlanRoutes(rt, "", "") != nil {
		t.Error("no business ZIP or no ZIP: no routes")
	}
}

// product.rb#applicable_discount: with a product type slug given, only
// that type's discount is looked at, never the product's own type's.
func TestDiscountPrecedence(t *testing.T) {
	t.Parallel()
	b := &Book{
		Discounts:    []Discount{{ID: 1, Name: "Type", DiscountType: "fixed", Amount: 100}, {ID: 2, Name: "Location", DiscountType: "fixed", Amount: 200}, {ID: 3, Name: "Service", DiscountType: "fixed", Amount: 300}},
		ServiceTypes: []ServiceType{{ID: 1, Slug: "keep-it", Name: "Keep It", DiscountID: i64(3)}, {ID: 2, Slug: "move-it", Name: "Move It"}},
		ProductTypes: []ProductType{{ID: 1, Slug: "box", Name: "Box", DiscountID: i64(1)}, {ID: 2, Slug: "other", Name: "Other"}},
		Locations:    []Location{{ID: 1, Name: "Yard", DiscountID: i64(2)}},
		Products:     []Product{{ID: 1, Name: "Unit", Price: i64(1000), ProductTypeID: 1, Active: true}},
		ZipCodes:     []ZipCode{{ID: 1, PostalCode: "78701", LocationID: 1}},
	}
	x, err := NewIndex(b)
	if err != nil {
		t.Fatal(err)
	}
	zips := NewMapZips(b.ZipCodes)
	p := &b.Products[0]
	name := func(d *Discount) string {
		if d == nil {
			return ""
		}
		return d.Name
	}
	for _, c := range []struct{ zip, st, pt, want string }{
		{"78701", "keep-it", "box", "Service"},
		{"78701", "move-it", "box", "Type"},
		{"78701", "move-it", "", "Type"},          // no slug: the product's own type
		{"78701", "move-it", "other", "Location"}, // a slug with no discount: not the product's own type's
		{"78701", "move-it", "nope", "Location"},  // a slug naming nothing: the same
		{"", "move-it", "other", ""},
	} {
		if got := name(x.applicableDiscount(p, nil, c.zip, zips, c.st, c.pt)); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	b := &Book{
		Discounts: []Discount{{ID: 1, Name: "Half", DiscountType: "percentage", Amount: 150, DurationUnit: "months"}},
		Fees:      []Fee{{ID: 1, Name: "Miles", AppliesAt: "pickup_location", TotalMode: "included", MileageBasedPricing: true}},
		ZipCodes:  []ZipCode{{ID: 1, PostalCode: "7870", LocationID: 9}, {ID: 2, PostalCode: "78701", LocationID: 9}, {ID: 3, PostalCode: "78701", LocationID: 9}},
	}
	errs := Validate(b)
	for _, k := range []string{"discounts[0].amount", "discounts[0].duration_value", "fees[0].tiers", "zip_codes[0].postal_code", "zip_codes[2].postal_code", "book"} {
		if len(errs[k]) == 0 {
			t.Errorf("no error at %s: %v", k, errs)
		}
	}
	if errs := Validate(&Book{}); len(errs) != 0 {
		t.Errorf("an empty book: %v", errs)
	}
}
