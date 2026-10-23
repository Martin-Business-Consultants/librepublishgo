// Package engine is Fuse's pricing, in Go: QuoteFeeCalculator,
// MileagePricingService, DiscountedFee, Product#applicable_discount,
// Quoteable.calculate_quote and the serializers of the headless quoting API
// (POST /api/v1/quoting/preview, GET /api/v1/quoting/config, GET
// /api/service_areas/:zip), ported to answer the same JSON for the same rate
// book. It imports nothing of the CMS: a Book in, figures out.
//
// The comments name the Ruby each part ports (fuse/app/...), and every quirk
// the port keeps on purpose says so with "Fuse quirk:".
package engine

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Book is one rate book: the tables Fuse prices from, with Fuse's ids, as
// Fuse's pricing:export writes them (the bundle's "rates").
type Book struct {
	Settings     Settings      `json:"settings"`
	ServiceTypes []ServiceType `json:"service_types"`
	ProductTypes []ProductType `json:"product_types"`
	Discounts    []Discount    `json:"discounts"`
	Locations    []Location    `json:"locations"`
	Zones        []Zone        `json:"zones"`
	MileageTiers []Tier        `json:"mileage_tiers"`
	Fees         []Fee         `json:"fees"`
	Products     []Product     `json:"products"`
	ZipCodes     []ZipCode     `json:"zip_codes,omitempty"`
}

// Settings are the tenant settings that change a price or the coverage
// answer.
type Settings struct {
	Name                string `json:"name,omitempty"`
	Phone               string `json:"phone,omitempty"`
	Zip                 string `json:"zip"` // the business ZIP every route starts and ends at
	MileageBasedPricing bool   `json:"mileage_based_pricing"`
	MileageRoundTrip    bool   `json:"mileage_round_trip"`
	ZipQualification    bool   `json:"zip_qualification"`
	OutOfAreaZipText    string `json:"out_of_area_zip_text,omitempty"`
	QuoteValidityDays   *int   `json:"quote_validity_days,omitempty"`
	PricingEnabled      *bool  `json:"pricing_enabled,omitempty"`
	TotalPriceLabel     string `json:"total_price_label,omitempty"`
}

// ServiceType is keep-it, move-it, store-it and the like.
type ServiceType struct {
	ID         int64  `json:"id"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	DiscountID *int64 `json:"discount_id"`
}

// ProductType is portable-storage, shipping-container and the like.
type ProductType struct {
	ID         int64  `json:"id"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	DiscountID *int64 `json:"discount_id"`
}

// Discount is fixed (cents off) or percentage.
type Discount struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	DiscountType  string `json:"discount_type"` // fixed, percentage
	Amount        int64  `json:"amount"`
	DurationUnit  string `json:"duration_unit"` // indefinite, weeks, months, years
	DurationValue *int64 `json:"duration_value"`
}

// Location is a yard: a phone, and optionally its own fee and discount.
type Location struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Phone      string `json:"phone"`
	FeeID      *int64 `json:"fee_id"`
	DiscountID *int64 `json:"discount_id"`
}

// Zone groups ZIPs, belongs to a location, and may carry a discount.
type Zone struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	LocationID int64  `json:"location_id"`
	DiscountID *int64 `json:"discount_id"`
}

// Tier is one band of a mileage schedule (MileageTier, FeeMileageTier).
type Tier struct {
	ID       int64  `json:"id"`
	MinMiles int64  `json:"min_miles"`
	MaxMiles int64  `json:"max_miles"`
	Amount   int64  `json:"amount"`
	RateType string `json:"rate_type"` // flat, per_mile, per_hour
	Position *int64 `json:"position"`
}

// Fee is a charge, and where and how it applies.
type Fee struct {
	ID                    int64   `json:"id"`
	Name                  string  `json:"name"`
	Amount                int64   `json:"amount"`
	Position              *int64  `json:"position"`
	AppliesAt             string  `json:"applies_at"` // pickup_location, delivery_location, zone_to_zone
	TotalMode             string  `json:"total_mode"` // included, starting_at, excluded, starting_at_included, excluded_shown
	Recurring             bool    `json:"recurring"`
	MileageBasedPricing   bool    `json:"mileage_based_pricing"`
	ExcludeBeyondLastTier bool    `json:"exclude_beyond_last_tier"` // Fuse quirk: stored, read by nothing that prices (Q8)
	DiscountID            *int64  `json:"discount_id"`
	ServiceTypeIDs        []int64 `json:"service_type_ids"`
	ProductTypeIDs        []int64 `json:"product_type_ids"`
	FromZoneIDs           []int64 `json:"from_zone_ids"`
	ToZoneIDs             []int64 `json:"to_zone_ids"`
	Tiers                 []Tier  `json:"tiers"`
}

// Product is a unit that's rented or sold.
type Product struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Position       *int64   `json:"position"`
	Active         bool     `json:"active"`
	Price          *int64   `json:"price"`
	ProductTypeID  int64    `json:"product_type_id"`
	ServiceTypeIDs []int64  `json:"service_type_ids"`
	DiscountID     *int64   `json:"discount_id"`
	Monthly        bool     `json:"monthly"`
	ForSale        bool     `json:"for_sale"`
	Length         *int64   `json:"length"`
	Width          *int64   `json:"width"`
	Height         *int64   `json:"height"`
	IdealFor       []string `json:"ideal_for"` // ideal_for_1..3, blanks kept
	// Images are the product's pictures. An export carries them as base64;
	// once imported each is an asset of the media library, by URL.
	Images []Image `json:"images,omitempty"`
}

// Image is a product picture: a media-library URL once imported, base64 in
// an export.
type Image struct {
	URL         string `json:"url,omitempty"`
	AssetID     any    `json:"asset_id,omitempty"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Base64      string `json:"base64,omitempty"`
}

// ZipCode is one served ZIP (5-digit or ZIP+4) and what it's in.
type ZipCode struct {
	ID         int64  `json:"id"`
	PostalCode string `json:"postal_code"`
	ZoneID     *int64 `json:"zone_id"`
	LocationID int64  `json:"location_id"`
	FeeID      *int64 `json:"fee_id"`
	DiscountID *int64 `json:"discount_id"`
}

// Fee modes, as Fuse's enums name them.
const (
	pickupLocation   = "pickup_location"
	deliveryLocation = "delivery_location"
	zoneToZone       = "zone_to_zone"

	modeIncluded           = "included"
	modeStartingAt         = "starting_at"
	modeExcluded           = "excluded"
	modeStartingAtIncluded = "starting_at_included"
	modeExcludedShown      = "excluded_shown"
)

// Fee#excluded_from_total: starting_at or excluded_shown. starting_at_included
// says "Starting at" but is summed.
func (f *Fee) excludedFromTotal() bool {
	return f.TotalMode == modeStartingAt || f.TotalMode == modeExcludedShown
}

// Fee#display_as_starting_at?
func (f *Fee) displayAsStartingAt() bool {
	return f.TotalMode == modeStartingAt || f.TotalMode == modeStartingAtIncluded
}

func (f *Fee) excluded() bool { return f.TotalMode == modeExcluded }

// Fee#relocation is zone_to_zone?.
func (f *Fee) relocation() bool { return f.AppliesAt == zoneToZone }

// ZipSource answers the ZIP table, which can be far bigger than the rest of
// a book: the plugin reads it a shard at a time.
type ZipSource interface {
	// Zip is the record whose postal code is exactly code, nil when none.
	Zip(code string) *ZipCode
	// Any says whether the table has any ZIP at all (ZipCode.exists?).
	Any() bool
	// Served says whether zip5 or a ZIP+4 under it is in the table.
	Served(zip5 string) bool
}

// MapZips is a ZipSource over a whole table in memory.
type MapZips struct {
	byCode map[string]*ZipCode
	plus4  map[string]bool
}

// NewMapZips indexes zips.
func NewMapZips(zips []ZipCode) *MapZips {
	m := &MapZips{byCode: make(map[string]*ZipCode, len(zips)), plus4: map[string]bool{}}
	for i := range zips {
		z := &zips[i]
		// Fuse quirk: postal codes are unique case-insensitively, so the first
		// by id wins a lookup; an export never carries two.
		if _, dup := m.byCode[z.PostalCode]; !dup {
			m.byCode[z.PostalCode] = z
		}
		if len(z.PostalCode) == 10 {
			m.plus4[z.PostalCode[:5]] = true
		}
	}
	return m
}

// Zip is the record for exactly code.
func (m *MapZips) Zip(code string) *ZipCode { return m.byCode[code] }

// Any says whether there are any ZIPs.
func (m *MapZips) Any() bool { return len(m.byCode) > 0 }

// Served says whether zip5, or a ZIP+4 under it, is in the table.
func (m *MapZips) Served(zip5 string) bool { return m.byCode[zip5] != nil || m.plus4[zip5] }

// Index is a book made ready to price from: its records by id and its joins
// inverted, built once per published version.
type Index struct {
	Book *Book

	serviceTypes map[int64]*ServiceType
	stBySlug     map[string]*ServiceType
	productTypes map[int64]*ProductType
	ptBySlug     map[string]*ProductType
	discounts    map[int64]*Discount
	locations    map[int64]*Location
	zones        map[int64]*Zone
	fees         map[int64]*Fee
	products     map[int64]*Product

	// feesByID is every fee in id order (Fee.zone_to_zone has no ORDER BY;
	// SQLite answers it in rowid order).
	feesByID []*Fee
	// zonePickup and zoneDelivery invert fee_from_zones and fee_to_zones
	// (Zone#pickup_fees, Zone#delivery_fees), in fee id order: the join
	// tables' (zone_id, fee_id) index order, which is insertion order for
	// rows Fuse wrote.
	zonePickup   map[int64][]*Fee
	zoneDelivery map[int64][]*Fee
	// globalTiersByPosition is MileageTier.order(:position) (then
	// min_miles: Fee#effective_amount adds that order).
	globalTiersByPosition []Tier
}

// NewIndex indexes b, and says what doesn't hold together.
func NewIndex(b *Book) (*Index, error) {
	x := &Index{
		Book:         b,
		serviceTypes: map[int64]*ServiceType{}, stBySlug: map[string]*ServiceType{},
		productTypes: map[int64]*ProductType{}, ptBySlug: map[string]*ProductType{},
		discounts: map[int64]*Discount{}, locations: map[int64]*Location{}, zones: map[int64]*Zone{},
		fees: map[int64]*Fee{}, products: map[int64]*Product{},
		zonePickup: map[int64][]*Fee{}, zoneDelivery: map[int64][]*Fee{},
	}
	for i := range b.ServiceTypes {
		s := &b.ServiceTypes[i]
		x.serviceTypes[s.ID] = s
		if _, dup := x.stBySlug[s.Slug]; !dup { // find_by(slug:): the lowest id
			x.stBySlug[s.Slug] = s
		}
	}
	for i := range b.ProductTypes {
		p := &b.ProductTypes[i]
		x.productTypes[p.ID] = p
		if _, dup := x.ptBySlug[p.Slug]; !dup {
			x.ptBySlug[p.Slug] = p
		}
	}
	for i := range b.Discounts {
		x.discounts[b.Discounts[i].ID] = &b.Discounts[i]
	}
	for i := range b.Locations {
		x.locations[b.Locations[i].ID] = &b.Locations[i]
	}
	for i := range b.Zones {
		x.zones[b.Zones[i].ID] = &b.Zones[i]
	}
	for i := range b.Products {
		x.products[b.Products[i].ID] = &b.Products[i]
	}
	for i := range b.Fees {
		f := &b.Fees[i]
		x.fees[f.ID] = f
		x.feesByID = append(x.feesByID, f)
	}
	slices.SortStableFunc(x.feesByID, func(a, b *Fee) int { return cmpInt(a.ID, b.ID) })
	for _, f := range x.feesByID {
		for _, z := range f.FromZoneIDs {
			x.zonePickup[z] = append(x.zonePickup[z], f)
		}
		for _, z := range f.ToZoneIDs {
			x.zoneDelivery[z] = append(x.zoneDelivery[z], f)
		}
	}
	x.globalTiersByPosition = sqlTierOrder(b.MileageTiers, true)
	return x, x.check()
}

// check is what a book needs to price at all: every id it links resolves.
func (x *Index) check() error {
	var errs []string
	missing := func(what string, id int64, from string) {
		errs = append(errs, fmt.Sprintf("%s links %s %d, which isn't in the book", from, what, id))
	}
	disc := func(id *int64, from string) {
		if id != nil && x.discounts[*id] == nil {
			missing("discount", *id, from)
		}
	}
	fee := func(id *int64, from string) {
		if id != nil && x.fees[*id] == nil {
			missing("fee", *id, from)
		}
	}
	for _, s := range x.Book.ServiceTypes {
		disc(s.DiscountID, "service type "+s.Slug)
	}
	for _, p := range x.Book.ProductTypes {
		disc(p.DiscountID, "product type "+p.Slug)
	}
	for _, l := range x.Book.Locations {
		disc(l.DiscountID, "location "+l.Name)
		fee(l.FeeID, "location "+l.Name)
	}
	for _, z := range x.Book.Zones {
		disc(z.DiscountID, "zone "+z.Name)
		if x.locations[z.LocationID] == nil {
			missing("location", z.LocationID, "zone "+z.Name)
		}
	}
	for _, f := range x.Book.Fees {
		from := fmt.Sprintf("fee %d (%s)", f.ID, f.Name)
		disc(f.DiscountID, from)
		for _, id := range f.ServiceTypeIDs {
			if x.serviceTypes[id] == nil {
				missing("service type", id, from)
			}
		}
		for _, id := range f.ProductTypeIDs {
			if x.productTypes[id] == nil {
				missing("product type", id, from)
			}
		}
		for _, id := range append(slices.Clone(f.FromZoneIDs), f.ToZoneIDs...) {
			if x.zones[id] == nil {
				missing("zone", id, from)
			}
		}
	}
	for _, p := range x.Book.Products {
		from := fmt.Sprintf("product %d (%s)", p.ID, p.Name)
		disc(p.DiscountID, from)
		if x.productTypes[p.ProductTypeID] == nil {
			missing("product type", p.ProductTypeID, from)
		}
		for _, id := range p.ServiceTypeIDs {
			if x.serviceTypes[id] == nil {
				missing("service type", id, from)
			}
		}
	}
	for _, z := range x.Book.ZipCodes {
		from := "ZIP " + z.PostalCode
		disc(z.DiscountID, from)
		fee(z.FeeID, from)
		if z.ZoneID != nil && x.zones[*z.ZoneID] == nil {
			missing("zone", *z.ZoneID, from)
		}
		if x.locations[z.LocationID] == nil {
			missing("location", z.LocationID, from)
		}
	}
	if len(errs) > 0 {
		if len(errs) > 20 {
			errs = append(errs[:20], fmt.Sprintf("and %d more", len(errs)-20))
		}
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ServiceTypeBySlug is ServiceType.find_by(slug:), nil when there's none.
func (x *Index) ServiceTypeBySlug(slug string) *ServiceType { return x.stBySlug[slug] }

// ProductTypeBySlug is ProductType.find_by(slug:).
func (x *Index) ProductTypeBySlug(slug string) *ProductType { return x.ptBySlug[slug] }

// ProductByID is a product by its id, nil when there's none.
func (x *Index) ProductByID(id int64) *Product { return x.products[id] }

// LocationByID is a location by its id.
func (x *Index) LocationByID(id int64) *Location { return x.locations[id] }

func (x *Index) discount(id *int64) *Discount {
	if id == nil {
		return nil
	}
	return x.discounts[*id]
}

func (x *Index) fee(id *int64) *Fee {
	if id == nil {
		return nil
	}
	return x.fees[*id]
}

func (x *Index) zone(id *int64) *Zone {
	if id == nil {
		return nil
	}
	return x.zones[*id]
}

// sqlTierOrder is ORDER BY position[, min_miles] as SQLite answers it: NULL
// positions first, ties in id order. (Ruby's sort_by puts a nil position
// last; SQL puts it first. Fuse does both, in different places.)
func sqlTierOrder(tiers []Tier, thenMin bool) []Tier {
	out := slices.Clone(tiers)
	slices.SortStableFunc(out, func(a, b Tier) int {
		if c := cmpNullsFirst(a.Position, b.Position); c != 0 {
			return c
		}
		if thenMin {
			if c := cmpInt(a.MinMiles, b.MinMiles); c != 0 {
				return c
			}
		}
		return cmpInt(a.ID, b.ID)
	})
	return out
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// cmpNullsFirst orders nullable integers as SQLite's ORDER BY does.
func cmpNullsFirst(a, b *int64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	return cmpInt(*a, *b)
}

// ZipFormat is ZipCode's validation: 5 digits, or ZIP+4.
var ZipFormat = regexp.MustCompile(`^\d{5}(-\d{4})?$`)

// zip5Format is what the service-area lookup takes.
var zip5Format = regexp.MustCompile(`^\d{5}$`)
