package engine

import (
	"fmt"
	"slices"
	"strings"
)

// CatalogProduct is one product of GET /api/v1/quoting/config
// (FormsController#product_payload), field for field.
type CatalogProduct struct {
	ID              int64            `json:"id"`
	Name            string           `json:"name"`
	Price           *int64           `json:"price,omitempty"`
	DiscountedPrice *int64           `json:"discounted_price,omitempty"`
	ImageURL        string           `json:"image_url,omitempty"`
	Dimensions      string           `json:"dimensions,omitempty"`
	IdealFor        []string         `json:"ideal_for"`
	ProductType     string           `json:"product_type,omitempty"`
	ProductTypeName string           `json:"product_type_name,omitempty"`
	ServiceTypes    []string         `json:"service_types"`
	ForSale         bool             `json:"for_sale"`
	Monthly         bool             `json:"monthly"`
	Discount        *CatalogDiscount `json:"discount,omitempty"`
}

// CatalogDiscount is FormsController#discount_payload.
type CatalogDiscount struct {
	Name          string `json:"name"`
	Label         string `json:"label"`
	DurationUnit  string `json:"duration_unit"`
	DurationValue *int64 `json:"duration_value,omitempty"`
	DurationLabel string `json:"duration_label"`
}

// TypeRef is a service or product type as the config lists them.
type TypeRef struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Catalog is what GET /api/v1/quoting/config answers that's about prices.
type Catalog struct {
	ServiceTypes  []TypeRef        `json:"service_types"`
	ProductTypes  []TypeRef        `json:"product_types"`
	Products      []CatalogProduct `json:"products"`
	Phone         string           `json:"phone,omitempty"`
	LocationPhone string           `json:"location_phone,omitempty"`
}

// Catalog answers the config's catalog for a ZIP (optional) and a service
// (optional), as FormsController#site_config builds it.
//
// Fuse quirk: the catalog's discount comes from applicable_discount(zip)
// alone — no service or product type slug — so a service-type discount
// never shows here though the preview applies it, and the product's own
// type's discount always can. The two can disagree (Q7); both are kept.
//
// Fuse quirk: with a service, each product's service_types lists only that
// service (the filter's join is what loads the association).
func (x *Index) Catalog(zipCode, serviceSlug string, zips ZipSource) Catalog {
	var zip *ZipCode
	if !blank(zipCode) {
		zip = zips.Zip(zipCode)
	}
	c := Catalog{ServiceTypes: []TypeRef{}, ProductTypes: []TypeRef{}, Products: []CatalogProduct{}}
	sts := slices.Clone(x.Book.ServiceTypes)
	slices.SortStableFunc(sts, func(a, b ServiceType) int { return strings.Compare(a.Name, b.Name) })
	for _, s := range sts {
		c.ServiceTypes = append(c.ServiceTypes, TypeRef{s.Slug, s.Name})
	}
	pts := slices.Clone(x.Book.ProductTypes)
	slices.SortStableFunc(pts, func(a, b ProductType) int { return strings.Compare(a.Name, b.Name) })
	for _, p := range pts {
		c.ProductTypes = append(c.ProductTypes, TypeRef{p.Slug, p.Name})
	}

	var products []*Product
	for i := range x.Book.Products {
		p := &x.Book.Products[i]
		if !p.Active {
			continue
		}
		if !blank(serviceSlug) && !slices.ContainsFunc(p.ServiceTypeIDs, func(id int64) bool {
			st := x.serviceTypes[id]
			return st != nil && st.Slug == serviceSlug
		}) {
			continue
		}
		products = append(products, p)
	}
	sortProducts(products)
	for _, p := range products {
		c.Products = append(c.Products, x.catalogProduct(p, zip, zipCode, zips, serviceSlug))
	}
	settings := x.Book.Settings
	c.Phone = settings.Phone
	if zip != nil {
		if l := x.locations[zip.LocationID]; l != nil && !blank(l.Phone) {
			c.Phone, c.LocationPhone = l.Phone, l.Phone
		}
	}
	return c
}

func (x *Index) catalogProduct(p *Product, zip *ZipCode, zipCode string, zips ZipSource, serviceSlug string) CatalogProduct {
	code := ""
	if zip == nil {
		code = zipCode // applicable_discount(zip_record || zip): the string, looked up again
	}
	d := x.applicableDiscount(p, zip, code, zips, "", "")
	out := CatalogProduct{ID: p.ID, Name: p.Name, Price: p.Price, ForSale: p.ForSale, Monthly: p.Monthly,
		IdealFor: []string{}, ServiceTypes: []string{}}
	out.DiscountedPrice = p.Price
	if d != nil && p.Price != nil {
		v := discountedAmount(d, *p.Price)
		out.DiscountedPrice = &v
	}
	if len(p.Images) > 0 {
		out.ImageURL = p.Images[0].URL
	}
	if p.Length != nil && p.Width != nil && p.Height != nil {
		out.Dimensions = fmt.Sprintf("%d' × %d' × %d'", *p.Length, *p.Width, *p.Height)
	}
	for _, s := range p.IdealFor {
		if !blank(s) {
			out.IdealFor = append(out.IdealFor, s)
		}
	}
	if pt := x.productTypes[p.ProductTypeID]; pt != nil {
		out.ProductType, out.ProductTypeName = pt.Slug, pt.Name
	}
	ids := slices.Clone(p.ServiceTypeIDs)
	slices.Sort(ids)
	for _, id := range ids {
		st := x.serviceTypes[id]
		if st == nil || (!blank(serviceSlug) && st.Slug != serviceSlug) {
			continue
		}
		out.ServiceTypes = append(out.ServiceTypes, st.Slug)
	}
	if d != nil {
		out.Discount = &CatalogDiscount{Name: d.Name, Label: discountLabel(d), DurationUnit: d.DurationUnit,
			DurationValue: d.DurationValue, DurationLabel: durationLabel(d)}
	}
	return out
}
