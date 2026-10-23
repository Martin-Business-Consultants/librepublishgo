package engine

import "strings"

// ServiceArea is GET /api/service_areas/:zip's answer
// (Api::ServiceAreasController).
type ServiceArea struct {
	PostalCode string  `json:"postal_code"`
	Served     bool    `json:"served"`
	Enforced   bool    `json:"enforced"`
	Configured bool    `json:"configured"`
	Message    *string `json:"message"`
}

// ServiceAreaFor answers whether zip (5 digits) is served; ok is false for
// a ZIP that isn't 5 digits (Fuse: 400 invalid_postal_code).
//
// Biased toward served, as Fuse is: with ZIP qualification off, or no ZIPs
// loaded, every ZIP is served. Otherwise a 5-digit match, or a ZIP+4 under
// it.
//
// Fuse quirk: a ZIP stored only as ZIP+4 is served here, but the preview's
// exact lookup misses it (no zone, no ZIP discount, the out-of-area text).
func (x *Index) ServiceAreaFor(zip string, zips ZipSource) (ServiceArea, bool) {
	zip = strings.TrimSpace(zip)
	if !zip5Format.MatchString(zip) {
		return ServiceArea{}, false
	}
	s := x.Book.Settings
	if !s.ZipQualification {
		return ServiceArea{PostalCode: zip, Served: true, Enforced: false, Configured: zips.Any()}, true
	}
	if !zips.Any() {
		return ServiceArea{PostalCode: zip, Served: true, Enforced: true, Configured: false}, true
	}
	a := ServiceArea{PostalCode: zip, Served: zips.Served(zip), Enforced: true, Configured: true}
	if !a.Served && !blank(s.OutOfAreaZipText) {
		t := s.OutOfAreaZipText
		a.Message = &t
	}
	return a, true
}
