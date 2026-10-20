package forms

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/mail"
	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
)

// call is one call of the plugin: what the host hands it, and the request
// (empty outside a route). What touches the host is a method on it.
type call struct {
	ctx context.Context //nolint:containedctx // a call lives as long as its context
	api *plugin.API
	req request
}

// handle is a route's handler, with a call made for it.
func handle(fn func(c *call) *plugin.Response) plugin.Handler {
	return func(ctx context.Context, api *plugin.API, req *plugin.Request) (*plugin.Response, error) {
		return fn(&call{ctx: ctx, api: api, req: request{req}}), nil
	}
}

func (c *call) kvGet(key string) (string, bool) {
	v, ok, err := c.api.KVGet(c.ctx, key)
	return v, ok && err == nil
}

func (c *call) kvGetJSON(key string, v any) bool {
	s, ok := c.kvGet(key)
	return ok && json.Unmarshal([]byte(s), v) == nil
}

// kvSet answers "" when it's stored, or why it isn't.
func (c *call) kvSet(key, value string) string {
	if err := c.api.KVSet(c.ctx, key, value); err != nil {
		return err.Error()
	}
	return ""
}

func (c *call) kvSetJSON(key string, v any) string {
	b, _ := json.Marshal(v)
	return c.kvSet(key, string(b))
}

func (c *call) kvDelete(key string) {
	if err := c.api.KVDelete(c.ctx, key); err != nil {
		c.logf("deleting " + key + ": " + err.Error())
	}
}

// kvKeys lists keys under a prefix, a page at a time: the keys and the
// cursor for the next page ("" at the end).
func (c *call) kvKeys(prefix, after string, limit int) ([]string, string) {
	keys, next, _ := c.api.KVList(c.ctx, prefix, after, limit)
	return keys, next
}

// kvAll is every key under a prefix.
func (c *call) kvAll(prefix string) []string {
	keys, _ := c.api.KVAll(c.ctx, prefix)
	return keys
}

func (c *call) siteInfo() plugin.Site {
	s, _ := c.api.Site(c.ctx)
	return s
}

// sendMail answers "" when the host took the message, or why it didn't.
func (c *call) sendMail(m mail.Message) string {
	if err := c.api.Mail(c.ctx, m); err != nil {
		return err.Error()
	}
	return ""
}

// sendVisitorMail is sendMail for a visitor's confirmation (MailVisitor).
func (c *call) sendVisitorMail(m mail.Message) string {
	if err := c.api.MailVisitor(c.ctx, m); err != nil {
		return err.Error()
	}
	return ""
}

type upload struct {
	ID       int64  `json:"id"`
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Type     string `json:"content_type"`
	Size     int64  `json:"byte_size"`
}

// putMedia puts a posted file in the media library; "" when it did, or why
// it didn't.
func (c *call) putMedia(pf postedFile, folder string) (upload, string) {
	data := pf.raw
	if data == nil {
		var err error
		if data, err = base64.StdEncoding.DecodeString(pf.Data); err != nil {
			return upload{}, "data must be base64"
		}
	}
	asset, err := c.api.PutMedia(c.ctx, plugin.Media{Filename: pf.Filename, ContentType: pf.ContentType, Data: data, Folder: folder})
	if err != nil {
		return upload{}, err.Error()
	}
	var u upload
	b, _ := json.Marshal(asset)
	if err := json.Unmarshal(b, &u); err != nil {
		return upload{}, "the media library's answer isn't readable"
	}
	return u, ""
}

func (c *call) logf(msg string) { c.api.Log(msg) }

// ---- requests and answers -------------------------------------------------

type request struct{ *plugin.Request }

func (r request) q(name string) string { return r.Query.Get(name) }

func (r request) header(name string) string { return r.Header(name) }

func reply(status int, v any) *plugin.Response { return plugin.JSON(status, v) }

func replyRaw(status int, headers map[string]string, body string) *plugin.Response {
	return &plugin.Response{Status: status, Headers: headers, Body: []byte(body)}
}

// invalid answers 422 the way the core does, so the admin marks the fields.
func invalid(errs map[string][]string) *plugin.Response {
	return reply(422, map[string]any{"error": "invalid", "message": "Validation failed", "errors": errs})
}

func notFound(msg string) *plugin.Response {
	return reply(404, map[string]string{"error": "not_found", "message": msg})
}
