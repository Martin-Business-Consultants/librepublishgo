# Fuse pricing

Fuse pricing is a plugin (`plugins/fuse`, key `fuse`) built into the binary
but not installed by default. It holds one site's rate book and prices
quotes the way Fuse does: the same products, fees, discounts, zones, ZIPs
and mileage, the same figures to the cent, the same JSON as Fuse's quoting
API. In the admin it's **Tools › Pricing**.

It prices and nothing else. No quote, lead or customer is stored; a quote
travels as a signed token in the link the customer gets. There's no CRM and
there are no emails.

## Installing it

```sh
cms post /plugins '{"plugin":{"key":"fuse"}}'
cms patch /plugins/fuse '{"plugin":{"granted":{"storage":true,"media:write":true,"http":["maps.googleapis.com"]},"enabled":true}}'
```

`media:write` is only for product images that come in with an import, and
`maps.googleapis.com` is the only host it may call.

Capabilities, for Settings › Roles: `fuse:read` (see the rate book, test
prices), `fuse:write` (edit and import the draft), `fuse:publish` (publish,
roll back) and `fuse:secrets` (the Google key, rotating the token key).
Administrators have all four. The Agent role has none until it's given
them; give it read and write, and leave publishing to a person.

## The rate book: draft, publish, roll back

Every edit and import goes to the **draft**. Quotes price from the
**published** version until someone publishes the draft (Overview ›
Publish draft), which:

1. checks the book holds up (Fuse's own validations, and every link
   resolving),
2. writes it whole as a new version,
3. points quotes at it in one write, and
4. sends `fuse.rates_published`. Point a webhook (Settings › Webhooks) at
   the site's deploy hook to rebuild build-time prices.

The last 10 versions are kept (Versions › Roll back to this).

Storage stays within the plugin storage limits (1 MB a value, 100,000
keys). A book is cut into parts of at most 900 KB, and its ZIPs are split
into one shard per 3-digit prefix, so a quote reads at most two shards.

## Importing from Fuse

Fuse writes the export with a read-only rake task
(`fuse/lib/tasks/pricing_export.rake`):

```sh
bin/rails pricing:export TENANT=<tenant> > rates.json    # IMAGES=0 leaves product images out
```

The export holds the rate tables (with Fuse's ids) and the pricing
settings. It never holds API keys, tokens or other secrets, and it carries
a SHA-256 of its rates, so an edited or cut-short file is refused.

To load it, use **Pricing › Import from Fuse**: paste the file, then
**Check the file** for a dry-run diff, then **Save** to load it into the
draft. Or from a terminal:

```sh
jq -Rs '{import: {bundle: .}}' rates.json > import.json
cms post /x/fuse/admin/import/check - < import.json    # dry run: what would change
cms post /x/fuse/admin/import - < import.json          # into the draft
cms post /x/fuse/admin/publish '{}'                    # live (needs fuse:publish)
```

## Distances

When mileage pricing is on, Fuse asks Google's Directions API for each
quote's route from the business ZIP. The plugin sends the same request
(`origin=<zip>, USA`, waypoints as the bare ZIPs), so its miles match
Fuse's.

- **The key** goes in Settings › Google Maps server key. Use a key
  restricted to the Directions API and to this server's address. It's
  sealed in storage, shown back as `***abcd`, and never logged.
  **Check key** makes one call to test it.
- **Cache.** Answers are kept 180 days, `ZERO_RESULTS` for 7 days, and
  other refusals for 10 minutes. Timeouts aren't kept.
- **Budget.** All of a quote's routes are fetched in parallel within 4
  seconds (a caller can ask for less with `budget_ms`). A route not
  answered by then has no answer, and the quote gets Fuse's own no-distance
  lines ("Starting at"), as Fuse does when Google fails.
- **Cap.** At most `monthly_cap` calls a month (20,000 unless set); calls
  are counted on the Overview.
- **Only ZIPs in the table.** Off by default, to match Fuse, which asks
  about any ZIP. Turned on, a ZIP outside the table never reaches Google.
- **Fixture mode.** Recorded routes only, with no network, for a machine
  without a key and for the tests. Add them under Import › Recorded routes,
  keyed `"<origin> → <destination> → <waypoints>"`. A route that isn't
  recorded gets the no-distance lines.

The mode is Settings › Distances from: automatic (Google with a key, else
the fixture, else off), or any one of them.

## The routes

All are under `/api/x/fuse`. Amounts are in cents.

### `GET|POST /quote`

| Field | |
| --- | --- |
| `zip` | required, 5 digits (unless `t` is given) |
| `dest` | the destination ZIP, optional |
| `product_type` | default `portable-storage`; `""` for any |
| `services` | `all` (the default: every service with a product of the type), `"keep-it,move-it"` or a list |
| `dest_for` | the services `dest` is priced for: `move-it` (the default), `all`, or a list |
| `selected`, `product`, `qty` | the service the customer chose, the product and its quantity |
| `quantities` | POST only: `{"<product id>": n}` for the selected service, as Fuse takes them |
| `mint` | POST only: `true` answers a `token` for the selected service |
| `name` | its first word goes in the token |
| `t` | a token: when it's genuine and unexpired, its inputs are priced again (they replace the request's own) and `quoted` carries it; otherwise `422 invalid_token` (see [Quote tokens](#quote-tokens)) |
| `budget_ms` | at most this long for Google (≤ 4000) |

The answer:

```jsonc
{
  "rates_version": "20261023-041500-a1b2c3",
  "zip": "78701", "dest": "78626", "product_type": "portable-storage",
  "service_area": {"postal_code": "78701", "served": true, "enforced": true, "configured": true, "message": null},
  "location_phone": "5125550100",
  "out_of_area_text": "…",                      // only when the ZIP isn't in the table
  "labels": {"total": "Due Before Delivery"},
  "service_order": ["keep-it", "move-it", "store-it"],
  "services": {
    "move-it": {
      "name": "Move It", "available": true, "dest": "78626",
      "products": [ … ],                         // the catalog's products of the type, Fuse's /config shape
      "product_id": 15,
      "preview": { … },                          // Fuse's POST /api/v1/quoting/preview, field for field
      "summary": {"monthly": 14900, "first_month": 9900, "promo": 5000, "discount": {…},
                  "recurring_fees": […], "recurring_total": 1500, "transit": […],
                  "included_one_time": 73154, "excluded_one_time": 0, "due": 84554}
    }
  },
  "sale_from": {"product_id": 21, "price": 300000},   // the cheapest priced unit for sale, when there is one
  "distance": {"status": "ok", "mode": "google", "routes": 3, "live": 3, "ms": 412},
  "quoted": {"valid": true, "payload": {…}, "rates_changed": false},   // with a token it trusts
  "token": "v1.…"                                                      // POST with mint
}
```

`preview` is Fuse's preview: same fields, values and order. Nothing is
filtered: every line keeps its `starting_at`, `excluded_from_total`,
`excluded_shown` and `recurring` flags, because each site has its own rules
for what to show. `summary` does the arithmetic every thank-you page
repeats; `due` equals `preview.total`.

Errors: `400 invalid_zip` and `400 invalid_dest`, `409 no_rates` (nothing
published), `422 cannot_mint` (mint without a priced `selected`
service), and `422 invalid_token` (a `t` it can't trust; checked before
anything else, so never answered as `invalid_zip`).

### `GET /service-area?zip=`

Fuse's `GET /api/service_areas/:zip`:
`{postal_code, served, enforced, configured, message}`. It's biased
toward served: with ZIP qualification off, or no ZIPs loaded (or nothing
published), every ZIP is served. A 5-digit ZIP matches itself and any
ZIP+4 under it.

### `GET /catalog[?zip=&service_type=]`

Fuse's config catalog: `service_types`, `product_types`, and `products`
with their ZIP-aware discounts, for "from $149/mo" copy at build time.

### Rate limits

Public routes take 30 calls a minute from one address when called with no
token, so a site's server (SSR, a form hook) should send its
`PRODUCTION_TOKEN`: a caller with a token isn't limited. A browser asking
once per page view is fine.

## Quote tokens

```
v1.<base64url(payload)>.<base64url(HMAC-SHA256(key, "v1." + payload part)[:16])>
```

The payload holds the inputs (ZIPs, service, product, quantity), the rates
version, the time, a first name and the selected service's figures. It
holds no email, phone or last name. It isn't encrypted: a site can decode
it with no CMS to show the quoted figures while the CMS is down. It is
signed, though, so **Price tester › Check a token** tells staff whether a
link a customer reads out is genuine.

The key is 32 random bytes, sealed in storage, made on the first mint.
**Rotate token key** signs new tokens with a new key; links signed with an
old key still verify.

When the published book sets `quote_validity_days`, the token carries
`vu`, the last day the quote holds (`YYYY-MM-DD`): the day it was priced
plus that many days, in UTC. The plugin isn't told the site's time zone,
so UTC is the calendar both for writing `vu` and for checking it. A token
with no `vu` doesn't expire.

`/quote?t=` prices a token only when it can trust it: the signature
holds, under a key this site has, and today (UTC) is no later than `vu`.
Any other token is answered, before anything is priced and whatever else
the request sends,

```jsonc
// 422
{"error": "invalid_token", "reason": "bad_signature", "message": "The token's signature doesn't match: it was changed."}
```

with `reason` one of

| `reason` | when |
| --- | --- |
| `malformed` | it doesn't decode as a `v1.` token (or says no ZIP) |
| `unknown_key` | it names a key id this site doesn't have |
| `bad_signature` | the signature doesn't match: the payload or the signature was changed |
| `expired` | genuine, but `vu` has passed |

No `services`, no `quoted`, nothing the token carried (not its first
name, not its ZIPs) comes back. A token that is trusted comes back as
`quoted: {"valid": true, "payload": …, "rates_changed": …}`; a page that
shows a token's figures or name should still check `quoted.valid` before
showing anything from it, and a page that decodes a token itself, without
the CMS, has no signature check: it must treat what it reads as
unverified. **Price tester › Check a token** still reads an expired or
forged token for staff, and says which it is.

## Where it isn't Fuse

The goal is the same figures. Each quirk below is kept on purpose and
marked `Fuse quirk:` in the code; the golden tests pin them.

- Ruby's `Float#round(n)`, `Float#to_s`, `Array#sum` (compensated),
  `#to_i` and Integer division (flooring) are reimplemented, and so is the
  order `sort_by` leaves ties in (MRI's `rb_uniform_sort`, which isn't
  stable past 16 elements).
- **Percentage discounts** floor to the cent: 15% off 14,999 is 2,249 off.
- **"Starting at" amounts differ by path.** A mileage-based fee's starting
  amount is its *lowest-position* tier's, not the shortest band's. The
  no-distance "Relocation Fee" line uses the shortest band's. A
  zone-restricted service fee's placeholder shows `amount`, while a
  zone-to-zone placeholder shows `effective_amount`.
- **Zone-to-zone placeholders** include `excluded` fees; exact zone-to-zone
  matches don't.
- **An unknown service slug** matches only fees for every service. An
  unknown product type matches every fee.
- **A product type slug, when given,** is the only product-type discount
  looked at; the product's own type's isn't.
- **The catalog's discount** uses the ZIP alone (no service or product
  type), while the preview's uses everything, so the two can disagree.
- **Mileage edges.** A leg on a band edge pays both flat bands. A per-fee
  mileage line uses the whole route's miles. `exclude_beyond_last_tier` is
  stored but read by nothing.
- **A ZIP stored only as ZIP+4** is served, but the preview's exact match
  misses it (the out-of-area text, no zone).
- **Quantities naming only other products** aren't "all zero", so nothing
  is defaulted and the preview prices nothing.
- **"move-it" is a literal slug.** Only a service with that slug gets the
  no-distance relocation line.
- **Calculator lines never say `recurring`**, even one made from a
  recurring fee.

Not ported: storing quotes, idempotency, leads and lead forms, emails,
journeys, the hosted quote pages, address autocomplete and insurance.

## Tests

`plugins/fuse/engine` is checked against Fuse's own answers.
`testdata/austin/tools/seed_austin.rb` seeds a sandbox copy of Fuse with a
synthetic Austin book: real Austin ZIPs, invented prices that reach every
branch. `record_goldens.rb` then asks Fuse's real controllers, in-process,
for 338 previews, 80 configs and 38 service-area answers, with Google
replaced by recorded routes (`distances.json`). The engine's tests replay
those answers, and so do the plugin's, through import, publish, storage and
`/quote`.
