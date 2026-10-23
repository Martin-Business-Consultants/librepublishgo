package fuse

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/plugins/fuse/engine"
)

// The rate editor works on the draft. A record's links to others are edited
// by what people know them by — a service type's slug, a zone's or a
// discount's name, a fee's id where names repeat — and checked on save: a
// 422 marks what doesn't resolve. Ids are Fuse's, kept from the import, so
// a later import diffs against them; a new record takes the next id.

// kind is one of the rate book's tables, as the editor sees it.
type kind struct {
	singular, plural, title string
	list                    func(b *engine.Book) []map[string]any
	get                     func(b *engine.Book, id int64) (map[string]any, bool)
	// put writes rec as record id (0: a new one) and answers its id.
	put func(b *engine.Book, id int64, rec map[string]any) (int64, map[string][]string)
	// del removes record id, or says why it can't go.
	del func(b *engine.Book, id int64) string
}

type form map[string]any

func (f form) str(k string) string {
	s, _ := f[k].(string)
	return strings.TrimSpace(s)
}

func (f form) boolean(k string) bool { b, _ := f[k].(bool); return b }

// integer reads a whole number; ok is false when it's there but isn't one.
func (f form) integer(k string) (*int64, bool) {
	switch v := f[k].(type) {
	case nil:
		return nil, true
	case float64:
		if v != math.Trunc(v) {
			return nil, false
		}
		n := int64(v)
		return &n, true
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, true
		}
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return nil, false
		}
		return &n, true
	}
	return nil, false
}

func (f form) list(k string) []string {
	var out []string
	switch v := f[k].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		for s := range strings.SplitSeq(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

type errs map[string][]string

func (e errs) add(k, msg string) { e[k] = append(e[k], msg) }

// need reads a required integer.
func (e errs) need(f form, k string) int64 {
	n, ok := f.integer(k)
	switch {
	case !ok:
		e.add(k, "must be a whole number")
	case n == nil:
		e.add(k, "can't be blank")
	default:
		return *n
	}
	return 0
}

func (e errs) opt(f form, k string) *int64 {
	n, ok := f.integer(k)
	if !ok {
		e.add(k, "must be a whole number")
	}
	return n
}

// Name and id lookups on a book.

func discountName(b *engine.Book, id *int64) string {
	if id == nil {
		return ""
	}
	for _, d := range b.Discounts {
		if d.ID == *id {
			return d.Name
		}
	}
	return fmt.Sprintf("#%d", *id)
}

func (e errs) discountID(b *engine.Book, f form, k string) *int64 {
	name := f.str(k)
	if name == "" {
		return nil
	}
	var found []int64
	for _, d := range b.Discounts {
		if strings.EqualFold(d.Name, name) || "#"+strconv.FormatInt(d.ID, 10) == name {
			found = append(found, d.ID)
		}
	}
	switch len(found) {
	case 0:
		e.add(k, "there's no discount named "+name)
	case 1:
		return &found[0]
	default:
		e.add(k, fmt.Sprintf("%d discounts are named %s: rename one, or give its id as #%d", len(found), name, found[0]))
	}
	return nil
}

func slugsOf[T any](ids []int64, all []T, id func(T) int64, slug func(T) string) []string {
	out := []string{}
	for _, want := range ids {
		for _, x := range all {
			if id(x) == want {
				out = append(out, slug(x))
			}
		}
	}
	return out
}

func idsOf[T any](e errs, k string, names []string, all []T, id func(T) int64, name func(T) string, what string) []int64 {
	out := []int64{}
	for _, n := range names {
		i := slices.IndexFunc(all, func(x T) bool { return strings.EqualFold(name(x), n) })
		if i < 0 {
			e.add(k, "there's no "+what+" "+n)
			continue
		}
		if !slices.Contains(out, id(all[i])) {
			out = append(out, id(all[i]))
		}
	}
	slices.Sort(out)
	return out
}

func stID(s engine.ServiceType) int64    { return s.ID }
func stSlug(s engine.ServiceType) string { return s.Slug }
func ptID(p engine.ProductType) int64    { return p.ID }
func ptSlug(p engine.ProductType) string { return p.Slug }
func zID(z engine.Zone) int64            { return z.ID }
func zName(z engine.Zone) string         { return z.Name }

func nextID[T any](all []T, id func(T) int64) int64 {
	var m int64
	for _, x := range all {
		m = max(m, id(x))
	}
	return m + 1
}

func idOr0(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func ptrVal(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// upsert puts v at id's place in all, or appends it.
func upsert[T any](all []T, id func(T) int64, v T) []T {
	if i := slices.IndexFunc(all, func(x T) bool { return id(x) == id(v) }); i >= 0 {
		all[i] = v
		return all
	}
	return append(all, v)
}

func find[T any](all []T, id func(T) int64, want int64) (T, bool) {
	i := slices.IndexFunc(all, func(x T) bool { return id(x) == want })
	if i < 0 {
		var zero T
		return zero, false
	}
	return all[i], true
}

func without[T any](all []T, id func(T) int64, gone int64) []T {
	return slices.DeleteFunc(all, func(x T) bool { return id(x) == gone })
}

func drop(ids []int64, gone int64) []int64 {
	return slices.DeleteFunc(slices.Clone(ids), func(x int64) bool { return x == gone })
}

func nullify(p **int64, gone int64) {
	if *p != nil && **p == gone {
		*p = nil
	}
}

var rateTypeOptions = []map[string]string{{"value": "flat", "label": "Flat amount, inside the band"}, {"value": "per_mile", "label": "Per mile, for the miles in the band"}, {"value": "per_hour", "label": "Per hour (1-hour minimum), inside the band"}}

func tiersOut(ts []engine.Tier) []map[string]any {
	out := []map[string]any{}
	for _, t := range ts {
		out = append(out, map[string]any{"id": t.ID, "min_miles": t.MinMiles, "max_miles": t.MaxMiles, "amount": t.Amount, "rate_type": t.RateType, "position": ptrVal(t.Position)})
	}
	return out
}

// tiersIn reads a repeater of tiers, keeping ids it knows and numbering the
// rest after the highest of known (every tier id across the book).
func tiersIn(e errs, at string, raw any, b *engine.Book) []engine.Tier {
	items, _ := raw.([]any)
	var maxID int64
	for _, t := range b.MileageTiers {
		maxID = max(maxID, t.ID)
	}
	for _, f := range b.Fees {
		for _, t := range f.Tiers {
			maxID = max(maxID, t.ID)
		}
	}
	out := []engine.Tier{}
	for i, it := range items {
		m, _ := it.(map[string]any)
		f := form(m)
		te := errs{}
		t := engine.Tier{MinMiles: te.need(f, "min_miles"), MaxMiles: te.need(f, "max_miles"), Amount: te.need(f, "amount"), RateType: f.str("rate_type"), Position: te.opt(f, "position")}
		if t.RateType == "" {
			t.RateType = "flat"
		}
		if t.Position == nil {
			p := int64(i + 1)
			t.Position = &p
		}
		if id, ok := f.integer("id"); ok && id != nil && *id > 0 {
			t.ID = *id
		} else {
			maxID++
			t.ID = maxID
		}
		for k, v := range te {
			e[fmt.Sprintf("%s[%d].%s", at, i, k)] = v
		}
		out = append(out, t)
	}
	return out
}

func feeLabel(b *engine.Book, id *int64) string {
	if id == nil {
		return ""
	}
	for _, f := range b.Fees {
		if f.ID == *id {
			return fmt.Sprintf("%s (#%d)", f.Name, f.ID)
		}
	}
	return fmt.Sprintf("#%d", *id)
}

func (e errs) feeID(b *engine.Book, f form, k string) *int64 {
	id := e.opt(f, k)
	if id == nil {
		return nil
	}
	if _, ok := find(b.Fees, func(x engine.Fee) int64 { return x.ID }, *id); !ok {
		e.add(k, fmt.Sprintf("there's no fee #%d", *id))
		return nil
	}
	return id
}

func locationName(b *engine.Book, id int64) string {
	if l, ok := find(b.Locations, func(x engine.Location) int64 { return x.ID }, id); ok {
		return l.Name
	}
	return fmt.Sprintf("#%d", id)
}

func zoneName(b *engine.Book, id *int64) string {
	if id == nil {
		return ""
	}
	if z, ok := find(b.Zones, zID, *id); ok {
		return z.Name
	}
	return fmt.Sprintf("#%d", *id)
}

func money(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s$%d.%02d", sign, cents/100, cents%100)
}

func names(n []string) string { return strings.Join(n, ", ") }

var kinds = []kind{
	{
		singular: "product", plural: "products", title: "Products",
		list: func(b *engine.Book) []map[string]any {
			rows := []map[string]any{}
			for _, p := range b.Products {
				price := ""
				if p.Price != nil {
					price = money(*p.Price)
				}
				rows = append(rows, map[string]any{"id": p.ID, "name": p.Name, "price": price, "position": ptrVal(p.Position),
					"product_type":  slugsOf([]int64{p.ProductTypeID}, b.ProductTypes, ptID, ptSlug),
					"service_types": names(slugsOf(p.ServiceTypeIDs, b.ServiceTypes, stID, stSlug)),
					"status":        map[bool]string{true: "active", false: "inactive"}[p.Active], "discount": discountName(b, p.DiscountID)})
			}
			return rows
		},
		get: func(b *engine.Book, id int64) (map[string]any, bool) {
			p, ok := find(b.Products, func(x engine.Product) int64 { return x.ID }, id)
			if !ok {
				return nil, false
			}
			pt := slugsOf([]int64{p.ProductTypeID}, b.ProductTypes, ptID, ptSlug)
			image := ""
			if len(p.Images) > 0 {
				image = p.Images[0].URL
			}
			return map[string]any{"id": p.ID, "name": p.Name, "price": ptrVal(p.Price), "position": ptrVal(p.Position), "active": p.Active,
				"product_type": strings.Join(pt, ""), "service_types": slugsOf(p.ServiceTypeIDs, b.ServiceTypes, stID, stSlug),
				"discount": discountName(b, p.DiscountID), "monthly": p.Monthly, "for_sale": p.ForSale,
				"length": ptrVal(p.Length), "width": ptrVal(p.Width), "height": ptrVal(p.Height),
				"ideal_for": nonBlank(p.IdealFor), "image_url": image}, true
		},
		put: func(b *engine.Book, id int64, rec map[string]any) (int64, map[string][]string) {
			f, e := form(rec), errs{}
			p := engine.Product{ID: id, Name: f.str("name"), Price: e.opt(f, "price"), Position: e.opt(f, "position"), Active: f.boolean("active"),
				DiscountID: e.discountID(b, f, "discount"), Monthly: f.boolean("monthly"), ForSale: f.boolean("for_sale"),
				Length: e.opt(f, "length"), Width: e.opt(f, "width"), Height: e.opt(f, "height")}
			if p.Price == nil {
				e.add("price", "can't be blank (in cents: 14900 is $149)")
			}
			pts := idsOf(e, "product_type", []string{f.str("product_type")}, b.ProductTypes, ptID, ptSlug, "product type")
			if f.str("product_type") == "" {
				e.add("product_type", "can't be blank")
			} else if len(pts) == 1 {
				p.ProductTypeID = pts[0]
			}
			p.ServiceTypeIDs = idsOf(e, "service_types", f.list("service_types"), b.ServiceTypes, stID, stSlug, "service type")
			ideal := f.list("ideal_for")
			if len(ideal) > 3 {
				e.add("ideal_for", "takes at most 3")
			}
			p.IdealFor = make([]string, 3)
			copy(p.IdealFor, ideal)
			if old, ok := find(b.Products, func(x engine.Product) int64 { return x.ID }, id); ok {
				p.Images = old.Images
			}
			if u := f.str("image_url"); u != "" {
				if len(p.Images) == 0 || p.Images[0].URL != u {
					p.Images = append([]engine.Image{{URL: u}}, p.Images...)
				}
			} else {
				p.Images = nil
			}
			if len(e) > 0 {
				return 0, e
			}
			if p.ID == 0 {
				p.ID = nextID(b.Products, func(x engine.Product) int64 { return x.ID })
			}
			b.Products = upsert(b.Products, func(x engine.Product) int64 { return x.ID }, p)
			return p.ID, nil
		},
		del: func(b *engine.Book, id int64) string {
			b.Products = without(b.Products, func(x engine.Product) int64 { return x.ID }, id)
			return ""
		},
	},
	{
		singular: "fee", plural: "fees", title: "Fees",
		list: func(b *engine.Book) []map[string]any {
			rows := []map[string]any{}
			for _, f := range b.Fees {
				rows = append(rows, map[string]any{"id": f.ID, "name": f.Name, "amount": money(f.Amount), "position": ptrVal(f.Position),
					"applies_at": f.AppliesAt, "total_mode": f.TotalMode, "mileage": map[bool]string{true: "mileage", false: ""}[f.MileageBasedPricing],
					"service_types": names(slugsOf(f.ServiceTypeIDs, b.ServiceTypes, stID, stSlug)),
					"zones":         strings.TrimSpace(names(slugsOf(f.FromZoneIDs, b.Zones, zID, zName)) + " → " + names(slugsOf(f.ToZoneIDs, b.Zones, zID, zName)))})
			}
			return rows
		},
		get: func(b *engine.Book, id int64) (map[string]any, bool) {
			f, ok := find(b.Fees, func(x engine.Fee) int64 { return x.ID }, id)
			if !ok {
				return nil, false
			}
			return map[string]any{"id": f.ID, "name": f.Name, "amount": f.Amount, "position": ptrVal(f.Position), "applies_at": f.AppliesAt,
				"total_mode": f.TotalMode, "recurring": f.Recurring, "mileage_based_pricing": f.MileageBasedPricing,
				"exclude_beyond_last_tier": f.ExcludeBeyondLastTier, "discount": discountName(b, f.DiscountID),
				"service_types": slugsOf(f.ServiceTypeIDs, b.ServiceTypes, stID, stSlug), "product_types": slugsOf(f.ProductTypeIDs, b.ProductTypes, ptID, ptSlug),
				"from_zones": slugsOf(f.FromZoneIDs, b.Zones, zID, zName), "to_zones": slugsOf(f.ToZoneIDs, b.Zones, zID, zName),
				"tiers": tiersOut(f.Tiers)}, true
		},
		put: func(b *engine.Book, id int64, rec map[string]any) (int64, map[string][]string) {
			f, e := form(rec), errs{}
			fee := engine.Fee{ID: id, Name: f.str("name"), Amount: e.need(f, "amount"), Position: e.opt(f, "position"),
				AppliesAt: strOr(f.str("applies_at"), "pickup_location"), TotalMode: strOr(f.str("total_mode"), "included"),
				Recurring: f.boolean("recurring"), MileageBasedPricing: f.boolean("mileage_based_pricing"),
				ExcludeBeyondLastTier: f.boolean("exclude_beyond_last_tier"), DiscountID: e.discountID(b, f, "discount")}
			fee.ServiceTypeIDs = idsOf(e, "service_types", f.list("service_types"), b.ServiceTypes, stID, stSlug, "service type")
			fee.ProductTypeIDs = idsOf(e, "product_types", f.list("product_types"), b.ProductTypes, ptID, ptSlug, "product type")
			fee.FromZoneIDs = idsOf(e, "from_zones", f.list("from_zones"), b.Zones, zID, zName, "zone")
			fee.ToZoneIDs = idsOf(e, "to_zones", f.list("to_zones"), b.Zones, zID, zName, "zone")
			fee.Tiers = tiersIn(e, "tiers", rec["tiers"], b)
			if fee.MileageBasedPricing && len(fee.Tiers) == 0 && len(b.MileageTiers) == 0 {
				e.add("mileage_based_pricing", "requires at least one mileage tier (its own, or global ones under Mileage)")
			}
			if len(e) > 0 {
				return 0, e
			}
			if fee.ID == 0 {
				fee.ID = nextID(b.Fees, func(x engine.Fee) int64 { return x.ID })
				if fee.Position == nil { // acts_as_list: a new fee goes to the bottom
					var p int64
					for _, o := range b.Fees {
						if o.Position != nil {
							p = max(p, *o.Position)
						}
					}
					p++
					fee.Position = &p
				}
			}
			b.Fees = upsert(b.Fees, func(x engine.Fee) int64 { return x.ID }, fee)
			return fee.ID, nil
		},
		del: func(b *engine.Book, id int64) string {
			// fee.rb: has_many :locations, :zip_codes, dependent: :restrict_with_error
			for _, l := range b.Locations {
				if l.FeeID != nil && *l.FeeID == id {
					return "location " + l.Name + " charges this fee; take it off there first"
				}
			}
			for _, z := range b.ZipCodes {
				if z.FeeID != nil && *z.FeeID == id {
					return "ZIP " + z.PostalCode + " charges this fee; take it off there first"
				}
			}
			b.Fees = without(b.Fees, func(x engine.Fee) int64 { return x.ID }, id)
			return ""
		},
	},
	{
		singular: "discount", plural: "discounts", title: "Discounts",
		list: func(b *engine.Book) []map[string]any {
			rows := []map[string]any{}
			for _, d := range b.Discounts {
				amount := money(d.Amount) + " off"
				if d.DiscountType == "percentage" {
					amount = fmt.Sprintf("%d%% off", d.Amount)
				}
				dur := "Indefinite"
				if d.DurationValue != nil {
					dur = fmt.Sprintf("%d %s", *d.DurationValue, d.DurationUnit)
				}
				rows = append(rows, map[string]any{"id": d.ID, "name": d.Name, "amount": amount, "duration": dur})
			}
			return rows
		},
		get: func(b *engine.Book, id int64) (map[string]any, bool) {
			d, ok := find(b.Discounts, func(x engine.Discount) int64 { return x.ID }, id)
			if !ok {
				return nil, false
			}
			return map[string]any{"id": d.ID, "name": d.Name, "discount_type": d.DiscountType, "amount": d.Amount, "duration_unit": strOr(d.DurationUnit, "indefinite"), "duration_value": ptrVal(d.DurationValue)}, true
		},
		put: func(b *engine.Book, id int64, rec map[string]any) (int64, map[string][]string) {
			f, e := form(rec), errs{}
			d := engine.Discount{ID: id, Name: f.str("name"), DiscountType: strOr(f.str("discount_type"), "fixed"), Amount: e.need(f, "amount"),
				DurationUnit: strOr(f.str("duration_unit"), "indefinite"), DurationValue: e.opt(f, "duration_value")}
			if d.DurationUnit == "indefinite" {
				d.DurationValue = nil // normalize_duration
			}
			if len(e) > 0 {
				return 0, e
			}
			if d.ID == 0 {
				d.ID = nextID(b.Discounts, func(x engine.Discount) int64 { return x.ID })
			}
			b.Discounts = upsert(b.Discounts, func(x engine.Discount) int64 { return x.ID }, d)
			return d.ID, nil
		},
		del: func(b *engine.Book, id int64) string {
			for i := range b.Fees {
				nullify(&b.Fees[i].DiscountID, id)
			}
			for i := range b.Products {
				nullify(&b.Products[i].DiscountID, id)
			}
			for i := range b.Zones {
				nullify(&b.Zones[i].DiscountID, id)
			}
			for i := range b.Locations {
				nullify(&b.Locations[i].DiscountID, id)
			}
			for i := range b.ZipCodes {
				nullify(&b.ZipCodes[i].DiscountID, id)
			}
			for i := range b.ServiceTypes {
				nullify(&b.ServiceTypes[i].DiscountID, id)
			}
			for i := range b.ProductTypes {
				nullify(&b.ProductTypes[i].DiscountID, id)
			}
			b.Discounts = without(b.Discounts, func(x engine.Discount) int64 { return x.ID }, id)
			return ""
		},
	},
	{
		singular: "zone", plural: "zones", title: "Zones",
		list: func(b *engine.Book) []map[string]any {
			rows := []map[string]any{}
			for _, z := range b.Zones {
				n := 0
				for _, zc := range b.ZipCodes {
					if zc.ZoneID != nil && *zc.ZoneID == z.ID {
						n++
					}
				}
				rows = append(rows, map[string]any{"id": z.ID, "name": z.Name, "location": locationName(b, z.LocationID), "discount": discountName(b, z.DiscountID), "zips": n})
			}
			return rows
		},
		get: func(b *engine.Book, id int64) (map[string]any, bool) {
			z, ok := find(b.Zones, zID, id)
			if !ok {
				return nil, false
			}
			var pickup, delivery []string
			for _, f := range b.Fees {
				if slices.Contains(f.FromZoneIDs, id) {
					pickup = append(pickup, fmt.Sprintf("%s (#%d)", f.Name, f.ID))
				}
				if slices.Contains(f.ToZoneIDs, id) {
					delivery = append(delivery, fmt.Sprintf("%s (#%d)", f.Name, f.ID))
				}
			}
			n := 0
			for _, zc := range b.ZipCodes {
				if zc.ZoneID != nil && *zc.ZoneID == id {
					n++
				}
			}
			return map[string]any{"id": z.ID, "name": z.Name, "location": locationName(b, z.LocationID), "discount": discountName(b, z.DiscountID),
				"linked": []map[string]any{{"label": "ZIPs", "value": strconv.Itoa(n)}, {"label": "Fees from this zone", "value": names(pickup), "multiline": true},
					{"label": "Fees to this zone", "value": names(delivery), "multiline": true}}}, true
		},
		put: func(b *engine.Book, id int64, rec map[string]any) (int64, map[string][]string) {
			f, e := form(rec), errs{}
			z := engine.Zone{ID: id, Name: f.str("name"), DiscountID: e.discountID(b, f, "discount")}
			loc := idsOf(e, "location", []string{f.str("location")}, b.Locations, func(l engine.Location) int64 { return l.ID }, func(l engine.Location) string { return l.Name }, "location")
			if f.str("location") == "" {
				e.add("location", "can't be blank")
			} else if len(loc) == 1 {
				z.LocationID = loc[0]
			}
			if len(e) > 0 {
				return 0, e
			}
			if z.ID == 0 {
				z.ID = nextID(b.Zones, zID)
			}
			b.Zones = upsert(b.Zones, zID, z)
			return z.ID, nil
		},
		del: func(b *engine.Book, id int64) string {
			for _, z := range b.ZipCodes {
				if z.ZoneID != nil && *z.ZoneID == id {
					return "ZIP " + z.PostalCode + " is in this zone; move its ZIPs first"
				}
			}
			for i := range b.Fees {
				b.Fees[i].FromZoneIDs = drop(b.Fees[i].FromZoneIDs, id)
				b.Fees[i].ToZoneIDs = drop(b.Fees[i].ToZoneIDs, id)
			}
			b.Zones = without(b.Zones, zID, id)
			return ""
		},
	},
	{
		singular: "location", plural: "locations", title: "Locations",
		list: func(b *engine.Book) []map[string]any {
			rows := []map[string]any{}
			for _, l := range b.Locations {
				rows = append(rows, map[string]any{"id": l.ID, "name": l.Name, "phone": l.Phone, "fee": feeLabel(b, l.FeeID), "discount": discountName(b, l.DiscountID)})
			}
			return rows
		},
		get: func(b *engine.Book, id int64) (map[string]any, bool) {
			l, ok := find(b.Locations, func(x engine.Location) int64 { return x.ID }, id)
			if !ok {
				return nil, false
			}
			return map[string]any{"id": l.ID, "name": l.Name, "phone": l.Phone, "fee_id": idOr0(l.FeeID), "discount": discountName(b, l.DiscountID)}, true
		},
		put: func(b *engine.Book, id int64, rec map[string]any) (int64, map[string][]string) {
			f, e := form(rec), errs{}
			l := engine.Location{ID: id, Name: f.str("name"), Phone: f.str("phone"), FeeID: e.feeID(b, f, "fee_id"), DiscountID: e.discountID(b, f, "discount")}
			for _, o := range b.Locations {
				if o.ID != id && strings.EqualFold(o.Name, l.Name) {
					e.add("name", "another location is named "+o.Name)
				}
			}
			if len(e) > 0 {
				return 0, e
			}
			if l.ID == 0 {
				l.ID = nextID(b.Locations, func(x engine.Location) int64 { return x.ID })
			}
			b.Locations = upsert(b.Locations, func(x engine.Location) int64 { return x.ID }, l)
			return l.ID, nil
		},
		del: func(b *engine.Book, id int64) string {
			for _, z := range b.Zones {
				if z.LocationID == id {
					return "zone " + z.Name + " belongs to this location; move or delete it first"
				}
			}
			for _, z := range b.ZipCodes {
				if z.LocationID == id {
					return "ZIP " + z.PostalCode + " belongs to this location; move its ZIPs first"
				}
			}
			b.Locations = without(b.Locations, func(x engine.Location) int64 { return x.ID }, id)
			return ""
		},
	},
	{
		singular: "service_type", plural: "service_types", title: "Service types",
		list: func(b *engine.Book) []map[string]any {
			rows := []map[string]any{}
			for _, s := range b.ServiceTypes {
				rows = append(rows, map[string]any{"id": s.ID, "name": s.Name, "slug": s.Slug, "discount": discountName(b, s.DiscountID)})
			}
			return rows
		},
		get: func(b *engine.Book, id int64) (map[string]any, bool) {
			s, ok := find(b.ServiceTypes, stID, id)
			if !ok {
				return nil, false
			}
			return map[string]any{"id": s.ID, "name": s.Name, "slug": s.Slug, "discount": discountName(b, s.DiscountID)}, true
		},
		put: func(b *engine.Book, id int64, rec map[string]any) (int64, map[string][]string) {
			f, e := form(rec), errs{}
			s := engine.ServiceType{ID: id, Name: f.str("name"), Slug: strOr(f.str("slug"), parameterize(f.str("name"))), DiscountID: e.discountID(b, f, "discount")}
			for _, o := range b.ServiceTypes {
				if o.ID != id && o.Slug == s.Slug {
					e.add("slug", "another service type is "+o.Slug)
				}
			}
			if len(e) > 0 {
				return 0, e
			}
			if s.ID == 0 {
				s.ID = nextID(b.ServiceTypes, stID)
			}
			b.ServiceTypes = upsert(b.ServiceTypes, stID, s)
			return s.ID, nil
		},
		del: func(b *engine.Book, id int64) string {
			for i := range b.Products {
				b.Products[i].ServiceTypeIDs = drop(b.Products[i].ServiceTypeIDs, id)
			}
			for i := range b.Fees {
				b.Fees[i].ServiceTypeIDs = drop(b.Fees[i].ServiceTypeIDs, id)
			}
			b.ServiceTypes = without(b.ServiceTypes, stID, id)
			return ""
		},
	},
	{
		singular: "product_type", plural: "product_types", title: "Product types",
		list: func(b *engine.Book) []map[string]any {
			rows := []map[string]any{}
			for _, p := range b.ProductTypes {
				rows = append(rows, map[string]any{"id": p.ID, "name": p.Name, "slug": p.Slug, "discount": discountName(b, p.DiscountID)})
			}
			return rows
		},
		get: func(b *engine.Book, id int64) (map[string]any, bool) {
			p, ok := find(b.ProductTypes, ptID, id)
			if !ok {
				return nil, false
			}
			return map[string]any{"id": p.ID, "name": p.Name, "slug": p.Slug, "discount": discountName(b, p.DiscountID)}, true
		},
		put: func(b *engine.Book, id int64, rec map[string]any) (int64, map[string][]string) {
			f, e := form(rec), errs{}
			p := engine.ProductType{ID: id, Name: f.str("name"), Slug: strOr(f.str("slug"), parameterize(f.str("name"))), DiscountID: e.discountID(b, f, "discount")}
			for _, o := range b.ProductTypes {
				if o.ID != id && o.Slug == p.Slug {
					e.add("slug", "another product type is "+o.Slug)
				}
			}
			if len(e) > 0 {
				return 0, e
			}
			if p.ID == 0 {
				p.ID = nextID(b.ProductTypes, ptID)
			}
			b.ProductTypes = upsert(b.ProductTypes, ptID, p)
			return p.ID, nil
		},
		del: func(b *engine.Book, id int64) string {
			for _, p := range b.Products {
				if p.ProductTypeID == id {
					return "product " + p.Name + " is of this type; change or delete it first"
				}
			}
			for i := range b.Fees {
				b.Fees[i].ProductTypeIDs = drop(b.Fees[i].ProductTypeIDs, id)
			}
			b.ProductTypes = without(b.ProductTypes, ptID, id)
			return ""
		},
	},
}

func nonBlank(ss []string) []string {
	out := []string{}
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// parameterize is ActiveSupport's String#parameterize, as Fuse makes slugs
// from names: lowercase ASCII words joined by "-".
func parameterize(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		} else {
			dash = true
		}
	}
	return b.String()
}

func (c *call) id() int64 {
	n, _ := strconv.ParseInt(c.req.Query.Get("id"), 10, 64)
	return n
}

func (c *call) listKind(k kind) *plugin.Response {
	b, _, err := loadDraft(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "draft_unreadable", "message": err.Error()})
	}
	return reply(200, map[string]any{k.plural: k.list(b)})
}

func (c *call) showKind(k kind) *plugin.Response {
	b, _, err := loadDraft(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "draft_unreadable", "message": err.Error()})
	}
	if c.id() == 0 {
		rec := map[string]any{}
		switch k.singular {
		case "product":
			rec = map[string]any{"active": true, "monthly": true}
		case "fee":
			rec = map[string]any{"applies_at": "pickup_location", "total_mode": "included", "tiers": []any{}}
		case "discount":
			rec = map[string]any{"discount_type": "fixed", "duration_unit": "indefinite"}
		}
		return reply(200, map[string]any{k.singular: rec})
	}
	rec, ok := k.get(b, c.id())
	if !ok {
		return notFound(fmt.Sprintf("No %s %d in the draft.", strings.ReplaceAll(k.singular, "_", " "), c.id()))
	}
	return reply(200, map[string]any{k.singular: rec})
}

func (c *call) saveKind(k kind) *plugin.Response {
	var body map[string]json.RawMessage
	var rec map[string]any
	if json.Unmarshal(c.req.Body, &body) != nil || json.Unmarshal(body[k.singular], &rec) != nil || rec == nil {
		return reply(400, map[string]string{"error": "bad_request", "message": "Send {\"" + k.singular + "\": {…}}."})
	}
	b, _, err := loadDraft(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "draft_unreadable", "message": err.Error()})
	}
	if id := c.id(); id != 0 {
		if _, ok := k.get(b, id); !ok {
			return notFound(fmt.Sprintf("No %s %d in the draft.", k.singular, id))
		}
	}
	nameErr := ""
	if n := len([]rune(form(rec).str("name"))); n < 2 || n > 100 {
		nameErr = "must be 2 to 100 characters"
	}
	id, e := k.put(b, c.id(), rec)
	if len(e) > 0 || nameErr != "" {
		if e == nil {
			e = map[string][]string{}
		}
		if nameErr != "" {
			e["name"] = append(e["name"], nameErr)
		}
		return invalid(e)
	}
	if ve := filterErrs(engine.Validate(b), k.plural, id, b); len(ve) > 0 {
		return invalid(ve)
	}
	if err := saveDraft(c.ctx, c.h, b, c.actor(), ""); err != nil {
		return reply(500, map[string]string{"error": "not_saved", "message": err.Error()})
	}
	out, _ := k.get(b, id)
	return reply(200, map[string]any{k.singular: out, "redirect": map[string]any{"screen": k.singular, "params": map[string]string{"id": strconv.FormatInt(id, 10)}}})
}

// filterErrs keeps the book's validation errors that are about the record
// just saved, keyed by its field names.
func filterErrs(all map[string][]string, plural string, id int64, b *engine.Book) map[string][]string {
	idx := -1
	switch plural {
	case "products":
		idx = slices.IndexFunc(b.Products, func(x engine.Product) bool { return x.ID == id })
	case "fees":
		idx = slices.IndexFunc(b.Fees, func(x engine.Fee) bool { return x.ID == id })
	case "discounts":
		idx = slices.IndexFunc(b.Discounts, func(x engine.Discount) bool { return x.ID == id })
	case "zones":
		idx = slices.IndexFunc(b.Zones, func(x engine.Zone) bool { return x.ID == id })
	case "locations":
		idx = slices.IndexFunc(b.Locations, func(x engine.Location) bool { return x.ID == id })
	case "service_types":
		idx = slices.IndexFunc(b.ServiceTypes, func(x engine.ServiceType) bool { return x.ID == id })
	case "product_types":
		idx = slices.IndexFunc(b.ProductTypes, func(x engine.ProductType) bool { return x.ID == id })
	}
	prefix := fmt.Sprintf("%s[%d].", plural, idx)
	out := map[string][]string{}
	for k, v := range all {
		if field, ok := strings.CutPrefix(k, prefix); ok {
			out[field] = v
		}
	}
	return out
}

func (c *call) deleteKind(k kind) *plugin.Response {
	b, _, err := loadDraft(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "draft_unreadable", "message": err.Error()})
	}
	if _, ok := k.get(b, c.id()); !ok {
		return notFound(fmt.Sprintf("No %s %d in the draft.", k.singular, c.id()))
	}
	if why := k.del(b, c.id()); why != "" {
		return reply(409, map[string]string{"error": "in_use", "message": "It can't be deleted: " + why + "."})
	}
	if err := saveDraft(c.ctx, c.h, b, c.actor(), ""); err != nil {
		return reply(500, map[string]string{"error": "not_saved", "message": err.Error()})
	}
	return reply(200, map[string]any{"ok": true, "message": "Deleted from the draft. Publish to make it live."})
}
