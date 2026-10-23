package fuse

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/plugins/fuse/engine"
)

// host is what the plugin reaches of the CMS: *plugin.API, or a fake in
// the tests.
type host interface {
	Key() string
	Log(msg string)
	Site(ctx context.Context) (plugin.Site, error)
	Seal(plain string) string
	Reveal(v string) string
	HTTP() *http.Client
	Emit(ctx context.Context, event string, data any) error
	PutMedia(ctx context.Context, f plugin.Media) (map[string]any, error)
	KVGet(ctx context.Context, key string) (string, bool, error)
	KVSet(ctx context.Context, key, value string) error
	KVAdd(ctx context.Context, key string, delta int64) (int64, error)
	KVDelete(ctx context.Context, key string) error
	KVList(ctx context.Context, prefix, after string, limit int) ([]string, string, error)
	KVAll(ctx context.Context, prefix string) ([]string, error)
}

var _ host = (*plugin.API)(nil)

// How the rate book sits in storage (KV, ≤ 1 MB a value):
//
//	rates:draft              the book being edited, a blob (zips included)
//	rates:draft:meta         who changed it last, and what it came from
//	rates:active             {version, …}: the published book requests price from
//	rates:versions           the published versions, newest first (the last 10 kept)
//	rates:v:<ver>:core       a version's book without its ZIPs, a blob
//	rates:v:<ver>:zips:<p3>  its ZIPs under one 3-digit prefix, a blob
//	rates:v:<ver>:meta       its prefixes and counts
//
// A blob is JSON cut into parts of at most blobPart bytes (<key>#0, #1, …)
// under a head (<key>) saying how many and their SHA-256, so no value comes
// near the 1 MB cap whatever the book's size. A version is written whole
// before rates:active names it, and never changed after, so a request never
// sees a half-published book.
const (
	keyDraft     = "rates:draft"
	keyDraftMeta = "rates:draft:meta"
	keyActive    = "rates:active"
	keyVersions  = "rates:versions"
	keepVersions = 10
	blobPart     = 900 << 10
)

type blobHead struct {
	Parts  int    `json:"parts"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

func putBlob(ctx context.Context, h host, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	head := blobHead{Size: len(b), SHA256: hex.EncodeToString(sum[:])}
	for i := 0; i == 0 || i*blobPart < len(b); i++ {
		end := min((i+1)*blobPart, len(b))
		if err := h.KVSet(ctx, fmt.Sprintf("%s#%d", key, i), string(b[i*blobPart:end])); err != nil {
			return err
		}
		head.Parts = i + 1
	}
	// Parts a bigger blob left behind go too.
	for i := head.Parts; ; i++ {
		k := fmt.Sprintf("%s#%d", key, i)
		if _, ok, _ := h.KVGet(ctx, k); !ok {
			break
		}
		_ = h.KVDelete(ctx, k)
	}
	hb, _ := json.Marshal(head)
	return h.KVSet(ctx, key, string(hb))
}

// getBlob reads a blob into v; false when there's none.
func getBlob(ctx context.Context, h host, key string, v any) (bool, error) {
	raw, ok, err := h.KVGet(ctx, key)
	if err != nil || !ok {
		return false, err
	}
	var head blobHead
	if err := json.Unmarshal([]byte(raw), &head); err != nil || head.Parts == 0 {
		return false, fmt.Errorf("%s isn't a blob", key)
	}
	var sb strings.Builder
	for i := range head.Parts {
		part, ok, err := h.KVGet(ctx, fmt.Sprintf("%s#%d", key, i))
		if err != nil {
			return false, err
		}
		if !ok {
			return false, fmt.Errorf("%s is missing part %d", key, i)
		}
		sb.WriteString(part)
	}
	b := []byte(sb.String())
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != head.SHA256 {
		return false, fmt.Errorf("%s doesn't match its checksum", key)
	}
	return true, json.Unmarshal(b, v)
}

func deleteBlob(ctx context.Context, h host, key string) {
	keys, _ := h.KVAll(ctx, key+"#")
	for _, k := range keys {
		_ = h.KVDelete(ctx, k)
	}
	_ = h.KVDelete(ctx, key)
}

func getJSON(ctx context.Context, h host, key string, v any) bool {
	s, ok, err := h.KVGet(ctx, key)
	return err == nil && ok && json.Unmarshal([]byte(s), v) == nil
}

func setJSON(ctx context.Context, h host, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return h.KVSet(ctx, key, string(b))
}

// Active is the published version requests price from.
type Active struct {
	Version     string         `json:"version"`
	PublishedAt string         `json:"published_at"`
	By          string         `json:"by"`
	Digest      string         `json:"digest"`
	Counts      map[string]int `json:"counts"`
	Source      string         `json:"source,omitempty"` // "fuse:<tenant> exported <at>", when it came from an import
}

// DraftMeta says where the draft stands.
type DraftMeta struct {
	UpdatedAt   string `json:"updated_at"`
	By          string `json:"by"`
	BaseVersion string `json:"base_version"` // the version it was started from
	Source      string `json:"source,omitempty"`
}

type versionMeta struct {
	Prefixes []string       `json:"prefixes"`
	AnyZips  bool           `json:"any_zips"`
	Counts   map[string]int `json:"counts"`
}

// loadDraft is the draft book: the one stored, else a copy of the active
// version, else an empty book.
func loadDraft(ctx context.Context, h host) (*engine.Book, DraftMeta, error) {
	var b engine.Book
	var meta DraftMeta
	ok, err := getBlob(ctx, h, keyDraft, &b)
	if err != nil {
		return nil, meta, err
	}
	getJSON(ctx, h, keyDraftMeta, &meta)
	if ok {
		return &b, meta, nil
	}
	if v, err := loadActive(ctx, h); err == nil && v != nil {
		full, err := v.fullBook(ctx, h)
		if err != nil {
			return nil, meta, err
		}
		return full, DraftMeta{BaseVersion: v.active.Version, Source: v.active.Source}, nil
	}
	return emptyBook(), meta, nil
}

func emptyBook() *engine.Book {
	return &engine.Book{ServiceTypes: []engine.ServiceType{}, ProductTypes: []engine.ProductType{}, Discounts: []engine.Discount{},
		Locations: []engine.Location{}, Zones: []engine.Zone{}, MileageTiers: []engine.Tier{}, Fees: []engine.Fee{},
		Products: []engine.Product{}, ZipCodes: []engine.ZipCode{}}
}

func saveDraft(ctx context.Context, h host, b *engine.Book, by, source string) error {
	_, meta, _ := loadDraftMeta(ctx, h)
	if err := putBlob(ctx, h, keyDraft, b); err != nil {
		return err
	}
	if meta.BaseVersion == "" {
		var a Active
		if getJSON(ctx, h, keyActive, &a) {
			meta.BaseVersion = a.Version
		}
	}
	meta.UpdatedAt, meta.By = now().Format(time.RFC3339), by
	if source != "" {
		meta.Source = source
	}
	return setJSON(ctx, h, keyDraftMeta, meta)
}

func loadDraftMeta(ctx context.Context, h host) (bool, DraftMeta, error) {
	var meta DraftMeta
	_, ok, err := h.KVGet(ctx, keyDraft)
	getJSON(ctx, h, keyDraftMeta, &meta)
	return ok, meta, err
}

func discardDraft(ctx context.Context, h host) {
	deleteBlob(ctx, h, keyDraft)
	_ = h.KVDelete(ctx, keyDraftMeta)
}

// version is a published book, loaded: its index (core) and its ZIPs, read
// a shard at a time as quotes need them. Versions never change, so one is
// kept in memory for as long as it's active.
type version struct {
	active Active
	meta   versionMeta
	index  *engine.Index

	mu     sync.Mutex
	shards map[string]*engine.MapZips
}

// versions is the loaded versions, by site and version: one process can
// serve several sites (the tests do).
var versions sync.Map

func versionKey(ctx context.Context, h host, ver string) string {
	s, _ := h.Site(ctx)
	return s.CMSURL + "\x00" + ver
}

// loadActive is the active version, loaded; nil when nothing is published.
func loadActive(ctx context.Context, h host) (*version, error) {
	var a Active
	if !getJSON(ctx, h, keyActive, &a) || a.Version == "" {
		return nil, nil
	}
	return loadVersion(ctx, h, a)
}

func loadVersion(ctx context.Context, h host, a Active) (*version, error) {
	ck := versionKey(ctx, h, a.Version)
	if v, ok := versions.Load(ck); ok {
		return v.(*version), nil
	}
	var core engine.Book
	ok, err := getBlob(ctx, h, "rates:v:"+a.Version+":core", &core)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("version %s isn't in storage", a.Version)
	}
	v := &version{active: a, shards: map[string]*engine.MapZips{}}
	getJSON(ctx, h, "rates:v:"+a.Version+":meta", &v.meta)
	if v.index, err = engine.NewIndex(&core); err != nil {
		return nil, err
	}
	actual, _ := versions.LoadOrStore(ck, v)
	return actual.(*version), nil
}

// shard is the ZIPs under a 3-digit prefix.
func (v *version) shard(ctx context.Context, h host, p3 string) *engine.MapZips {
	v.mu.Lock()
	s, ok := v.shards[p3]
	v.mu.Unlock()
	if ok {
		return s
	}
	var zips []engine.ZipCode
	if slices.Contains(v.meta.Prefixes, p3) {
		if _, err := getBlob(ctx, h, "rates:v:"+v.active.Version+":zips:"+p3, &zips); err != nil {
			h.Log("reading ZIPs " + p3 + ": " + err.Error())
			return engine.NewMapZips(nil) // not kept: the next request tries again
		}
	}
	s = engine.NewMapZips(zips)
	v.mu.Lock()
	if len(v.shards) > 2000 {
		v.shards = map[string]*engine.MapZips{}
	}
	v.shards[p3] = s
	v.mu.Unlock()
	return s
}

// zips is the version's ZIP table for one call, as engine.ZipSource.
func (v *version) zips(ctx context.Context, h host) engine.ZipSource {
	return &shardZips{v: v, ctx: ctx, h: h}
}

type shardZips struct {
	v   *version
	ctx context.Context //nolint:containedctx // lives for one call
	h   host
}

func (s *shardZips) of(code string) *engine.MapZips {
	if len(code) < 3 {
		return engine.NewMapZips(nil)
	}
	return s.v.shard(s.ctx, s.h, code[:3])
}

func (s *shardZips) Zip(code string) *engine.ZipCode { return s.of(code).Zip(code) }
func (s *shardZips) Any() bool                       { return s.v.meta.AnyZips }
func (s *shardZips) Served(zip5 string) bool         { return s.of(zip5).Served(zip5) }

// fullBook is the version with all its ZIPs, for the draft and the admin.
func (v *version) fullBook(ctx context.Context, h host) (*engine.Book, error) {
	b := *v.index.Book
	b.ZipCodes = []engine.ZipCode{}
	for _, p := range v.meta.Prefixes {
		var zips []engine.ZipCode
		if _, err := getBlob(ctx, h, "rates:v:"+v.active.Version+":zips:"+p, &zips); err != nil {
			return nil, err
		}
		b.ZipCodes = append(b.ZipCodes, zips...)
	}
	slices.SortStableFunc(b.ZipCodes, func(a, c engine.ZipCode) int { return cmp64(a.ID, c.ID) })
	return clone(&b), nil
}

func cmp64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// clone is a deep copy of a book, so an edit never reaches a loaded version.
func clone(b *engine.Book) *engine.Book {
	raw, _ := json.Marshal(b)
	var out engine.Book
	_ = json.Unmarshal(raw, &out)
	return &out
}

// publish makes the draft the active version: checked, written whole under
// a new version, then named by rates:active in one write.
func publish(ctx context.Context, h host, by string) (*Active, error) {
	draft, meta, err := loadDraft(ctx, h)
	if err != nil {
		return nil, err
	}
	if errs := engine.Validate(draft); len(errs) > 0 {
		return nil, &invalidBook{errs}
	}
	if _, err := engine.NewIndex(draft); err != nil {
		return nil, &invalidBook{map[string][]string{"book": {err.Error()}}}
	}
	ver := newVersionID()
	core := *draft
	core.ZipCodes = nil
	if err := putBlob(ctx, h, "rates:v:"+ver+":core", &core); err != nil {
		return nil, err
	}
	shards := map[string][]engine.ZipCode{}
	for _, z := range draft.ZipCodes {
		p := z.PostalCode[:3]
		shards[p] = append(shards[p], z)
	}
	vm := versionMeta{AnyZips: len(draft.ZipCodes) > 0, Counts: counts(draft)}
	for p, zips := range shards {
		if err := putBlob(ctx, h, "rates:v:"+ver+":zips:"+p, zips); err != nil {
			return nil, err
		}
		vm.Prefixes = append(vm.Prefixes, p)
	}
	slices.Sort(vm.Prefixes)
	if err := setJSON(ctx, h, "rates:v:"+ver+":meta", vm); err != nil {
		return nil, err
	}
	digest, _ := json.Marshal(draft)
	sum := sha256.Sum256(digest)
	a := Active{Version: ver, PublishedAt: now().Format(time.RFC3339), By: by, Digest: hex.EncodeToString(sum[:8]), Counts: vm.Counts, Source: meta.Source}
	if err := setJSON(ctx, h, keyActive, a); err != nil {
		return nil, err
	}
	var list []Active
	getJSON(ctx, h, keyVersions, &list)
	list = append([]Active{a}, list...)
	if len(list) > keepVersions {
		for _, old := range list[keepVersions:] {
			keys, _ := h.KVAll(ctx, "rates:v:"+old.Version+":")
			for _, k := range keys {
				_ = h.KVDelete(ctx, k)
			}
		}
		list = list[:keepVersions]
	}
	if err := setJSON(ctx, h, keyVersions, list); err != nil {
		return nil, err
	}
	discardDraft(ctx, h)
	if err := h.Emit(ctx, "fuse.rates_published", map[string]any{"version": ver, "published_at": a.PublishedAt, "by": by}); err != nil {
		h.Log("emitting fuse.rates_published: " + err.Error())
	}
	return &a, nil
}

// rollback points rates:active at an older version, still kept.
func rollback(ctx context.Context, h host, ver, by string) (*Active, error) {
	var list []Active
	getJSON(ctx, h, keyVersions, &list)
	i := slices.IndexFunc(list, func(a Active) bool { return a.Version == ver })
	if i < 0 {
		return nil, errors.New("no kept version " + ver)
	}
	a := list[i]
	if _, err := loadVersion(ctx, h, a); err != nil {
		return nil, err
	}
	a.PublishedAt, a.By = now().Format(time.RFC3339), by+" (rolled back)"
	if err := setJSON(ctx, h, keyActive, a); err != nil {
		return nil, err
	}
	_ = h.Emit(ctx, "fuse.rates_published", map[string]any{"version": ver, "published_at": a.PublishedAt, "by": a.By, "rollback": true})
	return &a, nil
}

func counts(b *engine.Book) map[string]int {
	return map[string]int{"zips": len(b.ZipCodes), "fees": len(b.Fees), "products": len(b.Products), "zones": len(b.Zones),
		"locations": len(b.Locations), "discounts": len(b.Discounts), "service_types": len(b.ServiceTypes),
		"product_types": len(b.ProductTypes), "mileage_tiers": len(b.MileageTiers)}
}

func newVersionID() string {
	var r [3]byte
	_, _ = rand.Read(r[:])
	return now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(r[:])
}

// now is the clock, a variable for the tests.
var now = time.Now

type invalidBook struct{ errs map[string][]string }

func (e *invalidBook) Error() string {
	var parts []string
	for k, v := range e.errs {
		parts = append(parts, k+": "+strings.Join(v, ", "))
	}
	slices.Sort(parts)
	return "the book doesn't hold up: " + strings.Join(parts, "; ")
}
