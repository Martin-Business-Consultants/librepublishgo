package engine

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The golden cases are Fuse's own answers. testdata/austin/tools/
// seed_austin.rb seeds a sandbox copy of Fuse with a synthetic Austin rate
// book (real Austin ZIPs, invented prices built to reach every branch of the
// calculator), and record_goldens.rb asks Fuse's real Rails stack in-process
// — POST /api/v1/quoting/preview, GET /api/v1/quoting/config, GET
// /api/service_areas/:zip — for each case, with Google's Directions API
// replaced by a fixture whose every answer is in distances.json.
// rates.json is that tenant as Fuse's pricing:export wrote it.

type golden struct {
	Previews []struct {
		Variant  string          `json:"variant"`
		Request  map[string]any  `json:"request"`
		Response json.RawMessage `json:"response"`
	} `json:"previews"`
	Configs []struct {
		Request       map[string]any  `json:"request"`
		Products      json.RawMessage `json:"products"`
		ServiceTypes  json.RawMessage `json:"service_types"`
		ProductTypes  json.RawMessage `json:"product_types"`
		Phone         *string         `json:"phone"`
		LocationPhone *string         `json:"location_phone"`
	} `json:"configs"`
	ServiceAreas []struct {
		ZipQualification bool            `json:"zip_qualification"`
		Zip              string          `json:"zip"`
		Status           int             `json:"status"`
		Response         json.RawMessage `json:"response"`
	} `json:"service_areas"`
}

func loadAustin(t testing.TB) (*Book, map[string]FixtureRoute, golden) {
	t.Helper()
	raw, err := os.ReadFile("../testdata/austin/rates.json")
	if err != nil {
		t.Fatal(err)
	}
	_, book, err := ParseBundle(raw)
	if err != nil {
		t.Fatal(err)
	}
	var routes map[string]FixtureRoute
	b, err := os.ReadFile("../testdata/austin/distances.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &routes); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open("../testdata/austin/goldens.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.NewDecoder(gz).Decode(&g); err != nil {
		t.Fatal(err)
	}
	return book, routes, g
}

// replay answers Directions from the recording; a route Fuse never asked
// for fails the test.
func replay(t *testing.T, routes map[string]FixtureRoute, down bool) Directions {
	return func(k RouteKey) *Route {
		if down {
			return nil
		}
		r, ok := routes[k.String()]
		if !ok {
			t.Errorf("route %q wasn't recorded: Fuse never asked for it", k.String())
			return nil
		}
		if r.Status != "OK" {
			return nil
		}
		return &Route{LegsMeters: r.LegsMeters, LegsSeconds: r.LegsSeconds}
	}
}

// variant is the recording's setting override for a case.
func variant(b Book, name string) (Book, bool) {
	down := false
	switch name {
	case "round_trip":
		b.Settings.MileageBasedPricing, b.Settings.MileageRoundTrip = true, true
	case "flat":
		b.Settings.MileageBasedPricing, b.Settings.MileageRoundTrip = false, true
	case "one_way":
		b.Settings.MileageBasedPricing, b.Settings.MileageRoundTrip = true, false
	case "google_down", "no_key":
		b.Settings.MileageBasedPricing, b.Settings.MileageRoundTrip = true, true
		down = true
	default:
		panic("unknown variant " + name)
	}
	return b, down
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// sameJSON compares a and b as decoded JSON, and says where they differ.
func sameJSON(a, b []byte) (bool, string) {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false, err.Error()
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false, err.Error()
	}
	if reflect.DeepEqual(x, y) {
		return true, ""
	}
	return false, diff("", x, y)
}

func diff(at string, x, y any) string {
	switch xv := x.(type) {
	case map[string]any:
		yv, ok := y.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: %v vs %v", at, x, y)
		}
		keys := map[string]bool{}
		for k := range xv {
			keys[k] = true
		}
		for k := range yv {
			keys[k] = true
		}
		var ks []string
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		var out []string
		for _, k := range ks {
			if !reflect.DeepEqual(xv[k], yv[k]) {
				out = append(out, diff(at+"."+k, xv[k], yv[k]))
			}
		}
		return strings.Join(out, "; ")
	case []any:
		yv, ok := y.([]any)
		if !ok || len(xv) != len(yv) {
			a, _ := json.Marshal(x)
			b, _ := json.Marshal(y)
			return fmt.Sprintf("%s:\n   go   %s\n   fuse %s", at, a, b)
		}
		var out []string
		for i := range xv {
			if !reflect.DeepEqual(xv[i], yv[i]) {
				out = append(out, diff(fmt.Sprintf("%s[%d]", at, i), xv[i], yv[i]))
			}
		}
		return strings.Join(out, "; ")
	}
	return fmt.Sprintf("%s: go %v, fuse %v", at, x, y)
}

// Every preview Fuse answered, the Go engine answers the same, to the cent
// and the flag, in the same order.
func TestGoldenPreviews(t *testing.T) {
	t.Parallel()
	book, routes, g := loadAustin(t)
	if len(g.Previews) < 300 {
		t.Fatalf("only %d golden previews", len(g.Previews))
	}
	zips := NewMapZips(book.ZipCodes)
	failed := 0
	for i, c := range g.Previews {
		b, down := variant(*book, c.Variant)
		x, err := NewIndex(&b)
		if err != nil {
			t.Fatal(err)
		}
		p := Params{ServiceType: str(c.Request, "service_type"), ProductType: str(c.Request, "product_type"),
			ZipCode: str(c.Request, "zip_code"), DestinationZip: str(c.Request, "destination_zip_code")}
		if q, ok := c.Request["quantities"].(map[string]any); ok {
			p.Quantities = q
		}
		got, err := x.Preview(p, zips, replay(t, routes, down))
		if err != nil {
			t.Fatalf("case %d %v: %v", i, c.Request, err)
		}
		gotJSON, _ := json.Marshal(got)
		if ok, why := sameJSON(gotJSON, c.Response); !ok {
			failed++
			if failed <= 8 {
				t.Errorf("case %d (%s) %v differs from Fuse: %s", i, c.Variant, c.Request, why)
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d previews differ from Fuse's", failed, len(g.Previews))
	}
}

// The config's catalog (products, types, phone) is Fuse's.
func TestGoldenCatalog(t *testing.T) {
	t.Parallel()
	book, _, g := loadAustin(t)
	b, _ := variant(*book, "round_trip")
	x, err := NewIndex(&b)
	if err != nil {
		t.Fatal(err)
	}
	zips := NewMapZips(book.ZipCodes)
	for _, c := range g.Configs {
		got := x.Catalog(str(c.Request, "zip_code"), str(c.Request, "service_type"), zips)
		for _, part := range []struct {
			name string
			v    any
			want json.RawMessage
		}{{"products", got.Products, c.Products}, {"service_types", got.ServiceTypes, c.ServiceTypes}, {"product_types", got.ProductTypes, c.ProductTypes}} {
			j, _ := json.Marshal(part.v)
			if ok, why := sameJSON(j, part.want); !ok {
				t.Errorf("config %v %s: %s", c.Request, part.name, why)
			}
		}
		if c.Phone == nil || got.Phone != *c.Phone {
			t.Errorf("config %v phone %q, Fuse %v", c.Request, got.Phone, c.Phone)
		}
		if (c.LocationPhone == nil) != (got.LocationPhone == "") || c.LocationPhone != nil && *c.LocationPhone != got.LocationPhone {
			t.Errorf("config %v location_phone %q, Fuse %v", c.Request, got.LocationPhone, c.LocationPhone)
		}
	}
}

func TestGoldenServiceAreas(t *testing.T) {
	t.Parallel()
	book, _, g := loadAustin(t)
	zips := NewMapZips(book.ZipCodes)
	for _, c := range g.ServiceAreas {
		b := *book
		b.Settings.ZipQualification = c.ZipQualification
		x, _ := NewIndex(&b)
		got, ok := x.ServiceAreaFor(c.Zip, zips)
		if !ok {
			if c.Status != 400 {
				t.Errorf("%s: refused, Fuse answered %d", c.Zip, c.Status)
			}
			continue
		}
		j, _ := json.Marshal(got)
		if same, why := sameJSON(j, c.Response); !same {
			t.Errorf("service area %s (qualification %v): %s", c.Zip, c.ZipQualification, why)
		}
	}
}
