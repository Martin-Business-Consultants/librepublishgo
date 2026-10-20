# Forms

Forms is a default plugin (`plugins/forms`): it comes with the binary,
installed and switched on. In the admin it's **Content › Forms**.

## Building a form

- **Fields**: drag a type from the left onto the form (or click it to add it
  after the chosen field), drag the grip to reorder (or focus it and press
  the arrow keys), and click a field to edit it on the right: label, name,
  required, width (full or half), help, placeholder, choices, limits. The
  name is what the site posts and the merge tag; a new field's name follows
  its label until you edit it.
- **Show only when**: a field can be shown only when a field above it has
  one of some answers (a choice, a dropdown's option, a ticked consent box):
  "Destination zip" only when "Service" is "Move It". A field that's hidden
  isn't asked for: it isn't required, and anything posted for it is dropped.
  The site hides and shows it as the answers change (the definition carries
  each field's `show_if`).
The editor's tabs: **General** (name, slug, status, the site's URLs),
**Editor** (the fields), **Admin Confirmation**, **User Confirmation**,
**Spam**, **Hook** and **After Submission**.

- **Admin Confirmation**: who gets each submission (to, cc, bcc), the subject,
  the message, which email field replies go to, and a from name and address
  for this form. Blank ones use Settings › Email.
- **User Confirmation**: sent to the address they typed in the email field
  you choose, with its own subject, message, reply-to and from.
- **Messages** are rich text (Tiptap) and start from boilerplate. The values
  they can use are listed beside them: drag one into the message, or click it
  in at the cursor. They're sent as HTML (answers escaped, `{{ all_fields }}`
  as a table) with a plain-text copy.
- **Spam**: the honeypot (`_hp`), a minimum time between the page loading
  (`_ts`) and the post, and a captcha: none, Cloudflare Turnstile or Google
  reCAPTCHA (v2, or v3 with a lowest score), each with its own keys.
- **Hook**: an address on the site that hears of each submission first (see
  [The hook](#the-hook)).
- **After Submission**: the button label, the thank-you message, or a page to
  send them to.

Merge tags: `{{ <field name> }}`, `{{ all_fields }}`, `{{ form_name }}`,
`{{ site_name }}`, `{{ submission_id }}`, `{{ date }}`, and the values the
form's hook adds (spaces inside the braces are optional).

Email needs Settings › Email (an SMTP server and a default from name and
address); **Send test email** there checks it.

## On the site

```
GET  /api/x/forms/list                     every form: slug, name, whether it's open, its digest
GET  /api/x/forms/definition?form=<slug>   the fields, labels, choices, rules and spam checks to draw
POST /api/x/forms/submit?form=<slug>       a submission
```

They're public (no token) and their answers are pinned in
`contracts/forms/` (contracts/README.md). The list:

```json
{"forms": [{"slug": "contact", "name": "Contact", "open": true, "digest": "sha256:6e80…"}]}
```

The definition:

```json
{"form": {
  "slug": "contact", "name": "Contact", "open": true,
  "action": "https://cms.example/api/x/forms/submit?form=contact",
  "submit_label": "Send", "success_message": "Thanks!", "redirect_url": "",
  "spam": {"honeypot_field": "_hp", "timestamp_field": "_ts", "min_seconds": 3, "captcha": null},
  "fields": [
    {"type": "heading", "name": "", "label": "", "text": "Say hello"},
    {"type": "email", "name": "email", "label": "Email", "required": true,
     "rules": {"required": true, "max_length": 500, "pattern": "^[^\\x00-\\x20@<>,;]+@…$"}},
    {"type": "number", "name": "guests", "label": "Guests", "min": 1, "max": 12,
     "rules": {"required": false, "max_length": 500, "min": 1, "max": 12, "pattern": "^-?(?:…)$"}},
    {"type": "file", "name": "brief", "label": "Brief", "accept": ".pdf, image/*", "max_mb": 2,
     "rules": {"required": false, "accept": [".pdf", "image/*"], "max_mb": 2}}
  ],
  "digest": "sha256:6e80…"}}
```

- `digest` is `"sha256:"` and the hex SHA-256 of the rest of `form` (every
  key but `digest`) in the same canonical JSON as the schema digest
  (docs/delivery-api.md): a site built against one definition can tell when
  it's changed. The list carries the same digest.
- Each field but a heading has `rules`: what the server checks, resolved
  from the field's type and settings by the same table submit validates
  with (`plugins/forms/rules.go`), so checking them in the browser checks
  what the server will. A key that doesn't apply to the type is left out.
  - `required` (always): a checkbox must be ticked, a choice made.
  - `max_length`: the most characters (each answer, for checkboxes): the
    field's own, else 500, or 10,000 for a paragraph. Not on files.
  - `min`, `max` (number): the bounds, when set.
  - `pattern` (email, tel, url, date, number): a regular expression the
    trimmed answer must match, written so JavaScript's `RegExp` (with no
    flag, `u` or `v`, so an `<input pattern>` too) and Go read it alike.
  - `options` (select, radio, checkboxes): the answers allowed.
  - `accept`, `max_mb` (file): extensions (`.pdf`), MIME types or families
    (`image/*`), lowercased; the size limit in MB (5 unless the field sets
    one).
- The server is stricter than a pattern in three places, on purpose: a date
  must also be a real one (`2026-02-30` matches but isn't), a URL must also
  parse with an http(s) scheme and a host, and a number mustn't overflow.
  The pattern is never looser than the server: what fails it, the server
  refuses too.
- `spam.min_seconds`: a post sooner than this after `_ts` is taken as spam
  (quietly: it's answered as if it worked). 0: off. `spam.captcha` names the
  provider, its site key, its script, and the field its token is posted in
  (`cf-turnstile-response` or `g-recaptcha-response`).

Post JSON (files as `{"filename", "content_type", "data": "<base64>"}`), a
urlencoded form, or `multipart/form-data` (for files; 10 MB in all). Send an
empty `_hp`, and `_ts` set to when the page loaded (ms since the epoch):

```js
const res = await fetch(`${CMS}/api/x/forms/submit?form=contact`, {
  method: "POST",
  headers: { "Content-Type": "application/json", Accept: "application/json" },
  body: JSON.stringify({ name, email, message, _hp: "", _ts: loadedAt }),
});
// 200 {"ok": true, "message": "Thanks!", "redirect": ""}
// 422 {"error": "invalid", "message": "Some fields need attention.", "errors": {"email": ["isn't an email address"]}}
// 410 {"error": "closed", "message": "This form isn't taking submissions."}
// 429 too many posts from one address
```

A plain HTML `<form method="post" action="…">` works without script: it's
answered with a redirect to the thank-you page, or back to the page with
`?form=<slug>&form_status=sent` (or `error`). The page it goes back to is
the hidden `_return` field (the page's absolute address) when that's on
one of the site's origins — the public site URL's, or one in Settings ›
General › Allowed origins, the list CORS uses — and the `Referer`
otherwise: a post can't send anyone to another site. The site's origin must be in
Settings › General › Allowed origins (or be the public site URL) for a
browser's `fetch` to be allowed.

Uploaded files go into the media library under `Form uploads/<slug>`. Like
every media file, they're served at an address that can't be guessed, but
isn't private: don't take documents on a form that mustn't be public.

## The hook

A form can send each submission to the site before it's kept and emailed,
for what only the site can do with it: create a quote in another system,
check an answer against it, work out where the visitor goes next. Set it in
the form's **Hook** tab: an address on the site (its public URL's origin, or
one of Settings › General › Allowed origins: the only hosts the plugin may
call) and a signing secret.

After a submission passes the form's checks and isn't spam, the CMS posts it:

```
POST <hook_url>
Content-Type: application/json
X-CMS-Form: quote
X-CMS-Signature: sha256=<hex HMAC-SHA256 of the body, keyed by the secret>

{"event": "submission.created", "form": "quote", "form_name": "Quote",
 "submission_id": "…", "submitted_at": "2026-…Z",
 "values": {"email": "ada@example.test", "service": "Move It", "to_zip": "32207"}}
```

and waits for its answer, up to 6 seconds:

- **2xx** `{"redirect": "/quote?q=Q-1", "message": "…", "values": {"quote_id": "Q-1"}}`,
  every key optional: the visitor goes to `redirect` (a path on the site, or
  an address on one of its origins) instead of the form's own thank-you, sees
  `message` instead of its own, and `values` are kept with the submission
  (shown with it, in the CSV, and as merge tags in its emails: `{{ quote_id }}`).
- **422** `{"message": "…", "errors": {"zip": ["isn't in our service area"]}}`:
  the visitor is refused with those errors, beside their fields, and nothing
  is kept or sent.
- Anything else (down, slow, another status): the submission goes through as
  the form says, and the failure is noted on it. A hook never loses one.

`contracts/forms/form-hook-request.json` pins what's posted, and
`@librepublish/astro` checks the signature for an Astro site
(`formHook`).

## Webhooks

Each submission that isn't spam is also sent to the webhooks that take
`submission.created` (Settings › Webhooks; a new webhook doesn't, since it
carries what people sent): `{form, form_name, submission_id, submitted_at,
values, hook}`, in the webhook envelope (`contracts/webhooks/submission.created.json`).
It goes out after the submission is kept, in the background, so it can't
change what the visitor sees: that's the hook's job.

## Submissions

**Forms › Submissions** lists them newest first: unread, read, spam, by
form. Opening one marks it read and shows how its emails (and its hook) went,
and what the hook added. **Export CSV** takes the current form and status,
with a column for each value a hook added; values a spreadsheet would run
as a formula are kept as text.

The routes the admin uses are the plugin's own (`/api/x/forms/forms`,
`/form`, `/submissions`, `/submission`, `/submissions.csv`, …), so the CLI
and agents can use them too, with the `forms:read` and `forms:write`
capabilities (Roles).
