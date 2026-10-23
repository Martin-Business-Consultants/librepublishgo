package fuse

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/plugins/fuse/engine"
)

func item(label string, value any) map[string]any {
	return map[string]any{"label": label, "value": fmt.Sprint(value)}
}

func (c *call) overview() *plugin.Response {
	var a Active
	published := getJSON(c.ctx, c.h, keyActive, &a)
	hasDraft, meta, _ := loadDraftMeta(c.ctx, c.h)
	cfg := loadConfig(c.ctx, c.h)
	month := now().UTC().Format("200601")
	calls, _, _ := c.h.KVGet(c.ctx, "stats:google:"+month)
	rec := map[string]any{"has_draft": hasDraft, "published": published}

	pub := []map[string]any{}
	if published {
		pub = append(pub, item("Version", a.Version), map[string]any{"label": "Published", "value": a.PublishedAt, "time": true}, item("By", a.By))
		if a.Source != "" {
			pub = append(pub, item("From", a.Source))
		}
		for _, k := range []string{"products", "fees", "zips", "zones", "discounts"} {
			pub = append(pub, item(strings.ToUpper(k[:1])+k[1:], a.Counts[k]))
		}
	} else {
		pub = append(pub, map[string]any{"label": "Rates", "value": "Nothing is published: /quote answers 409 until something is.", "tone": "danger"})
	}
	draft := []map[string]any{}
	if hasDraft {
		draft = append(draft, map[string]any{"label": "Changed", "value": meta.UpdatedAt, "time": true}, item("By", meta.By), item("Started from", strOr(meta.BaseVersion, "nothing published")))
		if meta.Source != "" {
			draft = append(draft, item("Imported", meta.Source))
		}
		if b, _, err := loadDraft(c.ctx, c.h); err == nil {
			if errs := engine.Validate(b); len(errs) > 0 {
				draft = append(draft, map[string]any{"label": "Before it can be published", "value": (&invalidBookError{errs}).Error(), "tone": "danger", "multiline": true})
			}
			cts := counts(b)
			draft = append(draft, item("Counts", fmt.Sprintf("%d products, %d fees, %d ZIPs, %d zones, %d discounts", cts["products"], cts["fees"], cts["zips"], cts["zones"], cts["discounts"])))
		}
	} else {
		draft = append(draft, item("Draft", "No changes since the published version."))
	}
	dist := []map[string]any{item("Mode", cfg.mode(c.ctx, c.h)), item("Google calls this month", strOr(calls, "0")+" of "+strconv.FormatInt(cfg.cap(), 10))}
	if cfg.LastCheck != "" {
		dist = append(dist, item("Key last checked", cfg.LastCheck))
	}
	keys := loadTokenKeys(c.ctx, c.h)
	dist = append(dist, item("Quote-token key", strOr(keys.Active, "made on the first quote")))
	rec["published_items"], rec["draft_items"], rec["distance_items"] = pub, draft, dist
	return reply(200, map[string]any{"overview": rec})
}

func (c *call) publish() *plugin.Response {
	if has, _, _ := loadDraftMeta(c.ctx, c.h); !has {
		return reply(409, map[string]string{"error": "no_draft", "message": "There's no draft to publish: edit or import first."})
	}
	a, err := publish(c.ctx, c.h, c.actor())
	if bad, ok := errors.AsType[*invalidBookError](err); ok {
		return reply(422, map[string]any{"error": "invalid", "message": bad.Error(), "errors": bad.errs})
	}
	if err != nil {
		return reply(500, map[string]string{"error": "not_published", "message": err.Error()})
	}
	return reply(200, map[string]any{"ok": true, "message": "Published " + a.Version + ". Quotes price from it now.", "active": a})
}

func (c *call) discard() *plugin.Response {
	discardDraft(c.ctx, c.h)
	return reply(200, map[string]any{"ok": true, "message": "The draft is gone; the published rates are as they were."})
}

func (c *call) rollback() *plugin.Response {
	ver := c.req.Query.Get("version")
	a, err := rollback(c.ctx, c.h, ver, c.actor())
	if err != nil {
		return reply(422, map[string]string{"error": "not_rolled_back", "message": err.Error()})
	}
	return reply(200, map[string]any{"ok": true, "message": "Quotes price from " + a.Version + " again.", "active": a})
}

func (c *call) versions() *plugin.Response {
	var list []Active
	getJSON(c.ctx, c.h, keyVersions, &list)
	var a Active
	getJSON(c.ctx, c.h, keyActive, &a)
	rows := []map[string]any{}
	for _, v := range list {
		status := "kept"
		if v.Version == a.Version {
			status = "active"
		}
		rows = append(rows, map[string]any{"version": v.Version, "published_at": v.PublishedAt, "by": v.By, "digest": v.Digest, "status": status, "source": v.Source})
	}
	return reply(200, map[string]any{"versions": rows})
}

func (c *call) rotateToken() *plugin.Response {
	kid, _, err := rotateTokenKey(c.ctx, c.h)
	if err != nil {
		return reply(500, map[string]string{"error": "not_rotated", "message": err.Error()})
	}
	return reply(200, map[string]any{"ok": true, "message": "New quote tokens are signed with " + kid + "; links signed before still verify."})
}

// Settings: the rate book's (draft, published with it) and the plugin's
// own (distances; in force at once).

func mask(secret string) string {
	if secret == "" {
		return ""
	}
	if len(secret) > 8 {
		return "***" + secret[len(secret)-4:]
	}
	return "***"
}

func (c *call) settings() *plugin.Response {
	b, _, err := loadDraft(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "draft_unreadable", "message": err.Error()})
	}
	s := b.Settings
	cfg := loadConfig(c.ctx, c.h)
	rec := map[string]any{"zip": s.Zip, "mileage_based_pricing": s.MileageBasedPricing, "mileage_round_trip": s.MileageRoundTrip,
		"zip_qualification": s.ZipQualification, "out_of_area_zip_text": s.OutOfAreaZipText, "quote_validity_days": s.QuoteValidityDays,
		"total_price_label": s.TotalPriceLabel, "name": s.Name, "phone": s.Phone,
		"distance_mode": strOr(cfg.DistanceMode, "auto"), "google_key": mask(c.h.Reveal(cfg.GoogleKey)),
		"known_zips_only": cfg.KnownZipsOnly, "monthly_cap": cfg.cap()}
	return reply(200, map[string]any{"settings": rec})
}

func (c *call) saveSettings() *plugin.Response {
	var body struct {
		Settings map[string]any `json:"settings"`
	}
	if json.Unmarshal(c.req.Body, &body) != nil || body.Settings == nil {
		return reply(400, map[string]string{"error": "bad_request", "message": "Send {\"settings\": {…}}."})
	}
	f, e := form(body.Settings), errs{}
	b, _, err := loadDraft(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "draft_unreadable", "message": err.Error()})
	}
	s := b.Settings
	s.Zip, s.Name, s.Phone = f.str("zip"), f.str("name"), f.str("phone")
	if s.Zip != "" && !engine.ZipFormat.MatchString(s.Zip) {
		e.add("zip", "must be a ZIP code (12345)")
	}
	s.MileageBasedPricing, s.MileageRoundTrip, s.ZipQualification = f.boolean("mileage_based_pricing"), f.boolean("mileage_round_trip"), f.boolean("zip_qualification")
	s.OutOfAreaZipText, s.TotalPriceLabel = f.str("out_of_area_zip_text"), f.str("total_price_label")
	if d := e.opt(f, "quote_validity_days"); d != nil {
		v := int(*d)
		s.QuoteValidityDays = &v
	} else {
		s.QuoteValidityDays = nil
	}
	cfg := loadConfig(c.ctx, c.h)
	switch m := f.str("distance_mode"); m {
	case "auto", "":
		cfg.DistanceMode = ""
	case modeGoogle, modeFixture, modeOff:
		cfg.DistanceMode = m
	default:
		e.add("distance_mode", "must be auto, google, fixture or off")
	}
	cfg.KnownZipsOnly = f.boolean("known_zips_only")
	if capN := e.opt(f, "monthly_cap"); capN != nil {
		cfg.MonthlyCap = *capN
	}
	keyChanged := false
	if k := f.str("google_key"); !strings.HasPrefix(k, "***") {
		if !canSecrets(c) && k != c.h.Reveal(cfg.GoogleKey) {
			e.add("google_key", "your role can't set the Google key (fuse:secrets)")
		} else if k != c.h.Reveal(cfg.GoogleKey) {
			cfg.GoogleKey, keyChanged = c.h.Seal(k), true
		}
	}
	if len(e) > 0 {
		return invalid(e)
	}
	if before, after := mustJSON(b.Settings), mustJSON(s); before != after {
		b.Settings = s
		if err := saveDraft(c.ctx, c.h, b, c.actor(), ""); err != nil {
			return reply(500, map[string]string{"error": "not_saved", "message": err.Error()})
		}
	}
	if keyChanged {
		cfg.LastCheck = ""
	}
	if err := setJSON(c.ctx, c.h, keyConfig, cfg); err != nil {
		return reply(500, map[string]string{"error": "not_saved", "message": err.Error()})
	}
	return c.settings()
}

// canSecrets says whether the caller may set secrets (fuse:secrets).
func canSecrets(c *call) bool {
	return c.req.Request != nil && c.req.Actor != nil && slices.Contains(c.req.Actor.Capabilities, "fuse:secrets")
}

func (c *call) checkKey() *plugin.Response {
	var body struct {
		Settings map[string]any `json:"settings"`
	}
	_ = json.Unmarshal(c.req.Body, &body)
	cfg := loadConfig(c.ctx, c.h)
	key := c.h.Reveal(cfg.GoogleKey)
	if k := form(body.Settings).str("google_key"); k != "" && !strings.HasPrefix(k, "***") {
		key = k // the one typed in, not yet saved
	}
	zip := form(body.Settings).str("zip")
	res := checkKey(c.ctx, c.h, key, zip)
	if key == c.h.Reveal(cfg.GoogleKey) {
		cfg.LastCheck = res + " " + now().UTC().Format(time.RFC3339)
		_ = setJSON(c.ctx, c.h, keyConfig, cfg)
	}
	return reply(200, map[string]any{"ok": res == "OK", "message": "Google key: " + res})
}

func (c *call) mileage() *plugin.Response {
	b, _, err := loadDraft(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "draft_unreadable", "message": err.Error()})
	}
	return reply(200, map[string]any{"mileage": map[string]any{"tiers": tiersOut(b.MileageTiers)}})
}

func (c *call) saveMileage() *plugin.Response {
	var body struct {
		Mileage map[string]any `json:"mileage"`
	}
	if json.Unmarshal(c.req.Body, &body) != nil || body.Mileage == nil {
		return reply(400, map[string]string{"error": "bad_request", "message": "Send {\"mileage\": {\"tiers\": […]}}."})
	}
	b, _, err := loadDraft(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "draft_unreadable", "message": err.Error()})
	}
	e := errs{}
	b.MileageTiers = tiersIn(e, "tiers", body.Mileage["tiers"], b)
	for k, v := range engine.Validate(b) {
		if strings.HasPrefix(k, "mileage_tiers") {
			e[strings.Replace(k, "mileage_tiers", "tiers", 1)] = v
		}
		if strings.HasSuffix(k, ".tiers") {
			e["tiers"] = append(e["tiers"], "a mileage-based fee would have no tiers left")
		}
	}
	if len(e) > 0 {
		return invalid(e)
	}
	if err := saveDraft(c.ctx, c.h, b, c.actor(), ""); err != nil {
		return reply(500, map[string]string{"error": "not_saved", "message": err.Error()})
	}
	return c.mileage()
}

// ZIPs: a page at a time, and a bulk form (CSV) in place of Fuse's importer.

const zipsPage = 200

func (c *call) zips() *plugin.Response {
	b, _, err := loadDraft(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "draft_unreadable", "message": err.Error()})
	}
	zone := c.req.Query.Get("zone")
	after := c.req.Query.Get("after")
	q := strings.TrimSpace(c.req.Query.Get("q"))
	zs := slices.Clone(b.ZipCodes)
	slices.SortFunc(zs, func(a, c engine.ZipCode) int { return strings.Compare(a.PostalCode, c.PostalCode) })
	rows := []map[string]any{}
	next, last := "", ""
	for _, z := range zs {
		if z.PostalCode <= after || q != "" && !strings.HasPrefix(z.PostalCode, q) || zone != "" && zoneName(b, z.ZoneID) != zone {
			continue
		}
		if len(rows) == zipsPage {
			next = last
			break
		}
		last = z.PostalCode
		rows = append(rows, map[string]any{"id": z.ID, "postal_code": z.PostalCode, "zone": zoneName(b, z.ZoneID), "location": locationName(b, z.LocationID),
			"fee": feeLabel(b, z.FeeID), "discount": discountName(b, z.DiscountID)})
	}
	zones := []map[string]string{{"value": "", "label": "All zones"}}
	for _, z := range b.Zones {
		zones = append(zones, map[string]string{"value": z.Name, "label": z.Name})
	}
	return reply(200, map[string]any{"zips": rows, "next": next, "zones": zones})
}

func (c *call) deleteZip() *plugin.Response {
	b, _, err := loadDraft(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "draft_unreadable", "message": err.Error()})
	}
	n := len(b.ZipCodes)
	b.ZipCodes = without(b.ZipCodes, func(z engine.ZipCode) int64 { return z.ID }, c.id())
	if len(b.ZipCodes) == n {
		return notFound("No such ZIP in the draft.")
	}
	if err := saveDraft(c.ctx, c.h, b, c.actor(), ""); err != nil {
		return reply(500, map[string]string{"error": "not_saved", "message": err.Error()})
	}
	return reply(200, map[string]any{"ok": true, "message": "Removed from the draft."})
}

func (c *call) zipsBulkForm() *plugin.Response {
	return reply(200, map[string]any{"bulk": map[string]any{"mode": "merge", "csv": ""}})
}

// saveZipsBulk takes CSV lines "zip,zone,location[,fee id][,discount]":
// merge adds and updates, replace_all makes the table exactly these.
func (c *call) saveZipsBulk() *plugin.Response { //nolint:gocyclo // a line read, then each column checked in turn
	var body struct {
		Bulk map[string]any `json:"bulk"`
	}
	if json.Unmarshal(c.req.Body, &body) != nil || body.Bulk == nil {
		return reply(400, map[string]string{"error": "bad_request", "message": "Send {\"bulk\": {\"csv\": …, \"mode\": …}}."})
	}
	f := form(body.Bulk)
	b, _, err := loadDraft(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "draft_unreadable", "message": err.Error()})
	}
	mode := strOr(f.str("mode"), "merge")
	if mode != "merge" && mode != "replace_all" {
		return invalid(map[string][]string{"mode": {"must be merge or replace_all"}})
	}
	var bad []string
	var parsed []engine.ZipCode
	sc := bufio.NewScanner(strings.NewReader(f.str("csv")))
	sc.Buffer(make([]byte, 0, 64<<10), 10<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || line == 1 && strings.HasPrefix(strings.ToLower(raw), "zip") {
			continue
		}
		cols := strings.Split(raw, ",")
		for i := range cols {
			cols[i] = strings.TrimSpace(cols[i])
		}
		for len(cols) < 5 {
			cols = append(cols, "")
		}
		e := errs{}
		z := engine.ZipCode{PostalCode: cols[0]}
		if !engine.ZipFormat.MatchString(z.PostalCode) {
			e.add("zip", "isn't a ZIP")
		}
		if cols[1] != "" {
			if ids := idsOf(e, "zone", []string{cols[1]}, b.Zones, zID, zName, "zone"); len(ids) == 1 {
				z.ZoneID = &ids[0]
			}
		}
		if ids := idsOf(e, "location", []string{cols[2]}, b.Locations, func(l engine.Location) int64 { return l.ID }, func(l engine.Location) string { return l.Name }, "location"); len(ids) == 1 {
			z.LocationID = ids[0]
		}
		z.FeeID = e.feeID(b, form{"fee": cols[3]}, "fee")
		z.DiscountID = e.discountID(b, form{"discount": cols[4]}, "discount")
		if len(e) > 0 {
			var why []string
			for k, v := range e {
				why = append(why, k+" "+strings.Join(v, ", "))
			}
			slices.Sort(why)
			bad = append(bad, fmt.Sprintf("line %d (%s): %s", line, raw, strings.Join(why, "; ")))
			continue
		}
		parsed = append(parsed, z)
	}
	if len(bad) > 0 {
		if len(bad) > 30 {
			bad = append(bad[:30], fmt.Sprintf("and %d more", len(bad)-30))
		}
		return invalid(map[string][]string{"csv": bad})
	}
	if mode == "replace_all" {
		old := map[string]int64{}
		for _, z := range b.ZipCodes {
			old[z.PostalCode] = z.ID
		}
		b.ZipCodes = nil
		next := int64(1)
		for _, id := range old {
			next = max(next, id+1)
		}
		for _, z := range parsed {
			if id, ok := old[z.PostalCode]; ok {
				z.ID = id
			} else {
				z.ID, next = next, next+1
			}
			b.ZipCodes = upsert(b.ZipCodes, func(x engine.ZipCode) int64 { return x.ID }, z)
		}
	} else {
		for _, z := range parsed {
			i := slices.IndexFunc(b.ZipCodes, func(x engine.ZipCode) bool { return x.PostalCode == z.PostalCode })
			if i >= 0 {
				z.ID = b.ZipCodes[i].ID
				b.ZipCodes[i] = z
			} else {
				z.ID = nextID(b.ZipCodes, func(x engine.ZipCode) int64 { return x.ID })
				b.ZipCodes = append(b.ZipCodes, z)
			}
		}
	}
	if ve := engine.Validate(b); len(ve) > 0 {
		var why []string
		for k, v := range ve {
			if strings.HasPrefix(k, "zip_codes") {
				why = append(why, strings.Join(v, ", "))
			}
		}
		if len(why) > 0 {
			return invalid(map[string][]string{"csv": why})
		}
	}
	if err := saveDraft(c.ctx, c.h, b, c.actor(), ""); err != nil {
		return reply(500, map[string]string{"error": "not_saved", "message": err.Error()})
	}
	return reply(200, map[string]any{"bulk": map[string]any{"mode": mode, "csv": ""}, "message": fmt.Sprintf("%d ZIPs in the draft.", len(b.ZipCodes)),
		"redirect": map[string]any{"screen": "zips", "params": map[string]string{}}})
}

// Import: a Fuse export into the draft.

func (c *call) importForm() *plugin.Response {
	return reply(200, map[string]any{"import": map[string]any{"bundle": "", "images": true, "distance_fixture": ""}})
}

type importBody struct {
	Import struct {
		Bundle          string `json:"bundle"`
		Images          *bool  `json:"images"`
		DistanceFixture string `json:"distance_fixture"`
	} `json:"import"`
}

func (c *call) readImport() (importBody, *engine.Bundle, *engine.Book, *plugin.Response) {
	var body importBody
	if err := json.Unmarshal(c.req.Body, &body); err != nil {
		return body, nil, nil, reply(400, map[string]string{"error": "bad_request", "message": "Send {\"import\": {\"bundle\": \"<the export, as text>\"}}."})
	}
	bun, book, err := engine.ParseBundle([]byte(strings.TrimSpace(body.Import.Bundle)))
	if err != nil {
		return body, nil, nil, invalid(map[string][]string{"bundle": {err.Error()}})
	}
	if errs := engine.Validate(book); len(errs) > 0 {
		return body, nil, nil, invalid(map[string][]string{"bundle": {(&invalidBookError{errs}).Error()}})
	}
	return body, bun, book, nil
}

// checkBundle is the dry run: what importing would change in the draft.
func (c *call) checkBundle() *plugin.Response {
	_, bun, book, bad := c.readImport()
	if bad != nil {
		return bad
	}
	cur, _, err := loadDraft(c.ctx, c.h)
	if err != nil {
		return reply(503, map[string]string{"error": "draft_unreadable", "message": err.Error()})
	}
	items := []map[string]any{item("Tenant", bun.Tenant), item("Exported", bun.ExportedAt), item("Fuse schema", bun.FuseSchemaVersion)}
	items = append(items, diffBooks(cur, book)...)
	return reply(200, map[string]any{"result": map[string]any{"title": "Importing would change the draft like this", "items": items}})
}

func (c *call) importBundle() *plugin.Response {
	body, bun, book, bad := c.readImport()
	if bad != nil {
		return bad
	}
	images := body.Import.Images == nil || *body.Import.Images
	var notes []string
	for i := range book.Products {
		var kept []engine.Image
		for _, im := range book.Products[i].Images {
			if im.Base64 == "" {
				if im.URL != "" {
					kept = append(kept, engine.Image{URL: im.URL, AssetID: im.AssetID})
				}
				continue
			}
			if !images {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(im.Base64)
			if err != nil {
				notes = append(notes, "an image of "+book.Products[i].Name+" doesn't decode")
				continue
			}
			asset, err := c.h.PutMedia(c.ctx, plugin.Media{Filename: im.Filename, ContentType: im.ContentType, Data: data, Alt: book.Products[i].Name})
			if err != nil {
				notes = append(notes, "an image of "+book.Products[i].Name+" wasn't kept: "+err.Error())
				continue
			}
			url, _ := asset["url"].(string)
			kept = append(kept, engine.Image{URL: url, AssetID: asset["id"]})
		}
		book.Products[i].Images = kept
	}
	source := fmt.Sprintf("fuse:%s exported %s", bun.Tenant, bun.ExportedAt)
	if err := saveDraft(c.ctx, c.h, book, c.actor(), source); err != nil {
		return reply(500, map[string]string{"error": "not_saved", "message": err.Error()})
	}
	fixture := bun.DistanceFixture
	if s := strings.TrimSpace(body.Import.DistanceFixture); s != "" {
		if err := json.Unmarshal([]byte(s), &fixture); err != nil {
			return invalid(map[string][]string{"distance_fixture": {"isn't routes JSON: " + err.Error()}})
		}
	}
	if len(fixture) > 0 {
		if err := putBlob(c.ctx, c.h, keyFixture, fixture); err != nil {
			notes = append(notes, "the distance fixture wasn't kept: "+err.Error())
		} else {
			notes = append(notes, fmt.Sprintf("%d fixture routes kept", len(fixture)))
		}
	}
	msg := fmt.Sprintf("Imported %s into the draft: %d products, %d fees, %d ZIPs. Review it, test prices, then publish.", bun.Tenant, len(book.Products), len(book.Fees), len(book.ZipCodes))
	if len(notes) > 0 {
		msg += " (" + strings.Join(notes, "; ") + ")"
	}
	return reply(200, map[string]any{"import": map[string]any{"bundle": "", "images": images}, "message": msg,
		"redirect": map[string]any{"screen": "overview", "params": map[string]string{}}})
}

// diffBooks counts what's added, changed and removed, table by table.
func diffBooks(from, to *engine.Book) []map[string]any {
	type rec struct {
		id  int64
		raw string
	}
	table := func(name string, a, b []rec) map[string]any {
		old := map[int64]string{}
		for _, r := range a {
			old[r.id] = r.raw
		}
		var added, changed int
		seen := map[int64]bool{}
		for _, r := range b {
			seen[r.id] = true
			o, ok := old[r.id]
			switch {
			case !ok:
				added++
			case o != r.raw:
				changed++
			}
		}
		removed := 0
		for id := range old {
			if !seen[id] {
				removed++
			}
		}
		return item(name, fmt.Sprintf("%d in the export: %d added, %d changed, %d removed", len(b), added, changed, removed))
	}
	recs := func(v any) []rec {
		raw, _ := json.Marshal(v)
		var items []json.RawMessage
		_ = json.Unmarshal(raw, &items)
		out := make([]rec, 0, len(items))
		for _, it := range items {
			var head struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal(it, &head)
			out = append(out, rec{head.ID, string(it)})
		}
		return out
	}
	out := []map[string]any{
		table("Products", recs(from.Products), recs(to.Products)),
		table("Fees", recs(from.Fees), recs(to.Fees)),
		table("Discounts", recs(from.Discounts), recs(to.Discounts)),
		table("Zones", recs(from.Zones), recs(to.Zones)),
		table("Locations", recs(from.Locations), recs(to.Locations)),
		table("ZIPs", recs(from.ZipCodes), recs(to.ZipCodes)),
		table("Mileage tiers", recs(from.MileageTiers), recs(to.MileageTiers)),
		table("Service types", recs(from.ServiceTypes), recs(to.ServiceTypes)),
		table("Product types", recs(from.ProductTypes), recs(to.ProductTypes)),
	}
	fs, _ := json.Marshal(from.Settings)
	ts, _ := json.Marshal(to.Settings)
	if string(fs) != string(ts) {
		out = append(out, map[string]any{"label": "Settings", "value": string(ts), "multiline": true})
	}
	return out
}

// The price tester: one service priced on the draft and on the published
// rates, side by side, so an edit can be checked before it goes live.

const keyTester = "cfg:tester"

func (c *call) testForm() *plugin.Response {
	rec := map[string]any{"zip": "", "dest": "", "product_type": "portable-storage", "service": "keep-it", "qty": 1}
	getJSON(c.ctx, c.h, keyTester, &rec)
	return reply(200, map[string]any{"test": rec})
}

func (c *call) saveTest() *plugin.Response {
	var body struct {
		Test map[string]any `json:"test"`
	}
	if json.Unmarshal(c.req.Body, &body) != nil || body.Test == nil {
		return reply(400, map[string]string{"error": "bad_request"})
	}
	delete(body.Test, "token")
	_ = setJSON(c.ctx, c.h, keyTester, body.Test)
	return reply(200, map[string]any{"test": body.Test})
}

func (c *call) testPrice() *plugin.Response {
	var body struct {
		Test map[string]any `json:"test"`
	}
	if json.Unmarshal(c.req.Body, &body) != nil || body.Test == nil {
		return reply(400, map[string]string{"error": "bad_request", "message": "Send {\"test\": {…}}."})
	}
	f, e := form(body.Test), errs{}
	zip, dest := f.str("zip"), f.str("dest")
	if !zip5.MatchString(zip) {
		e.add("zip", "must be a 5-digit ZIP")
	}
	if dest != "" && !zip5.MatchString(dest) {
		e.add("dest", "must be a 5-digit ZIP, or blank")
	}
	product, qty := e.opt(f, "product"), e.opt(f, "qty")
	if len(e) > 0 {
		return invalid(e)
	}
	p := engine.Params{ServiceType: f.str("service"), ProductType: f.str("product_type"), ZipCode: zip, DestinationZip: dest}
	if product != nil {
		n := int64(1)
		if qty != nil && *qty > 0 {
			n = *qty
		}
		p.Quantities = map[string]any{strconv.FormatInt(*product, 10): float64(n)}
	} else if qty != nil && *qty > 1 {
		p.Quantities = nil // the first product, quantity 1, as Fuse defaults
	}
	cfg := loadConfig(c.ctx, c.h)
	items := []map[string]any{}
	price := func(label string, x *engine.Index, zips engine.ZipSource) {
		keys := engine.PlanRoutes(x.Book.Settings, zip, dest)
		needs := x.Book.Settings.MileageBasedPricing || slices.ContainsFunc(x.Book.Fees, func(f engine.Fee) bool { return f.MileageBasedPricing })
		if !needs {
			keys = nil
		}
		routes, rep := fetchRoutes(c.ctx, c.h, cfg, keys, distanceBudget)
		pv, err := x.Preview(p, zips, func(k engine.RouteKey) *engine.Route { return routes[k.String()] })
		if err != nil {
			items = append(items, map[string]any{"label": label, "value": "Can't price it: " + err.Error(), "tone": "danger"})
			return
		}
		items = append(items, map[string]any{"label": label, "value": describe(pv, rep), "multiline": true})
	}
	if draft, _, err := loadDraft(c.ctx, c.h); err == nil {
		if x, err := engine.NewIndex(draft); err == nil {
			price("Draft", x, engine.NewMapZips(draft.ZipCodes))
		} else {
			items = append(items, map[string]any{"label": "Draft", "value": err.Error(), "tone": "danger"})
		}
	}
	if v, err := loadActive(c.ctx, c.h); err == nil && v != nil {
		price("Published ("+v.active.Version+")", v.index, v.zips(c.ctx, c.h))
	} else {
		items = append(items, item("Published", "nothing is published"))
	}
	return reply(200, map[string]any{"result": map[string]any{"title": fmt.Sprintf("%s · %s · %s", strOr(p.ServiceType, "(no service)"), zip, strOr(dest, "no destination")), "items": items}})
}

// describe is a preview as lines a person reads.
func describe(pv *engine.Preview, rep distanceReport) string {
	var b strings.Builder
	if len(pv.Products) == 0 {
		b.WriteString("No product of that type for that service: Fuse prices nothing.\n")
	}
	for _, p := range pv.Products {
		fmt.Fprintf(&b, "%d × %s  %s", p.Quantity, p.Name, money(p.Subtotal))
		if p.HasDiscount {
			fmt.Fprintf(&b, " (list %s)", money(p.OriginalPrice*p.Quantity))
		}
		b.WriteString("\n")
	}
	for _, f := range pv.Fees {
		amount := f.Amount
		if f.PerQuantity {
			amount *= pv.TotalQuantity
		}
		var flags []string
		if f.StartingAt {
			flags = append(flags, "starting at")
		}
		if f.ExcludedFromTotal {
			flags = append(flags, "not in total")
		}
		if f.Recurring {
			flags = append(flags, "recurring")
		}
		if f.HasDiscount {
			flags = append(flags, "was "+money(f.OriginalAmount))
		}
		fmt.Fprintf(&b, "%s  %s", f.Name, money(amount))
		if len(flags) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(flags, ", "))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Subtotal %s · fees %s · discount %s · total %s\n", money(pv.Subtotal), money(pv.FeesTotal), money(pv.TotalDiscount), money(pv.Total))
	if pv.OutOfAreaZipText != nil {
		fmt.Fprintf(&b, "Out of area: %s\n", *pv.OutOfAreaZipText)
	}
	fmt.Fprintf(&b, "Distances: %s (%s, %d routes)", rep.Status, rep.Mode, rep.Routes)
	return b.String()
}

// testToken checks a quote token pasted from a link: genuine or not, and
// what it said.
func (c *call) testToken() *plugin.Response {
	var body struct {
		Test map[string]any `json:"test"`
	}
	_ = json.Unmarshal(c.req.Body, &body)
	t := form(body.Test).str("token")
	if i := strings.Index(t, "t="); i >= 0 { // a whole link pasted
		t = t[i+2:]
		if j := strings.IndexAny(t, "&#"); j >= 0 {
			t = t[:j]
		}
	}
	if t == "" {
		return invalid(map[string][]string{"token": {"paste a quote token, or the link that carries one"}})
	}
	p, err := verifyToken(c.ctx, c.h, t)
	if errors.Is(err, errTokenShape) {
		return invalid(map[string][]string{"token": {err.Error()}})
	}
	verdict := map[string]any{"label": "Signature", "value": "Genuine: these are the figures this site quoted.", "tone": "ok"}
	if err != nil {
		verdict = map[string]any{"label": "Signature", "value": "Not genuine — " + err.Error() + ". Don't rely on its figures.", "tone": "danger"}
	}
	items := []map[string]any{verdict,
		{"label": "Priced", "value": time.Unix(p.Iat, 0).UTC().Format(time.RFC3339), "time": true},
		item("Rates version", p.RV), item("Service", p.ST+" · "+p.PT), item("ZIP", strings.TrimSpace(p.Z+" → "+p.DZ)), item("Name", p.FN),
	}
	var lines []string
	for _, pr := range p.Fig.Products {
		lines = append(lines, fmt.Sprintf("%d × %s  %s", pr.Q, pr.N, money(pr.S)))
	}
	for _, f := range p.Fig.Fees {
		lines = append(lines, fmt.Sprintf("%s  %s", f.N, money(f.A)))
	}
	lines = append(lines, "Total "+money(p.Fig.Tot))
	items = append(items, map[string]any{"label": "Figures", "value": strings.Join(lines, "\n"), "multiline": true})
	if p.VU != "" {
		items = append(items, item("Valid until", p.VU))
	}
	return reply(200, map[string]any{"result": map[string]any{"title": "Quote token", "items": items}})
}
