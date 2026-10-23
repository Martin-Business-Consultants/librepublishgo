package fuse

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/plugins/fuse/engine"
)

var zip5 = regexp.MustCompile(`^\d{5}$`)

// quoteRequest is what /quote takes: the query string on a GET, the same
// fields as JSON on a POST (plus quantities and mint).
type quoteRequest struct {
	Zip         string         `json:"zip"`
	Dest        string         `json:"dest"`
	ProductType *string        `json:"product_type"`
	Services    any            `json:"services"` // "all", "a,b", or ["a", "b"]
	DestFor     any            `json:"dest_for"` // which services get dest: "move-it" (the default), "all", a list
	Selected    string         `json:"selected"`
	Product     int64          `json:"product"`
	Qty         int64          `json:"qty"`
	Quantities  map[string]any `json:"quantities"`
	Token       string         `json:"t"`
	Name        string         `json:"name"`
	Mint        bool           `json:"mint"`
	BudgetMS    int64          `json:"budget_ms"`
}

func readQuoteRequest(req *plugin.Request) (quoteRequest, bool) {
	var q quoteRequest
	if req.Method == http.MethodPost {
		if len(req.Body) > 0 && json.Unmarshal(req.Body, &q) != nil {
			return q, false
		}
		return q, true
	}
	v := req.Query
	q.Zip, q.Dest, q.Selected, q.Token, q.Name = v.Get("zip"), v.Get("dest"), v.Get("selected"), v.Get("t"), v.Get("name")
	if v.Has("product_type") {
		pt := v.Get("product_type")
		q.ProductType = &pt
	}
	if v.Has("services") {
		q.Services = v.Get("services")
	}
	if v.Has("dest_for") {
		q.DestFor = v.Get("dest_for")
	}
	q.Product, _ = strconv.ParseInt(v.Get("product"), 10, 64)
	q.Qty, _ = strconv.ParseInt(v.Get("qty"), 10, 64)
	q.BudgetMS, _ = strconv.ParseInt(v.Get("budget_ms"), 10, 64)
	return q, true
}

// list reads "all", "a,b" or ["a","b"]; all is true for "all".
func list(v any, dflt []string) (names []string, all bool) {
	switch x := v.(type) {
	case nil:
		return dflt, false
	case string:
		x = strings.TrimSpace(x)
		if x == "all" {
			return nil, true
		}
		if x == "" {
			return []string{}, false
		}
		for s := range strings.SplitSeq(x, ",") {
			if s = strings.TrimSpace(s); s != "" {
				names = append(names, s)
			}
		}
		return names, false
	case []any:
		for _, s := range x {
			if str, ok := s.(string); ok && strings.TrimSpace(str) != "" {
				if str == "all" {
					return nil, true
				}
				names = append(names, strings.TrimSpace(str))
			}
		}
		return names, false
	}
	return dflt, false
}

// serviceView is one service in /quote's answer.
type serviceView struct {
	Name      string                  `json:"name,omitempty"`
	Available bool                    `json:"available"`
	Products  []engine.CatalogProduct `json:"products,omitempty"`
	ProductID int64                   `json:"product_id,omitempty"`
	Dest      string                  `json:"dest,omitempty"` // the destination it was priced to
	Preview   *engine.Preview         `json:"preview,omitempty"`
	Summary   *summary                `json:"summary,omitempty"`
	Error     string                  `json:"error,omitempty"`
}

// summary is the arithmetic every thank-you page does with a preview, done
// once: per-quantity amounts times the quantity, recurring apart from
// one-time, included apart from excluded. No site's display rule is
// applied: every line is here, with its flags.
type summary struct {
	Monthly         int64         `json:"monthly"`     // list price × quantity
	FirstMonth      int64         `json:"first_month"` // the products' subtotal, discounts applied
	Promo           int64         `json:"promo"`       // monthly − first_month
	Discount        *discountView `json:"discount,omitempty"`
	RecurringFees   []summaryLine `json:"recurring_fees"`
	RecurringTotal  int64         `json:"recurring_total"`
	Transit         []summaryLine `json:"transit"` // the one-time lines
	IncludedOneTime int64         `json:"included_one_time"`
	ExcludedOneTime int64         `json:"excluded_one_time"`
	Due             int64         `json:"due"` // == preview.total
}

type discountView struct {
	Name          string `json:"name"`
	Label         string `json:"label"`
	DurationLabel string `json:"duration_label"`
}

type summaryLine struct {
	Name              string `json:"name"`
	Amount            int64  `json:"amount"`
	StartingAt        bool   `json:"starting_at"`
	ExcludedFromTotal bool   `json:"excluded_from_total"`
	ExcludedShown     bool   `json:"excluded_shown"`
	RateDescription   string `json:"rate_description,omitempty"`
}

func summarize(p *engine.Preview, catalog []engine.CatalogProduct) *summary {
	s := &summary{RecurringFees: []summaryLine{}, Transit: []summaryLine{}}
	for _, pr := range p.Products {
		s.Monthly += pr.Price * pr.Quantity
		s.FirstMonth += pr.Subtotal
		if s.Discount == nil && pr.Discount != nil {
			for _, c := range catalog {
				if c.ID == pr.ID && c.Discount != nil && c.Discount.Name == pr.Discount.Name {
					s.Discount = &discountView{c.Discount.Name, c.Discount.Label, c.Discount.DurationLabel}
				}
			}
			if s.Discount == nil {
				s.Discount = &discountView{Name: pr.Discount.Name, DurationLabel: pr.DiscountDurationLabel}
			}
		}
	}
	s.Promo = s.Monthly - s.FirstMonth
	for _, f := range p.Fees {
		amount := f.Amount
		if f.PerQuantity {
			amount *= p.TotalQuantity
		}
		l := summaryLine{Name: f.Name, Amount: amount, StartingAt: f.StartingAt, ExcludedFromTotal: f.ExcludedFromTotal, ExcludedShown: f.ExcludedShown, RateDescription: f.RateDescription}
		switch {
		case f.Recurring:
			s.RecurringFees = append(s.RecurringFees, l)
			if !f.ExcludedFromTotal {
				s.RecurringTotal += amount
			}
		case f.ExcludedFromTotal:
			s.Transit = append(s.Transit, l)
			s.ExcludedOneTime += amount
		default:
			s.Transit = append(s.Transit, l)
			s.IncludedOneTime += amount
		}
	}
	s.Due = s.FirstMonth + s.RecurringTotal + s.IncludedOneTime
	return s
}

// quoteAnswer is /quote's answer.
type quoteAnswer struct {
	RatesVersion  string                  `json:"rates_version"`
	Zip           string                  `json:"zip"`
	Dest          string                  `json:"dest"`
	ProductType   string                  `json:"product_type"`
	ServiceArea   engine.ServiceArea      `json:"service_area"`
	LocationPhone string                  `json:"location_phone,omitempty"`
	OutOfAreaText string                  `json:"out_of_area_text,omitempty"`
	Labels        map[string]string       `json:"labels"`
	ServiceOrder  []string                `json:"service_order"`
	Services      map[string]*serviceView `json:"services"`
	SaleFrom      *saleFrom               `json:"sale_from,omitempty"`
	Distance      distanceReport          `json:"distance"`
	Quoted        *quotedView             `json:"quoted,omitempty"`
	Token         string                  `json:"token,omitempty"`
}

type saleFrom struct {
	ProductID int64 `json:"product_id"`
	Price     int64 `json:"price"`
}

// quotedView is what a trusted token said: valid is always true, since a
// token that can't be trusted is answered 422 invalid_token instead.
type quotedView struct {
	Valid        bool          `json:"valid"`
	Payload      *TokenPayload `json:"payload,omitempty"`
	RatesChanged *bool         `json:"rates_changed,omitempty"`
}

// quote is GET|POST /quote: every asked-for service priced for a ZIP (and
// destination) in one call, from the published rates, and a signed token
// for the selected one when asked (POST, mint: true). With t, the token is
// checked first: a genuine, unexpired one has its inputs priced again;
// any other is 422 invalid_token, nothing priced and nothing of it echoed.
func (c *call) quote() *plugin.Response {
	q, ok := readQuoteRequest(c.req.Request)
	if !ok {
		return reply(400, map[string]string{"error": "bad_request", "message": "Send the fields as JSON."})
	}
	var quoted *quotedView
	if q.Token != "" {
		var reason string
		if quoted, reason = c.readToken(&q); reason != "" {
			return reply(422, map[string]string{"error": "invalid_token", "reason": reason, "message": tokenMessages[reason]})
		}
	}
	q.Zip, q.Dest = strings.TrimSpace(q.Zip), strings.TrimSpace(q.Dest)
	if !zip5.MatchString(q.Zip) {
		return reply(400, map[string]string{"error": "invalid_zip", "message": "zip must be a 5-digit ZIP."})
	}
	if q.Dest != "" && !zip5.MatchString(q.Dest) {
		return reply(400, map[string]string{"error": "invalid_dest", "message": "dest must be a 5-digit ZIP, or left out."})
	}
	v, err := loadActive(c.ctx, c.h)
	if err != nil {
		c.h.Log("loading the rates: " + err.Error())
		return reply(503, map[string]string{"error": "rates_unreadable", "message": "The published rates can't be read."})
	}
	if v == nil {
		return reply(409, map[string]string{"error": "no_rates", "message": "No rates are published yet."})
	}
	res := c.priceAll(v, q)
	if quoted != nil && quoted.Payload != nil {
		changed := quoted.Payload.RV != v.active.Version
		quoted.RatesChanged = &changed
	}
	res.Quoted = quoted
	if q.Mint && c.req.Method == http.MethodPost {
		tok, why := c.mint(v, q, res)
		if why != "" {
			return reply(422, map[string]any{"error": "cannot_mint", "message": why})
		}
		res.Token = tok
	}
	return replyRaw(200, map[string]string{"Content-Type": "application/json; charset=utf-8", "Cache-Control": "private, max-age=60"}, mustJSON(res))
}

// tokenMessages say each invalid_token reason in words. None repeats
// anything the token carried.
var tokenMessages = map[string]string{
	"malformed":     "t isn't a quote token.",
	"unknown_key":   "The token was signed with a key this site doesn't have.",
	"bad_signature": "The token's signature doesn't match: it was changed.",
	"expired":       "The quote has passed its valid-until date.",
}

// readToken checks q's token and, only when it can be trusted, makes its
// inputs q's; otherwise the second answer is why not (tokenReason) and q
// is left as it was.
func (c *call) readToken(q *quoteRequest) (*quotedView, string) {
	p, err := verifyToken(c.ctx, c.h, q.Token)
	if err == nil && !zip5.MatchString(p.Z) {
		err = errTokenShape
	}
	if err != nil {
		return nil, tokenReason(err)
	}
	q.Zip, q.Dest, q.Selected = p.Z, p.DZ, p.ST
	pt := p.PT
	q.ProductType = &pt
	if p.PID > 0 {
		q.Product, q.Qty = p.PID, p.Q
	}
	return &quotedView{Valid: true, Payload: &p}, ""
}

// services are the services q asks for: the named ones, else every one with
// a product of the type, in name order (Fuse's config order); the selected
// one always.
func requestedServices(x *engine.Index, q quoteRequest, pt string, catalog engine.Catalog) []string {
	services, all := list(q.Services, nil)
	if all || q.Services == nil {
		services = nil
		for _, st := range catalog.ServiceTypes {
			if len(x.LoadProducts(pt, st.Slug)) > 0 {
				services = append(services, st.Slug)
			}
		}
	}
	if q.Selected != "" && !slices.Contains(services, q.Selected) {
		services = append(services, q.Selected)
	}
	return services
}

// planRoutes is every Directions request the services need, once each;
// skipped says some weren't planned because a ZIP isn't in the table and
// only ZIPs in it go to Google.
func planRoutes(x *engine.Index, zips engine.ZipSource, cfg config, zip string, services []string, destOf func(string) string) (keys []engine.RouteKey, skipped bool) {
	if !x.Book.Settings.MileageBasedPricing && !slices.ContainsFunc(x.Book.Fees, func(f engine.Fee) bool { return f.MileageBasedPricing }) {
		return nil, false
	}
	seen := map[string]bool{}
	for _, st := range services {
		dest := destOf(st)
		if cfg.KnownZipsOnly && (zips.Zip(zip) == nil || dest != "" && zips.Zip(dest) == nil) {
			skipped = true
			continue
		}
		for _, k := range engine.PlanRoutes(x.Book.Settings, zip, dest) {
			if !seen[k.String()] {
				seen[k.String()] = true
				keys = append(keys, k)
			}
		}
	}
	return keys, skipped
}

// priceAll prices each service, Google's routes fetched first, together.
func (c *call) priceAll(v *version, q quoteRequest) *quoteAnswer {
	x := v.index
	zips := v.zips(c.ctx, c.h)
	pt := "portable-storage"
	if q.ProductType != nil {
		pt = strings.TrimSpace(*q.ProductType)
	}
	catalog := x.Catalog(q.Zip, "", zips)
	services := requestedServices(x, q, pt, catalog)
	destFor, destAll := list(q.DestFor, []string{"move-it"})
	destOf := func(st string) string {
		if q.Dest != "" && (destAll || slices.Contains(destFor, st)) {
			return q.Dest
		}
		return ""
	}

	cfg := loadConfig(c.ctx, c.h)
	keys, skipped := planRoutes(x, zips, cfg, q.Zip, services, destOf)
	budget := distanceBudget
	if q.BudgetMS > 0 && time.Duration(q.BudgetMS)*time.Millisecond < budget {
		budget = max(time.Duration(q.BudgetMS)*time.Millisecond, 100*time.Millisecond)
	}
	routes, rep := fetchRoutes(c.ctx, c.h, cfg, keys, budget)
	if skipped && len(keys) == 0 {
		rep.Status = "skipped"
	}
	dirs := func(k engine.RouteKey) *engine.Route { return routes[k.String()] }

	views := map[string]*serviceView{}
	for _, st := range services {
		views[st] = c.priceService(x, zips, q, st, pt, destOf(st), dirs)
	}
	sa, _ := x.ServiceAreaFor(q.Zip, zips)
	out := &quoteAnswer{
		RatesVersion: v.active.Version, Zip: q.Zip, Dest: q.Dest, ProductType: pt, ServiceArea: sa,
		LocationPhone: catalog.LocationPhone, Labels: map[string]string{"total": strOr(x.Book.Settings.TotalPriceLabel, "Total")},
		ServiceOrder: services, Services: views, Distance: rep,
	}
	if zips.Zip(q.Zip) == nil {
		out.OutOfAreaText = x.Book.Settings.OutOfAreaZipText
	}
	// The cheapest priced unit for sale of the product type: "Starting at".
	for _, p := range catalog.Products {
		if p.ForSale && p.Price != nil && *p.Price > 0 && (pt == "" || p.ProductType == pt) && (out.SaleFrom == nil || *p.Price < out.SaleFrom.Price) {
			out.SaleFrom = &saleFrom{p.ID, *p.Price}
		}
	}
	return out
}

// priceService is one service of a quote: its catalog, its preview (Fuse's
// /preview) and the summary.
func (c *call) priceService(x *engine.Index, zips engine.ZipSource, q quoteRequest, st, pt, dest string, dirs engine.Directions) *serviceView {
	sv := &serviceView{}
	ref := x.ServiceTypeBySlug(st)
	if ref == nil {
		return sv
	}
	sv.Name = ref.Name
	if len(x.LoadProducts(pt, st)) == 0 {
		return sv
	}
	sv.Available = true
	for _, p := range x.Catalog(q.Zip, st, zips).Products {
		if pt == "" || p.ProductType == pt {
			sv.Products = append(sv.Products, p)
		}
	}
	params := engine.Params{ServiceType: st, ProductType: pt, ZipCode: q.Zip, DestinationZip: dest}
	if st == q.Selected {
		switch {
		case len(q.Quantities) > 0:
			params.Quantities = q.Quantities
		case q.Product > 0:
			params.Quantities = map[string]any{strconv.FormatInt(q.Product, 10): float64(max(q.Qty, 1))}
		}
	}
	pv, err := x.Preview(params, zips, dirs)
	if err != nil {
		sv.Error = err.Error()
		return sv
	}
	sv.Dest, sv.Preview = dest, pv
	if len(pv.Products) > 0 {
		sv.ProductID = pv.Products[0].ID
	}
	sv.Summary = summarize(pv, sv.Products)
	return sv
}

// mint signs the selected service's figures; the second answer, when not
// "", is why it can't.
func (c *call) mint(v *version, q quoteRequest, res *quoteAnswer) (string, string) {
	sv := res.Services[q.Selected]
	if q.Selected == "" || sv == nil || !sv.Available || sv.Preview == nil {
		return "", "selected must name a service that has a price for this ZIP"
	}
	p := TokenPayload{
		Iat: now().Unix(), RV: v.active.Version, PT: res.ProductType, ST: q.Selected,
		Z: q.Zip, DZ: sv.Dest, FN: firstName(q.Name),
	}
	pv := sv.Preview
	if len(pv.Products) == 1 {
		p.PID, p.Q = pv.Products[0].ID, pv.Products[0].Quantity
	}
	figs := TokenFigs{Products: []TokenProduct{}, Fees: []TokenFee{}, Sub: pv.Subtotal, FT: pv.FeesTotal, TD: pv.TotalDiscount, Tot: pv.Total,
		Dist: res.Distance.Status}
	for _, pr := range pv.Products {
		figs.Products = append(figs.Products, TokenProduct{pr.Name, pr.Quantity, pr.OriginalPrice, pr.Subtotal})
	}
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	for _, f := range pv.Fees {
		figs.Fees = append(figs.Fees, TokenFee{f.Name, f.Amount, b(f.Recurring), b(f.StartingAt), b(f.ExcludedFromTotal), b(f.PerQuantity)})
	}
	p.Fig = figs
	if d := v.index.Book.Settings.QuoteValidityDays; d != nil && *d > 0 {
		p.VU = now().UTC().AddDate(0, 0, *d).Format(time.DateOnly)
	}
	tok, err := mintToken(c.ctx, c.h, p)
	if err != nil {
		c.h.Log("minting a quote token: " + err.Error())
		return "", "the token couldn't be signed"
	}
	return tok, ""
}

// firstName is the first word of a name: a token carries no more.
func firstName(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	r := []rune(f[0])
	if len(r) > 40 {
		r = r[:40]
	}
	return string(r)
}

// serviceArea is GET /service-area?zip=: Fuse's GET /api/service_areas/:zip.
// With nothing published it answers served, as Fuse does with no ZIPs
// loaded: a wrong "no" turns a customer away.
func (c *call) serviceArea() *plugin.Response {
	zip := strings.TrimSpace(c.req.Query.Get("zip"))
	if !zip5.MatchString(zip) {
		return reply(400, map[string]string{"error": "invalid_postal_code"})
	}
	v, err := loadActive(c.ctx, c.h)
	if err != nil || v == nil {
		return replyPublic(engine.ServiceArea{PostalCode: zip, Served: true, Enforced: false, Configured: false})
	}
	a, _ := v.index.ServiceAreaFor(zip, v.zips(c.ctx, c.h))
	return replyPublic(a)
}

// catalog is GET /catalog[?zip=&service_type=]: Fuse's config catalog,
// for build-time prices ("from $149/mo") and the products of a service.
func (c *call) catalog() *plugin.Response {
	v, err := loadActive(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "rates_unreadable"})
	}
	if v == nil {
		return reply(409, map[string]string{"error": "no_rates", "message": "No rates are published yet."})
	}
	zip := strings.TrimSpace(c.req.Query.Get("zip"))
	cat := v.index.Catalog(zip, c.req.Query.Get("service_type"), v.zips(c.ctx, c.h))
	return replyPublic(map[string]any{"rates_version": v.active.Version, "service_types": cat.ServiceTypes, "product_types": cat.ProductTypes,
		"products": cat.Products, "phone": cat.Phone, "location_phone": cat.LocationPhone})
}

func strOr(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
