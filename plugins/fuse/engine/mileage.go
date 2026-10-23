package engine

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

// metersPerMile is MileagePricingService::METERS_PER_MILE.
const metersPerMile = 1609.344

// perHourMinimum is MileagePricingService::PER_HOUR_MINIMUM: a per_hour tier
// bills at least an hour.
const perHourMinimum = 1.0

// Route is what Google's Directions API answered for one request: each
// leg's meters and seconds, in order. A nil *Route is no answer (an error,
// a timeout, ZERO_RESULTS, no key), which Fuse treats the same in every case.
type Route struct {
	LegsMeters  []int64 `json:"legs_meters"`
	LegsSeconds []int64 `json:"legs_seconds"`
}

// RouteKey is one Directions request, as Fuse builds it: origin and
// destination as "<zip>, USA", waypoints as the bare ZIPs given.
type RouteKey struct {
	Origin, Destination string
	Waypoints           []string
}

// String is how the goldens' distances.json names the route.
func (k RouteKey) String() string {
	return k.Origin + " → " + k.Destination + " → " + strings.Join(k.Waypoints, "|")
}

// Directions answers a route, nil when there's no answer. The engine asks
// for exactly the routes PlanRoutes names, so a caller can fetch those
// first (in parallel, within a budget) and answer from what it has.
type Directions func(RouteKey) *Route

func zipQuery(zip string) string { return zip + ", USA" }

// PlanRoutes are the Directions calls MileagePricingService.calculate makes
// for a quote from initialZip (to finalZip, if any), in its order; none when
// it would make none (no business ZIP or no initial ZIP).
//
//	round trip, final ZIP:  biz→[i]→biz, biz→[i,f]→biz, biz→[f]→biz
//	round trip, none:       biz→[i]→biz twice (the same call, made twice)
//	one way, final ZIP:     biz→[i]→f
//	one way, none:          biz→i
func PlanRoutes(s Settings, initialZip, finalZip string) []RouteKey {
	if blank(s.Zip) || blank(initialZip) {
		return nil
	}
	origin := zipQuery(s.Zip)
	if s.MileageRoundTrip {
		if !blank(finalZip) {
			return []RouteKey{
				{origin, origin, []string{initialZip}},
				{origin, origin, []string{initialZip, finalZip}},
				{origin, origin, []string{finalZip}},
			}
		}
		return []RouteKey{{origin, origin, []string{initialZip}}, {origin, origin, []string{initialZip}}}
	}
	if !blank(finalZip) {
		return []RouteKey{{origin, zipQuery(finalZip), []string{initialZip}}}
	}
	return []RouteKey{{origin, zipQuery(initialZip), nil}}
}

// mileageResult is what MileagePricingService.calculate answers.
type mileageResult struct {
	DistanceMiles float64
	DurationHours float64
	FeeAmount     int64
	LegsMiles     []float64
	LegsHours     []float64
	LegsFees      []int64
	TierRateTypes []string
	TierRecords   []Tier
}

// calculateMileage is MileagePricingService.calculate: nil when it can't be
// had (no business ZIP, no initial ZIP, no answer for any phase).
//
// Fuse quirk: round-trip miles are each phase's meters summed then divided
// and rounded to 2 places, hours to 4; one-way miles are per leg. The total
// is Array#sum (compensated) then round(2).
func (x *Index) calculateMileage(initialZip, finalZip string, dirs Directions) *mileageResult {
	s := x.Book.Settings
	plan := PlanRoutes(s, initialZip, finalZip)
	if plan == nil || dirs == nil {
		return nil
	}
	var legsMiles, legsHours []float64
	if s.MileageRoundTrip {
		for _, k := range plan {
			r := dirs(k)
			if r == nil || !r.ok() {
				return nil
			}
			legsMiles = append(legsMiles, rubyRound(float64(sumInts(r.LegsMeters))/metersPerMile, 2))
			legsHours = append(legsHours, rubyRound(float64(sumInts(r.LegsSeconds))/3600.0, 4))
		}
	} else {
		r := dirs(plan[0])
		if r == nil || !r.ok() {
			return nil
		}
		for _, m := range r.LegsMeters {
			legsMiles = append(legsMiles, rubyRound(float64(m)/metersPerMile, 2))
		}
		for _, sec := range r.LegsSeconds {
			legsHours = append(legsHours, rubyRound(float64(sec)/3600.0, 4))
		}
	}
	tiers := sqlTierOrder(x.Book.MileageTiers, false) // MileageTier.order(:position)
	res := &mileageResult{
		DistanceMiles: rubyRound(rubySum(legsMiles), 2),
		DurationHours: rubyRound(rubySum(legsHours), 2),
		LegsMiles:     legsMiles,
		LegsHours:     legsHours,
		TierRecords:   tiers,
		TierRateTypes: rateTypes(tiers),
	}
	for i, m := range legsMiles {
		h := 0.0
		if i < len(legsHours) {
			h = legsHours[i]
		}
		fee := tierFee(tiers, m, h)
		res.LegsFees = append(res.LegsFees, fee)
		res.FeeAmount += fee
	}
	return res
}

// ok is Fuse's check on a Directions answer: some legs, more than 0 meters.
func (r *Route) ok() bool {
	return len(r.LegsMeters) > 0 && sumInts(r.LegsMeters) > 0
}

func sumInts(xs []int64) int64 {
	var n int64
	for _, x := range xs {
		n += x
	}
	return n
}

// rateTypes is tiers.map(&:rate_type).uniq.
func rateTypes(tiers []Tier) []string {
	var out []string
	for _, t := range tiers {
		if !slices.Contains(out, t.RateType) {
			out = append(out, t.RateType)
		}
	}
	return out
}

// tierFee is MileagePricingService.calculate_total_fee_from_tiers.
//
//	flat      the band's amount, when miles falls in [min, max]
//	per_mile  (amount × the miles inside this band).round — it stacks across
//	          bands, so a long trip pays every per_mile band it crosses
//	per_hour  (amount × max(hours, 1)).round, when miles falls in [min, max]
//
// Fuse quirk: a trip in no band costs 0; a trip on a band edge (30.0 with
// bands 15–30 and 30–60) pays both bands' flat amounts. Float#round is half
// away from zero, as math.Round is.
func tierFee(tiers []Tier, miles, hours float64) int64 {
	sorted := slices.Clone(tiers)
	rubySortBy(sorted, func(t Tier) float64 { return float64(t.MinMiles) })
	var total int64
	for _, t := range sorted {
		if miles < float64(t.MinMiles) {
			continue
		}
		if t.RateType != "per_mile" && miles > float64(t.MaxMiles) {
			continue
		}
		switch t.RateType {
		case "flat":
			total += t.Amount
		case "per_mile":
			// [miles, max_miles].min - min_miles: a Float when miles is the
			// smaller, else Integer arithmetic.
			var inTier float64
			if miles <= float64(t.MaxMiles) {
				inTier = miles - float64(t.MinMiles)
			} else {
				inTier = float64(t.MaxMiles - t.MinMiles)
			}
			total += int64(rubyRound(float64(t.Amount)*inTier, 0))
		case "per_hour":
			if hours <= 0 {
				continue
			}
			total += int64(rubyRound(float64(t.Amount)*max(hours, perHourMinimum), 0))
		}
	}
	return total
}

// rateDescription is MileagePricingService.rate_description: "2 Hour Route"
// when a per_hour band holds the trip, else "".
func rateDescription(tiers []Tier, miles, hours float64) string {
	applies := false
	for _, t := range tiers {
		if t.RateType == "per_hour" && miles >= float64(t.MinMiles) && miles <= float64(t.MaxMiles) {
			applies = true
			break
		}
	}
	if !applies || hours <= 0 {
		return ""
	}
	// rounded == rounded.to_i ? rounded.to_i.to_s : rounded.to_s
	rounded := rubyRound(max(hours, perHourMinimum), 1)
	if rounded == math.Trunc(rounded) {
		return strconv.FormatInt(int64(rounded), 10) + " Hour Route"
	}
	return rubyFloatString(rounded) + " Hour Route"
}

// mileageLabelSuffix is QuoteFeeCalculator#mileage_label_suffix: " (18.9 mi)"
// when a distance band (flat, per_mile) is in the schedule — or the schedule
// is empty — else " (0.7 hr)" when it's hourly, else "".
func mileageLabelSuffix(miles, hours float64, types []string) string {
	if len(types) == 0 || slices.Contains(types, "flat") || slices.Contains(types, "per_mile") {
		return " (" + rubyFloatString(rubyRound(miles, 1)) + " mi)"
	}
	if slices.Contains(types, "per_hour") {
		return " (" + rubyFloatString(rubyRound(hours, 1)) + " hr)"
	}
	return ""
}

// legRoles is QuoteFeeCalculator#leg_roles.
func legRoles(n int, roundTrip bool) []string {
	switch n {
	case 3:
		return []string{"initial", "move", "return"}
	case 2:
		if roundTrip {
			return []string{"initial", "return"}
		}
		return []string{"initial", "move"}
	case 1:
		return []string{"initial"}
	}
	out := make([]string, n)
	for i := range out {
		out[i] = "initial"
	}
	return out
}

var roleLabels = map[string]string{"initial": "Initial Delivery", "move": "Relocation", "return": "Final Pickup"}

// effectiveTiers is Fee#effective_mileage_tiers: the fee's own tiers
// (ordered by position), else the global ones (MileageTier.order(:position)).
func (x *Index) effectiveTiers(f *Fee) []Tier {
	if len(f.Tiers) > 0 {
		return sqlTierOrder(f.Tiers, false)
	}
	return sqlTierOrder(x.Book.MileageTiers, false)
}

// effectiveAmount is Fee#effective_amount: for a mileage-based fee, its
// effective tiers' .order(:min_miles).first amount, else its own.
//
// Fuse quirk: the tiers are already ordered by position, so .order(:min_miles)
// only breaks ties — the "Starting at" amount is the lowest-position tier's,
// not the shortest band's. The no-answer "Relocation Fee" line uses
// MileageTier.order(:min_miles) instead, so the two can differ.
func (x *Index) effectiveAmount(f *Fee) int64 {
	if !f.MileageBasedPricing {
		return f.Amount
	}
	var tiers []Tier
	if len(f.Tiers) > 0 {
		tiers = sqlTierOrder(f.Tiers, true)
	} else {
		tiers = sqlTierOrder(x.Book.MileageTiers, true)
	}
	if len(tiers) == 0 {
		return f.Amount
	}
	return tiers[0].Amount
}

// firstTierByMinMiles is MileageTier.order(:min_miles).first; ok is false
// when there are no tiers.
func (x *Index) firstTierByMinMiles() (Tier, bool) {
	if len(x.Book.MileageTiers) == 0 {
		return Tier{}, false
	}
	tiers := slices.Clone(x.Book.MileageTiers)
	slices.SortStableFunc(tiers, func(a, b Tier) int {
		if c := cmpInt(a.MinMiles, b.MinMiles); c != 0 {
			return c
		}
		return cmpInt(a.ID, b.ID)
	})
	return tiers[0], true
}
