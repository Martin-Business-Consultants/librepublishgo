package fuse

// The admin screens, as data (docs/plugins.md › Admin screens).

func opts(pairs ...string) []map[string]string {
	out := []map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, map[string]string{"value": pairs[i], "label": pairs[i+1]})
	}
	return out
}

var (
	appliesAtOptions = opts("pickup_location", "At the delivery ZIP (its zone, ZIP or location)", "delivery_location", "At the destination ZIP (Move It)", "zone_to_zone", "Between zones (a relocation)")
	totalModeOptions = opts(
		"included", "Included in the total",
		"starting_at", "“Starting at”, not in the total",
		"starting_at_included", "“Starting at”, but in the total",
		"excluded_shown", "Shown, billed outside the total",
		"excluded", "Not quoted at all",
	)
	discountTypeOptions = opts("fixed", "Fixed (cents off)", "percentage", "Percentage")
	durationOptions     = opts("indefinite", "Indefinite", "weeks", "Weeks", "months", "Months", "years", "Years")
	distanceOptions     = opts("auto", "Automatic: Google with a key, else the fixture, else off", "google", "Google Directions API", "fixture", "Recorded routes only (no network)", "off", "Off: every quote gets the “Starting at” lines")
	tierFields          = []map[string]any{
		{"name": "min_miles", "label": "From miles", "type": "integer", "required": true},
		{"name": "max_miles", "label": "To miles", "type": "integer", "required": true},
		{"name": "amount", "label": "Amount (cents)", "type": "integer", "required": true, "help": "Flat: the charge. Per mile or per hour: cents per mile or hour."},
		{"name": "rate_type", "label": "Rate", "type": "select", "options": rateTypeOptions},
		{"name": "position", "label": "Position", "type": "integer", "help": "Order in the list. Fuse quirk: the lowest position, not the shortest band, is the “Starting at” amount."},
	}
	discountHelp = "A discount's name, as under Discounts. Blank: none."
)

func link(screen string) map[string]any {
	return map[string]any{"screen": screen, "params": map[string]string{"id": "id"}}
}

func tableScreen(id, title, menu, source, rows, one string, columns []map[string]any, empty string) map[string]any {
	return map[string]any{
		"id": id, "title": title, "menu": menu, "source": source,
		"actions": []map[string]any{{"label": "New", "screen": one, "primary": true, "icon": "add"}},
		"table": map[string]any{
			"rows": rows, "key": "id", "columns": columns,
			"row_actions": []map[string]any{
				{"label": "Edit", "screen": one, "params": map[string]string{"id": "id"}},
				{"label": "Delete", "post": "/admin/" + one + "/delete", "params": map[string]string{"id": "id"}, "danger": true, "confirm": "Delete it from the draft? Publishing makes that live."},
			},
			"empty": map[string]string{"title": "None yet", "text": empty},
		},
	}
}

func formScreen(id, title, parent, record string, main, side []map[string]any) map[string]any {
	return map[string]any{
		"id": id, "title": title, "title_key": "name", "new_title": "New " + title, "parent": parent, "source": "/admin/" + record,
		"form": map[string]any{"record": record, "save": "/admin/" + record, "key": "id", "main": main, "side": side},
	}
}

func screens() []map[string]any {
	return []map[string]any{
		{
			"id": "overview", "title": "Pricing", "menu": "Overview", "source": "/admin/overview",
			"actions": []map[string]any{{"label": "Test a price", "screen": "test", "icon": "calculator"}, {"label": "Import from Fuse", "screen": "import", "icon": "upload"}},
			"detail": map[string]any{
				"record": "overview",
				"main":   []map[string]any{{"title": "Published", "items": "published_items"}, {"title": "Draft", "items": "draft_items"}},
				"side":   []map[string]any{{"title": "Distances and tokens", "items": "distance_items"}},
				"actions": []map[string]any{
					{"label": "Publish draft", "post": "/admin/publish", "confirm": "Publish the draft? Quotes price from it at once, and fuse.rates_published is sent (point a webhook at the site's deploy hook to rebuild its catalog prices).", "show_if": map[string]any{"has_draft": true}},
					{"label": "Discard draft", "post": "/admin/discard", "danger": true, "confirm": "Throw the draft away? The published rates stay as they are.", "show_if": map[string]any{"has_draft": true}},
					{"label": "Rotate token key", "post": "/admin/token/rotate", "confirm": "Sign new quote tokens with a new key? Links already sent keep verifying."},
				},
			},
		},
		{
			"id": "settings", "title": "Pricing settings", "menu": "Settings", "source": "/admin/settings",
			"form": map[string]any{
				"record": "settings", "save": "/admin/settings",
				"main": []map[string]any{
					{"title": "Rates (saved to the draft; live once published)", "fields": []map[string]any{
						{"name": "zip", "label": "Business ZIP", "type": "string", "help": "Every route starts and ends here (Fuse's settings.zip)."},
						{"name": "mileage_based_pricing", "label": "Price delivery legs by mileage", "type": "boolean", "help": "Initial Delivery, Relocation and Final Pickup from the Mileage tiers and Google's distance."},
						{"name": "mileage_round_trip", "label": "Each leg is a round trip from the business", "type": "boolean"},
						{"name": "zip_qualification", "label": "Only ZIPs in the table are served", "type": "boolean"},
						{"name": "out_of_area_zip_text", "label": "Out-of-area message", "type": "text"},
						{"name": "quote_validity_days", "label": "Quotes valid for (days)", "type": "integer", "help": "Printed in a quote token as valid until. Blank: no date."},
						{"name": "total_price_label", "label": "Total label", "type": "string", "help": "Blank: “Total”."},
						{"name": "name", "label": "Business name", "type": "string"},
						{"name": "phone", "label": "Phone", "type": "string", "help": "When a ZIP's location has none."},
					}},
				},
				"side": []map[string]any{
					{"title": "Distances (in force at once)", "fields": []map[string]any{
						{"name": "distance_mode", "label": "Distances from", "type": "select", "options": distanceOptions},
						{"name": "google_key", "label": "Google Maps server key", "type": "password", "help": "Restricted to the Directions API and this server's address. Sealed in storage; shown as ***abcd."},
						{"name": "known_zips_only", "label": "Only ask Google about ZIPs in the table", "type": "boolean", "help": "Cheaper, and safe from a script trying every ZIP. Fuse asks for any ZIP, so prices for an unknown ZIP can differ (no mileage legs) while this is on."},
						{"name": "monthly_cap", "label": "Google calls a month, at most", "type": "integer"},
					}, "actions": []map[string]any{{"label": "Check key", "icon": "check", "post": "/admin/settings/check_key", "send": "record", "help": "One Directions call from the business ZIP to itself."}}},
				},
			},
		},
		tableScreen("products", "Products", "Products", "/admin/products", "products", "product", []map[string]any{
			{"key": "name", "label": "Name", "link": link("product"), "sub": "product_type"},
			{"key": "price", "label": "Price", "num": true}, {"key": "service_types", "label": "Services"},
			{"key": "discount", "label": "Discount"}, {"key": "position", "label": "Position", "num": true}, {"key": "status", "label": "Status", "badge": true},
		}, "Import from Fuse, or add the units you rent or sell."),
		formScreen("product", "Product", "products", "product", []map[string]any{
			{"title": "Product", "fields": []map[string]any{
				{"name": "name", "label": "Name", "type": "string", "required": true},
				{"name": "price", "label": "Price (cents)", "type": "integer", "required": true, "help": "14900 is $149."},
				{"name": "product_type", "label": "Product type", "type": "string", "required": true, "help": "Its slug: portable-storage."},
				{"name": "service_types", "label": "Services", "type": "string_list", "help": "Slugs: keep-it, move-it."},
				{"name": "discount", "label": "Discount", "type": "string", "help": discountHelp},
				{"name": "ideal_for", "label": "Ideal for", "type": "string_list", "help": "Up to 3."},
				{"name": "image_url", "label": "Image address", "type": "string"},
			}},
		}, []map[string]any{{"title": "Listing", "fields": []map[string]any{
			{"name": "active", "label": "Active", "type": "boolean"}, {"name": "position", "label": "Position", "type": "integer"},
			{"name": "monthly", "label": "Billed monthly", "type": "boolean"}, {"name": "for_sale", "label": "For sale", "type": "boolean"},
			{"name": "length", "label": "Length (ft)", "type": "integer"}, {"name": "width", "label": "Width (ft)", "type": "integer"}, {"name": "height", "label": "Height (ft)", "type": "integer"},
		}}}),
		tableScreen("fees", "Fees", "Fees", "/admin/fees", "fees", "fee", []map[string]any{
			{"key": "name", "label": "Name", "link": link("fee"), "sub": "applies_at"},
			{"key": "amount", "label": "Amount", "num": true}, {"key": "total_mode", "label": "In total", "badge": true},
			{"key": "service_types", "label": "Services"}, {"key": "zones", "label": "Zones"}, {"key": "mileage", "label": "Mileage"}, {"key": "position", "label": "Position", "num": true},
		}, "Delivery, pickup, relocation, waivers: what's charged besides the unit."),
		formScreen("fee", "Fee", "fees", "fee", []map[string]any{
			{"title": "Fee", "fields": []map[string]any{
				{"name": "name", "label": "Name", "type": "string", "required": true, "help": "Fuse quirk: a fee named Initial Delivery, Relocation or Final Pickup is dropped while mileage prices that leg."},
				{"name": "amount", "label": "Amount (cents)", "type": "integer", "required": true, "help": "Per unit: multiplied by the quote's quantity."},
				{"name": "applies_at", "label": "Applies", "type": "select", "options": appliesAtOptions},
				{"name": "total_mode", "label": "Total", "type": "select", "options": totalModeOptions},
				{"name": "service_types", "label": "Services", "type": "string_list", "help": "Slugs. None: every service, but only when the fee is linked another way (a zone, ZIP or location)."},
				{"name": "product_types", "label": "Product types", "type": "string_list", "help": "Slugs. None: every product type."},
				{"name": "from_zones", "label": "From zones", "type": "string_list", "help": "Zone names: the delivery zones it applies in. None: any."},
				{"name": "to_zones", "label": "To zones", "type": "string_list", "help": "Zone names: the destination zones it applies to. None: any."},
				{"name": "discount", "label": "Discount", "type": "string", "help": discountHelp},
			}},
			{"title": "Mileage", "fields": []map[string]any{
				{"name": "mileage_based_pricing", "label": "Priced by mileage", "type": "boolean", "help": "From the tiers below (else the global Mileage tiers) over the whole route."},
				{"name": "exclude_beyond_last_tier", "label": "Refuse distances past the last tier", "type": "boolean", "help": "Fuse quirk: stored, but nothing in Fuse's pricing reads it; neither does this."},
				{"name": "tiers", "label": "Tiers", "type": "repeater", "of": tierFields},
			}},
		}, []map[string]any{{"title": "Listing", "fields": []map[string]any{
			{"name": "position", "label": "Position", "type": "integer", "help": "Fees are listed by position."},
			{"name": "recurring", "label": "Recurring", "type": "boolean", "help": "Billed each period (a damage waiver)."},
		}}}),
		tableScreen("discounts", "Discounts", "Discounts", "/admin/discounts", "discounts", "discount", []map[string]any{
			{"key": "name", "label": "Name", "link": link("discount")}, {"key": "amount", "label": "Amount"}, {"key": "duration", "label": "For"},
		}, "Promotions on products (by product, ZIP, zone, service, product type or location) and on fees."),
		formScreen("discount", "Discount", "discounts", "discount", []map[string]any{{"title": "Discount", "fields": []map[string]any{
			{"name": "name", "label": "Name", "type": "string", "required": true},
			{"name": "discount_type", "label": "Type", "type": "select", "options": discountTypeOptions},
			{"name": "amount", "label": "Amount", "type": "integer", "required": true, "help": "Cents for fixed (5000 is $50), percent for percentage (at most 100). Fuse quirk: a percentage rounds down to the cent."},
			{"name": "duration_unit", "label": "Lasts", "type": "select", "options": durationOptions},
			{"name": "duration_value", "label": "For how many", "type": "integer"},
		}}}, nil),
		tableScreen("zones", "Zones", "Zones", "/admin/zones", "zones", "zone", []map[string]any{
			{"key": "name", "label": "Name", "link": link("zone")}, {"key": "location", "label": "Location"}, {"key": "discount", "label": "Discount"}, {"key": "zips", "label": "ZIPs", "num": true},
		}, "Groups of ZIPs that fees and discounts apply to."),
		formScreen("zone", "Zone", "zones", "zone", []map[string]any{{"title": "Zone", "fields": []map[string]any{
			{"name": "name", "label": "Name", "type": "string", "required": true},
			{"name": "location", "label": "Location", "type": "string", "required": true, "help": "A location's name."},
			{"name": "discount", "label": "Discount", "type": "string", "help": discountHelp},
		}}}, []map[string]any{{"title": "Linked", "readonly": "linked"}}),
		tableScreen("locations", "Locations", "Locations", "/admin/locations", "locations", "location", []map[string]any{
			{"key": "name", "label": "Name", "link": link("location")}, {"key": "phone", "label": "Phone"}, {"key": "fee", "label": "Fee"}, {"key": "discount", "label": "Discount"},
		}, "Yards: each has its zones, a phone shown for its ZIPs, and optionally a fee and a discount."),
		formScreen("location", "Location", "locations", "location", []map[string]any{{"title": "Location", "fields": []map[string]any{
			{"name": "name", "label": "Name", "type": "string", "required": true},
			{"name": "phone", "label": "Phone", "type": "string"},
			{"name": "fee_id", "label": "Fee (its id)", "type": "integer", "help": "Fee names repeat, so by id (Fees list it)."},
			{"name": "discount", "label": "Discount", "type": "string", "help": discountHelp},
		}}}, nil),
		{
			"id": "zips", "title": "ZIPs", "menu": "ZIPs", "source": "/admin/zips",
			"actions": []map[string]any{{"label": "Add or replace ZIPs", "screen": "zips_bulk", "primary": true, "icon": "upload"}},
			"table": map[string]any{
				"rows": "zips", "key": "id",
				"filters":     []map[string]any{{"param": "zone", "label": "Zone", "options": "zones"}},
				"cursor":      map[string]any{"param": "after", "next": "next"},
				"columns":     []map[string]any{{"key": "postal_code", "label": "ZIP"}, {"key": "zone", "label": "Zone"}, {"key": "location", "label": "Location"}, {"key": "fee", "label": "Fee"}, {"key": "discount", "label": "Discount"}},
				"row_actions": []map[string]any{{"label": "Remove", "post": "/admin/zip/delete", "params": map[string]string{"id": "id"}, "danger": true, "confirm": "Remove this ZIP from the draft?"}},
				"empty":       map[string]string{"title": "No ZIPs", "text": "Import from Fuse, or paste them under Add or replace ZIPs."},
			},
		},
		{
			"id": "zips_bulk", "title": "Add or replace ZIPs", "parent": "zips", "source": "/admin/zips_bulk",
			"form": map[string]any{"record": "bulk", "save": "/admin/zips_bulk", "main": []map[string]any{{"title": "ZIPs", "fields": []map[string]any{
				{"name": "csv", "label": "One per line", "type": "text", "help": "zip,zone,location[,fee id][,discount] — 78701,Central,Austin. A zone may be blank."},
				{"name": "mode", "label": "Mode", "type": "select", "options": opts("merge", "Add these, updating ZIPs already in the table", "replace_all", "Make the table exactly these")},
			}}}},
		},
		tableScreen("service_types", "Service types", "Service types", "/admin/service_types", "service_types", "service_type", []map[string]any{
			{"key": "name", "label": "Name", "link": link("service_type"), "sub": "slug"}, {"key": "discount", "label": "Discount"},
		}, "Keep It, Move It, Store It."),
		formScreen("service_type", "Service type", "service_types", "service_type", []map[string]any{{"title": "Service type", "fields": []map[string]any{
			{"name": "name", "label": "Name", "type": "string", "required": true},
			{"name": "slug", "label": "Slug", "type": "string", "help": "Sites ask for it by this (move-it). Fuse quirk: “move-it” is what gets the relocation leg."},
			{"name": "discount", "label": "Discount", "type": "string", "help": discountHelp},
		}}}, nil),
		tableScreen("product_types", "Product types", "Product types", "/admin/product_types", "product_types", "product_type", []map[string]any{
			{"key": "name", "label": "Name", "link": link("product_type"), "sub": "slug"}, {"key": "discount", "label": "Discount"},
		}, "Portable storage, shipping containers."),
		formScreen("product_type", "Product type", "product_types", "product_type", []map[string]any{{"title": "Product type", "fields": []map[string]any{
			{"name": "name", "label": "Name", "type": "string", "required": true},
			{"name": "slug", "label": "Slug", "type": "string"},
			{"name": "discount", "label": "Discount", "type": "string", "help": discountHelp},
		}}}, nil),
		{
			"id": "mileage", "title": "Mileage tiers", "menu": "Mileage", "source": "/admin/mileage",
			"form": map[string]any{"record": "mileage", "save": "/admin/mileage", "main": []map[string]any{{"title": "Global tiers", "fields": []map[string]any{
				{"name": "tiers", "label": "Tiers", "type": "repeater", "of": tierFields, "help": "Flat and per-hour bands apply when the leg's miles fall inside them; per-mile bands stack. Fuse quirk: a leg on a band edge pays both flat bands."},
			}}}},
		},
		{
			"id": "import", "title": "Import from Fuse", "parent": "overview", "source": "/admin/import",
			"form": map[string]any{"record": "import", "save": "/admin/import", "main": []map[string]any{{"title": "Fuse rate export", "fields": []map[string]any{
				{"name": "bundle", "label": "The export", "type": "text", "help": "The file bin/rails pricing:export TENANT=<tenant> wrote, pasted whole. It replaces the draft; the published rates don't change until you publish."},
				{"name": "images", "label": "Put product images in the media library", "type": "boolean"},
				{"name": "distance_fixture", "label": "Recorded routes (optional)", "type": "text", "help": "For the fixture distance mode: {\"<origin> → <destination> → <waypoints>\": {\"status\": \"OK\", \"legs_meters\": […], \"legs_seconds\": […]}}."},
			}, "actions": []map[string]any{{"label": "Check the file", "icon": "search", "post": "/admin/import/check", "send": "record", "help": "What importing would change in the draft, table by table. Nothing is saved."}}}}},
		},
		{
			"id": "test", "title": "Test a price", "menu": "Price tester", "source": "/admin/test",
			"form": map[string]any{"record": "test", "save": "/admin/test", "main": []map[string]any{
				{"title": "Price", "fields": []map[string]any{
					{"name": "zip", "label": "ZIP", "type": "string", "required": true},
					{"name": "dest", "label": "Destination ZIP", "type": "string", "help": "For Move It."},
					{"name": "service", "label": "Service", "type": "string", "help": "A slug: keep-it."},
					{"name": "product_type", "label": "Product type", "type": "string", "help": "A slug. Blank: any."},
					{"name": "product", "label": "Product id", "type": "integer", "help": "Blank: the first, as Fuse does."},
					{"name": "qty", "label": "Quantity", "type": "integer"},
				}, "actions": []map[string]any{{"label": "Price it", "icon": "calculator", "post": "/admin/test/price", "send": "record", "help": "On the draft and on the published rates, side by side. Nothing is kept."}}},
				{"title": "Check a quote token", "fields": []map[string]any{
					{"name": "token", "label": "Token or link", "type": "text", "help": "A customer's /quote-thank-you?t=… link: whether it's genuine, and what it quoted."},
				}, "actions": []map[string]any{{"label": "Check token", "icon": "shield", "post": "/admin/test/token", "send": "record"}}},
			}},
		},
		{
			"id": "versions", "title": "Published versions", "menu": "Versions", "source": "/admin/versions",
			"table": map[string]any{
				"rows": "versions", "key": "version",
				"columns":     []map[string]any{{"key": "version", "label": "Version", "sub": "source"}, {"key": "published_at", "label": "Published", "time": true}, {"key": "by", "label": "By"}, {"key": "status", "label": "", "badge": true}},
				"row_actions": []map[string]any{{"label": "Roll back to this", "post": "/admin/rollback", "params": map[string]string{"version": "version"}, "confirm": "Price quotes from this version again?"}},
				"empty":       map[string]string{"title": "Nothing published", "text": "The last 10 published versions are kept here to roll back to."},
			},
		},
	}
}
