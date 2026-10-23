package engine

import (
	"fmt"
	"slices"
	"strconv"
)

// Params are what a preview is asked for: POST /api/v1/quoting/preview's
// fields that change a price.
type Params struct {
	ServiceType    string `json:"service_type"`
	ProductType    string `json:"product_type"`
	ZipCode        string `json:"zip_code"`
	DestinationZip string `json:"destination_zip_code"`
	// Quantities are product id → quantity, as posted: numbers or strings,
	// read with Ruby's #to_i. Nil (or all zero) prices one of the first
	// product.
	Quantities map[string]any `json:"quantities"`
}

// Preview is POST /api/v1/quoting/preview's data, field for field
// (QuotesController#serialize_calculation).
type Preview struct {
	Products         []PreviewProduct `json:"products"`
	Fees             []PreviewFee     `json:"fees"`
	Subtotal         int64            `json:"subtotal"`
	Total            int64            `json:"total"`
	TotalDiscount    int64            `json:"total_discount"`
	FeesTotal        int64            `json:"fees_total"`
	TotalQuantity    int64            `json:"total_quantity"`
	OutOfAreaZipText *string          `json:"out_of_area_zip_text,omitempty"`
}

// PreviewProduct is one product line of a preview.
type PreviewProduct struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Quantity        int64  `json:"quantity"`
	Price           int64  `json:"price"`
	Subtotal        int64  `json:"subtotal"`
	HasDiscount     bool   `json:"has_discount"`
	OriginalPrice   int64  `json:"original_price"`
	DiscountedPrice int64  `json:"discounted_price"`
	Weekly          bool   `json:"weekly"`
	Monthly         bool   `json:"monthly"`

	// Not in Fuse's answer; the plugin's summary uses them.
	Discount              *Discount `json:"-"`
	DiscountDurationLabel string    `json:"-"`
	ForSale               bool      `json:"-"`
}

// PreviewFee is one fee line of a preview (serialize_fee_struct).
type PreviewFee struct {
	Name              string `json:"name"`
	Amount            int64  `json:"amount"`
	HasDiscount       bool   `json:"has_discount"`
	OriginalAmount    int64  `json:"original_amount"`
	PerQuantity       bool   `json:"per_quantity"`
	ExcludedFromTotal bool   `json:"excluded_from_total"`
	ExcludedShown     bool   `json:"excluded_shown"`
	StartingAt        bool   `json:"starting_at"`
	Recurring         bool   `json:"recurring"`

	// Not in Fuse's answer: the line's "2 Hour Route" subtitle, and the fee
	// it came from (0 for a global mileage leg).
	RateDescription string `json:"-"`
	FeeID           int64  `json:"-"`
}

// Preview prices p as POST /api/v1/quoting/preview does (QuotesController
// #preview): the service's products, the default quantity, Quoteable
// .calculate_quote, serialize_calculation.
//
// Fuse quirk: Fuse fails a request whose quantity has no #to_i (true, an
// object), or whose product has no price; so does this, with an error.
func (x *Index) Preview(p Params, zips ZipSource, dirs Directions) (*Preview, error) {
	products := x.LoadProducts(p.ProductType, p.ServiceType)
	quantities := x.defaultedQuantities(products, p.Quantities)
	out := &Preview{Products: []PreviewProduct{}, Fees: []PreviewFee{}}
	if !blank(p.ZipCode) && zips.Zip(p.ZipCode) == nil && !blank(x.Book.Settings.OutOfAreaZipText) {
		t := x.Book.Settings.OutOfAreaZipText
		out.OutOfAreaZipText = &t
	}
	if len(products) == 0 {
		return out, nil
	}
	// build_product_details
	var subtotal, totalDiscount, totalQuantity int64
	for _, prod := range products {
		qty, ok := rubyToI(quantities[strconv.FormatInt(prod.ID, 10)])
		if !ok {
			return nil, fmt.Errorf("quantity for product %d isn't a number", prod.ID)
		}
		if qty <= 0 {
			continue
		}
		if prod.Price == nil {
			return nil, fmt.Errorf("product %d (%s) has no price", prod.ID, prod.Name)
		}
		original := *prod.Price
		d := x.applicableDiscount(prod, nil, p.ZipCode, zips, p.ServiceType, p.ProductType)
		discounted := discountedAmount(d, original)
		pp := PreviewProduct{
			ID: prod.ID, Name: prod.Name, Quantity: qty, Price: original, Subtotal: discounted * qty,
			HasDiscount: d != nil, OriginalPrice: original, DiscountedPrice: discounted,
			Monthly: prod.Monthly, Discount: d, ForSale: prod.ForSale,
		}
		if d != nil {
			pp.DiscountDurationLabel = durationLabel(d)
		}
		out.Products = append(out.Products, pp)
		subtotal += pp.Subtotal
		totalDiscount += (original - discounted) * qty
		totalQuantity += qty
	}

	lines := x.newQuote(p, zips, dirs).calculate()
	var feesTotal int64
	for _, l := range lines {
		if l.excludedFromTotalFlag() {
			continue
		}
		if l.isStruct() {
			feesTotal += l.amount
		} else {
			feesTotal += l.amount * totalQuantity
		}
		// Fee savings fold into total_discount, so "Promo applied" covers both.
		if l.hasDiscount {
			saving := l.originalAmount - l.amount
			if !l.isStruct() {
				saving *= totalQuantity
			}
			totalDiscount += saving
		}
	}
	for _, l := range lines {
		out.Fees = append(out.Fees, serializeFee(l))
	}
	out.Subtotal = subtotal
	out.TotalDiscount = totalDiscount
	out.TotalQuantity = totalQuantity
	out.FeesTotal = feesTotal
	out.Total = subtotal + feesTotal
	return out, nil
}

// serializeFee is serialize_fee_struct.
func serializeFee(l *line) PreviewFee {
	f := PreviewFee{Name: l.name, Amount: l.amount, HasDiscount: l.hasDiscount, PerQuantity: !l.isStruct(),
		ExcludedFromTotal: l.excludedFromTotalFlag(), RateDescription: l.rateDescription}
	f.OriginalAmount = l.amount
	if l.hasDiscount {
		f.OriginalAmount = l.originalAmount
	}
	if l.record != nil {
		f.ExcludedShown = l.record.TotalMode == modeExcludedShown
		f.StartingAt = l.record.displayAsStartingAt()
		f.Recurring = l.record.Recurring
		f.FeeID = l.record.ID
	} else {
		// Fee.excluded_but_shown?: an OpenStruct answers only when built with it.
		f.ExcludedShown = l.hasExcludedShown && l.excludedShown
		f.StartingAt = l.startingAt
		// Fuse quirk: a calculator line never says recurring, even one made
		// from a recurring fee (OpenStruct has no #recurring?).
		f.Recurring = false
		if l.source != nil {
			f.FeeID = l.source.ID
		}
	}
	return f
}

// LoadProducts is QuotesController#load_products: the active products of
// the service (by slug), of the product type when one is named, ordered by
// position (NULL first, ties by id).
func (x *Index) LoadProducts(productTypeSlug, serviceSlug string) []*Product {
	var out []*Product
	for i := range x.Book.Products {
		p := &x.Book.Products[i]
		if !p.Active || !slices.ContainsFunc(p.ServiceTypeIDs, func(id int64) bool {
			st := x.serviceTypes[id]
			return st != nil && st.Slug == serviceSlug
		}) {
			continue
		}
		if !blank(productTypeSlug) {
			pt := x.productTypes[p.ProductTypeID]
			if pt == nil || pt.Slug != productTypeSlug {
				continue
			}
		}
		out = append(out, p)
	}
	sortProducts(out)
	return out
}

func sortProducts(ps []*Product) {
	slices.SortStableFunc(ps, func(a, b *Product) int {
		if c := cmpNullsFirst(a.Position, b.Position); c != 0 {
			return c
		}
		return cmpInt(a.ID, b.ID)
	})
}

// defaultedQuantities is QuotesController#defaulted_quantities: with no
// quantities, or all of them 0, the first product is 1.
//
// Fuse quirk: quantities that name only products the service doesn't have
// aren't "all 0", so nothing is defaulted and the preview prices nothing —
// no products, and every per-quantity fee times 0.
func (x *Index) defaultedQuantities(products []*Product, q map[string]any) map[string]any {
	out := map[string]any{}
	allZero := true
	for k, v := range q {
		out[k] = v
		if n, ok := rubyToI(v); !ok || n != 0 {
			allZero = false
		}
	}
	if len(products) > 0 && (len(q) == 0 || allZero) {
		out[strconv.FormatInt(products[0].ID, 10)] = float64(1)
	}
	return out
}
