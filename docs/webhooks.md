# Webhooks

A webhook tells another system when content changes: an analytics tool, a
search index, a chat channel, your own service. Add one in Settings ›
Webhooks (or `cms webhooks`), choose its events, and the CMS POSTs to its URL
each time one happens.

What a receiver can rely on is pinned in
`contracts/webhook_contract.json`, and
the tests in `internal/cms` hold the CMS to it.

## The request

```http
POST <the webhook's URL>
Content-Type: application/json
User-Agent: librepublish-webhooks/1
X-CMS-Event: page.published
X-CMS-Delivery: 9f2c4e…  (the delivery's id, the same on each retry)
X-CMS-Timestamp: 1789640400  (Unix seconds, when this attempt was signed)
X-CMS-Signature: sha256=<hex HMAC-SHA256 of the raw body, keyed with the webhook's secret>
X-CMS-Signature-V2: sha256=<hex HMAC-SHA256 of "<X-CMS-Timestamp>.<raw body>", same key>

{"event": "page.published",
 "tenant": "acme",
 "delivered_at": "2026-09-17T10:00:00Z",
 "data": {"id": 12, "slug": "about", "path": "about",
          "url": "https://acme.test/about", "title": "About us",
          "status": "published", "locale": "en",
          "published_at": "2026-09-17T09:59:00Z",
          "updated_at": "2026-09-17T09:59:00Z"}}
```

- `event` is one of the webhook's events: `page.*` and `entry.*`
  (`published`, `updated`, `unpublished`, `deleted`), `global.updated`,
  `schema.changed`, and the plugins' events (`submission.created`, `quote_request.created`,
  `lead.created`, `lead.stage_changed`, …).
- `tenant` is the install's site key: `SITE_KEY`, else the first label of
  `APP_HOST`. A receiver serving several sites tells them apart by it, so
  when an install moves to a new host, keep `SITE_KEY` as it was.
- A page's `data` and an entry's carry the keys the contract lists; an entry
  has `collection_slug` in place of `path`.

That's an **internal** webhook: a receiver you run, sent the CMS's
envelope and signed. Another service that wants its own shape takes an
external one (below).

## External webhooks

An **external** webhook is another service's address (a CRM, a mailing
list) that wants its own JSON, not the CMS's envelope. Its **body** is a
list of fields, each a key of what's sent, a value and a transform:

| Key | Value | Transform |
| --- | --- | --- |
| `service_type` | `values.service_type` | `slug` |
| `initial_start_date` | `values.delivery_date` | `date_us` |
| `email` | `values.email` | |
| `source` | `website` | `text` |

A value is a dotted path in the event's `data` (`values.email`, a
submission's answer; `hook.quote_id`, what its form's hook added; `form`;
`submission_id`), or, with `text`, the text sent as it is. The transforms
are `slug` (Keep It → `keep-it`), `lowercase`, `uppercase`, `date_us`
(2026-11-14 → `11/14/2026`), `number`, and `text`; none leaves the value as
it is. A path the event lacks sends `""` (`null` for a number), and a list's
items are transformed one by one. With no fields, the event's `data` is
sent as it is.

```http
POST <the webhook's URL>
Content-Type: application/json
User-Agent: librepublish-webhooks/1

{"email": "ada@example.test", "initial_start_date": "11/14/2026",
 "service_type": "move-it", "source": "website"}
```

It isn't signed, and has no `X-CMS-Event` (it does carry
`X-CMS-Delivery`): the service doesn't know the CMS. Its headers (`headers` in the API) are sent with it, for one that
wants a key. "Send a test" posts the test event through its fields, which
a CRM takes for a real, mostly blank, lead.

```sh
cms post /webhooks '{"webhook":{"name":"Stella","kind":"external","url":"https://…",
  "events":["submission.created"],
  "event_filters":{"submission.created":{"form":["quote"]}},
  "body_map":[{"key":"service_type","value":"values.service_type","transform":"slug"}, …]}}'
```

## Only some events

A webhook's **Only when** conditions (`event_filters`) narrow an event to
those whose data says so: `{"submission.created": {"form": ["quote"]}}`
takes only the quote form's submissions. Each is a path, as above, and the
values it may have (any of a list's); every condition on the event must
hold. An event without conditions is taken whole.

A plugin can name the choices for one of its events, and the editor shows
them by name: with Forms on, a webhook that takes `submission.created` has
a **Forms** list to tick, which is the `form` condition. None ticked takes
every form's, ones added later included. The list of webhooks says which
forms each one is for.

## Testing

**Test webhook** (`POST /api/webhooks/<id>/preview`, `{event, data}`, both
optional) shows the delivery an event would make, exactly: its URL, headers
(the webhook's own masked) and body, byte for byte. It sends nothing. The
data is the latest of that event delivered that the webhook takes, or, with
none yet, a sample shaped to it: every path its body reads filled in, and
its conditions met. Edit it and preview again.

A webhook's **testing URL** (`testing_url`; a request catcher, a staging
copy) is where a test is sent, so the real receiver never sees it:
`POST /api/webhooks/<id>/test` with `{"event", "data", "to": "testing"}`.
`"to": "live"` (the default) sends it to the webhook's own URL. A test is
logged in Deliveries, marked Test, and doesn't change whether the webhook is
shown as failing. With no body, the test is the `webhook.test` event, as
before.

## schema.changed

```json
{"event": "schema.changed", "tenant": "acme", "delivered_at": "2026-09-17T10:00:00Z",
 "data": {"schema_digest": "sha256:4c71…"}}
```

Sent when the digest of `/api/v1/schema` (its `meta.schema_digest`, and
every `/api/v1` answer's `X-CMS-Schema-Digest`) moves: a block type, a
collection, a global or a live page's fields changed, or which collections
are served. Writes are let settle for two seconds first, and nothing is
sent when they leave the digest as it was. `data.schema_digest` is the new
digest; a site whose generated types were built from another regenerates
them. Like a publish, it also rebuilds or purges the site (cache tag
`schema`).

## Verifying the signature

Check `X-CMS-Signature-V2`, which covers when the post was signed as well
as what it says, so a post someone captured can't be replayed later:

1. Read `X-CMS-Timestamp` (Unix seconds) and refuse the post when it's more
   than **5 minutes** from your clock, either way. Each attempt is signed
   afresh, so a retry an hour later carries a fresh timestamp.
2. Compute the HMAC-SHA256, keyed with the webhook's secret, of the
   timestamp, a `.`, and the raw request body, byte for byte:
   `"<X-CMS-Timestamp>.<body>"`.
3. Compare its hex, in constant time, with `X-CMS-Signature-V2` (after
   `sha256=`).
4. Drop a post whose `X-CMS-Delivery` you've already handled: a retry of a
   delivery that reached you but whose answer didn't reach the CMS.

```js
const ts = req.headers["x-cms-timestamp"];
if (Math.abs(Date.now() / 1000 - Number(ts)) > 300) return res.status(400).end();
const want = "sha256=" + crypto.createHmac("sha256", secret).update(`${ts}.`).update(rawBody).digest("hex");
const got = req.headers["x-cms-signature-v2"] ?? "";
if (got.length !== want.length || !crypto.timingSafeEqual(Buffer.from(got), Buffer.from(want))) return res.status(401).end();
```

`X-CMS-Signature` is the original signature, over the body alone, and is
still sent for receivers that check it: compute the HMAC-SHA256 of the raw
body with the secret and compare it, in constant time (after `sha256=`). It
proves who sent a post, not when, so prefer the v2 one. Form hooks
(docs/forms.md) and on-demand purges (docs/delivery-api.md) are signed the
same way, with the same headers. The CMS generates a secret for every
webhook, and can rotate it. A receiver
that issues its own secret instead can have the webhook carry it:

```ruby
Webhook.create!(name: "analytics", url: "https://hooks.example.com/content",
                events: Webhook::EVENTS.grep(/\A(page|entry)\./),
                secret: "<the secret the receiver issued>")
```

## The URL is the record's address

`data.url` is the address the site publishes the record at: absolute,
following its SEO canonical, and the same as the sitemap's (both come from
`PubliclyAddressable`). A receiver can match it to rows that arrive keyed by
URL — search performance, analytics. It is absolute only once Settings ›
General has the site's URL; until then it is a path. A new content type that
receivers should see includes `PubliclyAddressable` and gives a
`default_public_path`, rather than computing its address another way.

## Deliveries

A delivery is queued (in the database, so a restart doesn't lose it) and
sent in the background, four at a time. One that can't connect, times out,
or is answered 408, 429 or 5xx is sent again after 30 seconds, 2, 5, 15 and
40 minutes: six attempts over about an hour, then it's given up on. Any
other answer (a 4xx: the receiver refused it) is final. Every attempt is
the same delivery, with the same `X-CMS-Delivery`. One under way when the
CMS stops is sent again when it starts.

Each attempt is logged with its status, timing, any error and the body it
sent (Deliveries › Body), and a webhook that keeps failing is shown as
failing (`failure_count` counts the attempts that failed since the last one
that got through).

## Where a webhook may post

A webhook's URL (a form hook's, a deploy hook's, a plugin's requests too)
must reach a public address. The check is on the address actually dialed,
so neither DNS nor a redirect gets around it:

- Never: link-local (`169.254.0.0/16`, `fe80::/10`, where most clouds keep
  their metadata service), `0.0.0.0/8`, multicast, reserved and broadcast,
  `fd00:ec2::254` and `100.100.100.200` (AWS's and Alibaba's metadata), and
  the IPv4-mapped, NAT64 and 6to4 forms of all of these.
- Only when the install sets `OUTBOUND_ALLOW_PRIVATE=1` (a site on the
  same box or network): loopback, `10/8`, `172.16/12`, `192.168/16`,
  carrier-grade NAT `100.64/10`, IPv6 unique-local `fc00::/7`, and NAT64
  `64:ff9b::/96`. The Rails core's `CMS_ALLOW_PRIVATE_WEBHOOKS=true` is read
  as the same.

A redirect is followed (up to 5) only on the host the URL names, and never
from `https` to `http`: the signed body and the webhook's own headers go
nowhere it didn't name. A receiver that has moved gets its new address
saved instead. `HTTP_PROXY`/`HTTPS_PROXY` aren't used for these posts:
through a proxy, the CMS could check only the proxy's address.
