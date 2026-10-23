package engine

import (
	"regexp"
	"slices"
)

// line is one entry of a quote's fee list. Fuse's lists mix two kinds, and
// the difference matters everywhere downstream:
//
//   - a Fee record (or a DiscountedFee wrapping one): its amount is per
//     unit, multiplied by the quote's total quantity (per_quantity: true);
//   - an OpenStruct the calculator built (a mileage leg, a "Starting at"
//     placeholder): its amount is absolute.
//
// For an OpenStruct, the "has*" flags say which attributes it was built
// with: Ruby's OpenStruct#eql? (Array#uniq) compares those, and
// respond_to?(:excluded_shown) is true only when it was set.
type line struct {
	record *Fee // a Fee record; nil for an OpenStruct

	name              string
	amount            int64
	excludedFromTotal bool
	excludedShown     bool
	hasExcludedShown  bool
	relocation        bool
	startingAt        bool
	pickup            bool
	position          float64
	deliveryLocation  bool
	source            *Fee
	rateDescription   string
	hasRateDesc       bool

	// After discounts: has_discount and original_amount.
	hasDiscount    bool
	originalAmount int64
}

func recordLine(f *Fee) *line {
	return &line{record: f, name: f.Name, amount: f.Amount}
}

func (l *line) isStruct() bool { return l.record == nil }

// excludedFromTotalFlag is what #excluded_from_total answers.
func (l *line) excludedFromTotalFlag() bool {
	if l.record != nil {
		return l.record.excludedFromTotal()
	}
	return l.excludedFromTotal
}

// relocationFlag is #relocation: an OpenStruct's attribute, a Fee's
// zone_to_zone?.
func (l *line) relocationFlag() bool {
	if l.record != nil {
		return l.record.relocation()
	}
	return l.relocation
}

// sortKey is fee.position || Float::INFINITY.
func (l *line) sortKey() float64 {
	if l.record != nil {
		if l.record.Position == nil {
			return inf
		}
		return float64(*l.record.Position)
	}
	return l.position
}

func positionOrInf(f *Fee) float64 {
	if f.Position == nil {
		return inf
	}
	return float64(*f.Position)
}

// same is Array#uniq's equality: a Fee record by id; an OpenStruct by all
// the attributes it was built with.
func (l *line) same(o *line) bool {
	if (l.record == nil) != (o.record == nil) {
		return false
	}
	if l.record != nil {
		return l.record.ID == o.record.ID
	}
	return l.name == o.name && l.amount == o.amount && l.excludedFromTotal == o.excludedFromTotal &&
		l.hasExcludedShown == o.hasExcludedShown && l.excludedShown == o.excludedShown &&
		l.relocation == o.relocation && l.startingAt == o.startingAt && l.pickup == o.pickup &&
		l.position == o.position && l.deliveryLocation == o.deliveryLocation && l.source == o.source &&
		l.hasRateDesc == o.hasRateDesc && l.rateDescription == o.rateDescription
}

func uniq(lines []*line) []*line {
	var out []*line
	for _, l := range lines {
		if !slices.ContainsFunc(out, l.same) {
			out = append(out, l)
		}
	}
	return out
}

// quote is one QuoteFeeCalculator.new(params).calculate(service_type).
type quote struct {
	x    *Index
	zips ZipSource
	dirs Directions

	zipCode, destZip, serviceSlug string
	zipRecord                     *ZipCode
	productType                   *ProductType // @product_type_record
	serviceType                   *ServiceType // @service_type_record

	destLooked bool
	destRecord *ZipCode // @destination_zip_record

	mileageDone bool
	mileage     *mileageResult // @params[:_mileage_result]
}

func (x *Index) newQuote(p Params, zips ZipSource, dirs Directions) *quote {
	q := &quote{x: x, zips: zips, dirs: dirs, zipCode: p.ZipCode, destZip: p.DestinationZip, serviceSlug: p.ServiceType}
	if !blank(p.ZipCode) {
		q.zipRecord = zips.Zip(p.ZipCode)
	}
	if !blank(p.ProductType) {
		q.productType = x.ptBySlug[p.ProductType]
	}
	if !blank(p.ServiceType) {
		q.serviceType = x.stBySlug[p.ServiceType]
	}
	return q
}

func (q *quote) destination() *ZipCode {
	if !q.destLooked {
		q.destLooked = true
		q.destRecord = q.zips.Zip(q.destZip)
	}
	return q.destRecord
}

// ensureMileage is ensure_mileage_calculated: the mileage for the quote's
// ZIPs, worked out the first time something needs it.
//
// Fuse quirk: per-fee mileage asks for it even when the setting
// mileage_based_pricing is off.
func (q *quote) ensureMileage() *mileageResult {
	if !q.mileageDone {
		q.mileageDone = true
		if !blank(q.zipCode) {
			final := ""
			if !blank(q.destZip) {
				final = q.destZip
			}
			q.mileage = q.x.calculateMileage(q.zipCode, final, q.dirs)
		}
	}
	return q.mileage
}

// matchesProductType is fee_matches_product_type?: a fee with no product
// types fits all, and so does every fee when the slug named no type.
func (q *quote) matchesProductType(f *Fee) bool {
	if q.productType == nil {
		return true
	}
	return len(f.ProductTypeIDs) == 0 || slices.Contains(f.ProductTypeIDs, q.productType.ID)
}

// matchesServiceType is fee_matches_service_type?.
//
// Fuse quirk: unlike the product type, a service slug that names no service
// type does not fall open: only fees for every service pass.
func (q *quote) matchesServiceType(f *Fee) bool {
	if len(f.ServiceTypeIDs) == 0 {
		return true
	}
	return q.serviceType != nil && slices.Contains(f.ServiceTypeIDs, q.serviceType.ID)
}

func (q *quote) matches(f *Fee) bool { return q.matchesServiceType(f) && q.matchesProductType(f) }

// serviceFees is @service_type_record.fees.where.not(total_mode: :excluded)
// .order(:position): positions NULL first, ties by id.
func (q *quote) serviceFees() []*Fee {
	if q.serviceType == nil {
		return nil
	}
	var out []*Fee
	for _, f := range q.x.feesByID {
		if !f.excluded() && slices.Contains(f.ServiceTypeIDs, q.serviceType.ID) {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(a, b *Fee) int { return cmpNullsFirst(a.Position, b.Position) })
	return out
}

func (q *quote) originZone() *Zone {
	if q.zipRecord == nil {
		return nil
	}
	return q.x.zone(q.zipRecord.ZoneID)
}

// zoneToZoneFees is calculate_zone_to_zone_fees.
func (q *quote) zoneToZoneFees() []*line {
	origin := q.originZone()
	if origin != nil && !blank(q.destZip) {
		dest := q.destination()
		if dest == nil || q.x.zone(dest.ZoneID) == nil {
			return nil
		}
		destZone := *dest.ZoneID
		var out []*line
		for _, f := range q.x.feesByID {
			if f.AppliesAt != zoneToZone || f.excluded() {
				continue
			}
			from := len(f.FromZoneIDs) == 0 || slices.Contains(f.FromZoneIDs, origin.ID)
			to := len(f.ToZoneIDs) == 0 || slices.Contains(f.ToZoneIDs, destZone)
			if from && to && q.matches(f) {
				out = append(out, recordLine(f))
			}
		}
		return out
	}
	// No origin zone or no destination: "Starting at" placeholders, the
	// cheapest of each name among the fees with no destination zones.
	//
	// Fuse quirk: this branch doesn't skip total_mode excluded fees, where
	// the exact branch does.
	var candidates []*Fee
	for _, f := range q.x.feesByID {
		if f.AppliesAt != zoneToZone || len(f.ToZoneIDs) > 0 || !q.matchesProductType(f) {
			continue
		}
		if origin != nil && len(f.FromZoneIDs) > 0 && !slices.Contains(f.FromZoneIDs, origin.ID) {
			continue
		}
		if q.matchesServiceType(f) {
			candidates = append(candidates, f)
		}
	}
	var out []*line
	for _, f := range cheapestByName(candidates) {
		out = append(out, &line{
			name: f.Name, amount: q.x.effectiveAmount(f), excludedFromTotal: true, relocation: true,
			startingAt: true, position: positionOrInf(f), source: f,
		})
	}
	return out
}

// cheapestByName is group_by(&:name).map { |_, g| g.min_by(&:amount) }:
// groups in order of first appearance, the first of the cheapest in each.
func cheapestByName(fees []*Fee) []*Fee {
	var names []string
	best := map[string]*Fee{}
	for _, f := range fees {
		b, seen := best[f.Name]
		if !seen {
			names = append(names, f.Name)
			best[f.Name] = f
		} else if f.Amount < b.Amount {
			best[f.Name] = f
		}
	}
	out := make([]*Fee, len(names))
	for i, n := range names {
		out[i] = best[n]
	}
	return out
}

// serviceTypeFees is calculate_service_type_fees.
func (q *quote) serviceTypeFees() []*line {
	if blank(q.serviceSlug) {
		return nil
	}
	var fees []*Fee
	for _, f := range q.serviceFees() {
		if q.matchesProductType(f) {
			fees = append(fees, f)
		}
	}
	var out []*line
	if zone := q.originZone(); zone != nil {
		for _, f := range fees {
			if f.AppliesAt == deliveryLocation || f.AppliesAt == zoneToZone {
				continue
			}
			if len(f.FromZoneIDs) == 0 || slices.Contains(f.FromZoneIDs, zone.ID) {
				out = append(out, recordLine(f))
			}
		}
		return out
	}
	var restricted []*Fee
	for _, f := range fees {
		if f.AppliesAt == deliveryLocation || f.AppliesAt == zoneToZone {
			continue
		}
		if len(f.FromZoneIDs) == 0 {
			out = append(out, recordLine(f))
		} else {
			restricted = append(restricted, f)
		}
	}
	// Fuse quirk: these placeholders carry fee.amount, where the
	// zone-to-zone ones carry effective_amount — for a mileage-based fee the
	// two differ.
	for _, f := range cheapestByName(restricted) {
		out = append(out, &line{
			name: f.Name, amount: f.Amount, excludedFromTotal: true, relocation: f.relocation(),
			startingAt: true, position: positionOrInf(f), source: f,
		})
	}
	return out
}

// locationBasedFees is calculate_location_based_fees: the ZIP's own fee,
// its zone's pickup fees, its location's fee — each pickup_location, not
// excluded, and for this service and product type.
func (q *quote) locationBasedFees() []*line {
	z := q.zipRecord
	if z == nil {
		return nil
	}
	var out []*line
	ok := func(f *Fee) bool { return f != nil && !f.excluded() && f.AppliesAt == pickupLocation && q.matches(f) }
	if f := q.x.fee(z.FeeID); ok(f) {
		out = append(out, recordLine(f))
	}
	if zone := q.x.zone(z.ZoneID); zone != nil {
		for _, f := range q.x.zonePickup[zone.ID] {
			if ok(f) {
				out = append(out, recordLine(f))
			}
		}
	}
	if l := q.x.locations[z.LocationID]; l != nil {
		if f := q.x.fee(l.FeeID); ok(f) {
			out = append(out, recordLine(f))
		}
	}
	return out
}

// finalZipFees is calculate_final_zip_fees: the destination zone's delivery
// fees, the destination ZIP's and location's own delivery fees, then the
// service's delivery_location fees for that zone (by id: find_each), each
// once.
func (q *quote) finalZipFees() []*line {
	if blank(q.destZip) {
		return nil
	}
	dest := q.destination()
	if dest == nil || q.x.zone(dest.ZoneID) == nil {
		return nil
	}
	zone := q.x.zone(dest.ZoneID)
	var fees []*Fee
	has := func(f *Fee) bool { return slices.ContainsFunc(fees, func(g *Fee) bool { return g.ID == f.ID }) }
	for _, f := range q.x.zoneDelivery[zone.ID] {
		if f.AppliesAt == deliveryLocation && !f.excluded() && q.matches(f) {
			fees = append(fees, f)
		}
	}
	own := func(f *Fee) {
		if f != nil && f.AppliesAt == deliveryLocation && !f.excluded() && !has(f) && q.matches(f) {
			fees = append(fees, f)
		}
	}
	own(q.x.fee(dest.FeeID))
	if l := q.x.locations[dest.LocationID]; l != nil {
		own(q.x.fee(l.FeeID))
	}
	if q.serviceType != nil {
		for _, f := range q.x.feesByID {
			if f.excluded() || f.AppliesAt != deliveryLocation || !slices.Contains(f.ServiceTypeIDs, q.serviceType.ID) || has(f) {
				continue
			}
			if (len(f.ToZoneIDs) == 0 || slices.Contains(f.ToZoneIDs, zone.ID)) && q.matchesProductType(f) {
				fees = append(fees, f)
			}
		}
	}
	out := make([]*line, len(fees))
	for i, f := range fees {
		out[i] = recordLine(f)
	}
	return out
}

// mileageFees is calculate_mileage_fee: the route's legs as lines, when the
// setting is on.
//
// With no distance, move-it gets one "Relocation Fee" "Starting at" line at
// the shortest band's amount (MileageTier.order(:min_miles).first), every
// other service nothing.
func (q *quote) mileageFees() []*line {
	if !q.x.Book.Settings.MileageBasedPricing {
		return nil
	}
	res := q.ensureMileage()
	if res == nil {
		// Fuse quirk: "move-it" is the literal slug, whatever the tenant
		// calls its relocation service.
		if q.serviceSlug != "move-it" {
			return nil
		}
		t, ok := q.x.firstTierByMinMiles()
		if !ok {
			return nil
		}
		return []*line{{name: "Relocation Fee", amount: t.Amount, excludedFromTotal: true, relocation: true, startingAt: true, position: inf}}
	}
	return q.splitMileage(res)
}

// splitMileage is split_mileage_fees: a line per role (Initial Delivery,
// Relocation, Final Pickup), each that leg's own tier fee.
func (q *quote) splitMileage(res *mileageResult) []*line {
	roles := legRoles(len(res.LegsMiles), q.x.Book.Settings.MileageRoundTrip)
	type agg struct {
		miles, hours float64
		fee          int64
		seen         bool
	}
	grouped := map[string]*agg{}
	for i, role := range roles {
		g := grouped[role]
		if g == nil {
			g = &agg{}
			grouped[role] = g
		}
		g.seen = true
		g.miles += res.LegsMiles[i]
		if i < len(res.LegsHours) {
			g.hours += res.LegsHours[i]
		}
		if i < len(res.LegsFees) {
			g.fee += res.LegsFees[i]
		}
	}
	var out []*line
	for _, role := range []string{"initial", "move", "return"} {
		g := grouped[role]
		if g == nil {
			continue
		}
		out = append(out, &line{
			name:   roleLabels[role] + mileageLabelSuffix(g.miles, g.hours, res.TierRateTypes),
			amount: g.fee, relocation: role == "move", pickup: role == "return", position: inf,
			rateDescription: rateDescription(res.TierRecords, g.miles, g.hours), hasRateDesc: true,
		})
	}
	return out
}

// mileSuffix is strip_mi_suffix's pattern: a trailing "(18.9 mi)",
// "(0.7 hr)" or "(18.9 mi · 0.7 hr)".
var mileSuffix = regexp.MustCompile(`\s*\([^)]*(?:mi|hr)[^)]*\)\s*\z`)

// dropCollidingFlat is drop_colliding_flat: Fee records (not calculator
// lines, not mileage-based fees) named like a live mileage leg go.
func dropCollidingFlat(lines []*line, names map[string]bool) []*line {
	var out []*line
	for _, l := range lines {
		if l.record != nil && !l.record.MileageBasedPricing && names[rubyStrip(l.record.Name)] {
			continue
		}
		out = append(out, l)
	}
	return out
}

// calculate is QuoteFeeCalculator#calculate's all_fees: the quote's fee
// lines, in Fuse's order, discounts applied.
func (q *quote) calculate() []*line {
	z2z := q.zoneToZoneFees()
	st := q.serviceTypeFees()
	loc := q.locationBasedFees()
	final := q.finalZipFees()
	mileage := q.mileageFees()

	live := map[string]bool{}
	for _, m := range mileage {
		if !m.startingAt {
			live[rubyStrip(mileSuffix.ReplaceAllString(m.name, ""))] = true
		}
	}
	if len(live) > 0 {
		st = dropCollidingFlat(st, live)
		loc = dropCollidingFlat(loc, live)
		final = dropCollidingFlat(final, live)
		z2z = dropCollidingFlat(z2z, live)
	}

	var all []*line
	if slices.ContainsFunc(mileage, func(l *line) bool { return l.relocation }) {
		// A mileage relocation replaces the zone-to-zone and destination fees.
		var nonRelo []*line
		for _, l := range append(slices.Clone(st), loc...) {
			if !l.relocationFlag() {
				nonRelo = append(nonRelo, l)
			}
		}
		all = uniq(nonRelo)
	} else {
		all = uniq(slices.Concat(z2z, st, loc, final))
	}
	// Fuse quirk: Ruby's sort_by isn't stable past 16 lines (see rubySortBy).
	rubySortBy(all, (*line).sortKey)
	all = append(all, mileage...)

	all = q.perFeeMileage(all)

	for i, l := range all {
		all[i] = q.applyDiscount(l)
	}
	return all
}

// perFeeMileage is apply_per_fee_mileage_pricing: a mileage-based Fee
// record becomes a line priced from its tiers over the whole route's miles
// and hours — or, with no distance, a "Starting at" placeholder at its
// effective amount.
func (q *quote) perFeeMileage(all []*line) []*line {
	if !slices.ContainsFunc(all, func(l *line) bool { return l.record != nil && l.record.MileageBasedPricing }) {
		return all
	}
	res := q.ensureMileage()
	out := make([]*line, len(all))
	for i, l := range all {
		out[i] = l
		f := l.record
		if f == nil || !f.MileageBasedPricing {
			continue
		}
		tiers := q.x.effectiveTiers(f)
		if len(tiers) == 0 {
			continue
		}
		if res != nil {
			// Fuse quirk: the whole route's miles (all legs), not a leg's.
			out[i] = &line{
				name:   f.Name + mileageLabelSuffix(res.DistanceMiles, res.DurationHours, rateTypes(tiers)),
				amount: tierFee(tiers, res.DistanceMiles, res.DurationHours), excludedFromTotal: f.excludedFromTotal(),
				excludedShown: f.TotalMode == modeExcludedShown, hasExcludedShown: true, relocation: f.relocation(),
				startingAt: f.displayAsStartingAt(), position: positionOrInf(f), deliveryLocation: f.AppliesAt == deliveryLocation,
				source: f, rateDescription: rateDescription(tiers, res.DistanceMiles, res.DurationHours), hasRateDesc: true,
			}
		} else {
			out[i] = &line{
				name: f.Name, amount: q.x.effectiveAmount(f), excludedFromTotal: true, relocation: f.relocation(),
				startingAt: true, position: positionOrInf(f), deliveryLocation: f.AppliesAt == deliveryLocation, source: f,
			}
		}
	}
	return out
}

// applyDiscount is apply_fee_discount: a Fee with a discount becomes a
// DiscountedFee (per unit, discounted); a calculator line made from a fee
// with one gets the discount on its computed amount. Only ever the fee's
// own discount — zone, ZIP and service discounts are for products — and
// never on a global mileage leg (no source fee).
func (q *quote) applyDiscount(l *line) *line {
	src := l.record
	if src == nil {
		src = l.source
	}
	if src == nil {
		return l
	}
	d := q.x.discount(src.DiscountID)
	if d == nil {
		return l
	}
	c := *l
	c.originalAmount = l.amount
	c.amount = discountedAmount(d, l.amount)
	c.hasDiscount = true
	return &c
}
