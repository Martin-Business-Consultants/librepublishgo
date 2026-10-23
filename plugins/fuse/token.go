package fuse

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A quote token is a quote with nothing stored: the inputs and the figures
// as they were priced, signed, so the link a customer is sent carries its
// own quote and anyone holding the plugin's key can tell it's genuine.
//
//	v1.<base64url(payload JSON)>.<base64url(HMAC-SHA256(key, "v1." + payload part)[:16])>
//
// The key is 32 random bytes, sealed (api.Seal) in cfg:token_keys under a
// key id the payload names, so a new key can be rotated in while links
// signed with the old one still verify. The payload holds no email, phone
// or last name — only a first name — and isn't encrypted: a site may read
// it with no CMS (atob) to show the quoted figures when the CMS is down.
const (
	keyTokenKeys = "cfg:token_keys" //nolint:gosec // the name of a storage key, not a credential
	tokenPrefix  = "v1."
	macBytes     = 16
	maxToken     = 8 << 10
)

type tokenKeys struct {
	Active string            `json:"active"`
	Keys   map[string]string `json:"keys"` // kid → sealed hex key
}

// TokenPayload is what a token says.
type TokenPayload struct {
	Kid string    `json:"kid"`
	Iat int64     `json:"iat"`          // when it was priced (Unix seconds)
	RV  string    `json:"rv"`           // the rates version it was priced from
	PT  string    `json:"pt"`           // product type
	ST  string    `json:"st"`           // service type
	Z   string    `json:"z"`            // ZIP
	DZ  string    `json:"dz,omitempty"` // destination ZIP
	PID int64     `json:"pid,omitempty"`
	Q   int64     `json:"q,omitempty"`
	FN  string    `json:"fn,omitempty"` // first name, nothing more
	Fig TokenFigs `json:"fig"`
	VU  string    `json:"vu,omitempty"` // valid until (quote_validity_days), YYYY-MM-DD
}

// TokenFigs are the selected service's figures, in cents.
type TokenFigs struct {
	Products []TokenProduct `json:"products"`
	Fees     []TokenFee     `json:"fees"`
	Sub      int64          `json:"sub"`
	FT       int64          `json:"ft"`   // fees_total
	TD       int64          `json:"td"`   // total_discount
	Tot      int64          `json:"tot"`  // total
	Dist     string         `json:"dist"` // the distance status it was priced with
}

// TokenProduct is a product line as a token carries it.
type TokenProduct struct {
	N string `json:"n"` // name
	Q int64  `json:"q"` // quantity
	O int64  `json:"o"` // original price
	S int64  `json:"s"` // subtotal
}

// TokenFee is a fee line as a token carries it, its flags as 0 or 1.
type TokenFee struct {
	N  string `json:"n"`
	A  int64  `json:"a"`
	R  int    `json:"r,omitempty"`  // recurring
	SA int    `json:"sa,omitempty"` // starting at
	X  int    `json:"x,omitempty"`  // excluded from the total
	PQ int    `json:"pq,omitempty"` // per quantity
}

func loadTokenKeys(ctx context.Context, h host) tokenKeys {
	var k tokenKeys
	getJSON(ctx, h, keyTokenKeys, &k)
	if k.Keys == nil {
		k.Keys = map[string]string{}
	}
	return k
}

// signingKey is the active key, made on first use.
func signingKey(ctx context.Context, h host) (string, []byte, error) {
	k := loadTokenKeys(ctx, h)
	if k.Active != "" {
		if raw, err := hex.DecodeString(h.Reveal(k.Keys[k.Active])); err == nil && len(raw) == 32 {
			return k.Active, raw, nil
		}
	}
	return rotateTokenKey(ctx, h)
}

// rotateTokenKey adds a new key and makes it the one tokens are signed
// with; the old ones still verify.
func rotateTokenKey(ctx context.Context, h host) (string, []byte, error) {
	k := loadTokenKeys(ctx, h)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	kid := fmt.Sprintf("k%d", len(k.Keys)+1)
	for k.Keys[kid] != "" {
		kid += "x"
	}
	k.Keys[kid] = h.Seal(hex.EncodeToString(raw))
	k.Active = kid
	if err := setJSON(ctx, h, keyTokenKeys, k); err != nil {
		return "", nil, err
	}
	return kid, raw, nil
}

func mac(key []byte, signed string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(signed))
	return m.Sum(nil)[:macBytes]
}

// mintToken signs p with the active key.
func mintToken(ctx context.Context, h host, p TokenPayload) (string, error) {
	kid, key, err := signingKey(ctx, h)
	if err != nil {
		return "", err
	}
	p.Kid = kid
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	signed := tokenPrefix + base64.RawURLEncoding.EncodeToString(body)
	return signed + "." + base64.RawURLEncoding.EncodeToString(mac(key, signed)), nil
}

var (
	errTokenShape   = errors.New("not a quote token")
	errTokenKey     = errors.New("signed with a key this site doesn't have")
	errTokenMAC     = errors.New("the signature doesn't match: the token was changed")
	errTokenExpired = errors.New("the quote's valid-until date has passed")
)

// tokenReason is why a token can't be trusted, as /quote answers it.
func tokenReason(err error) string {
	switch {
	case errors.Is(err, errTokenKey):
		return "unknown_key"
	case errors.Is(err, errTokenMAC):
		return "bad_signature"
	case errors.Is(err, errTokenExpired):
		return "expired"
	}
	return "malformed"
}

// tokenToday is the date a token's vu is held to. A plugin isn't told the
// site's time zone, so it's UTC's, the same calendar vu is written in.
func tokenToday() string { return now().UTC().Format(time.DateOnly) }

// decodeToken reads a token's payload without checking it (the figures
// can't be trusted until verifyToken says so).
func decodeToken(t string) (TokenPayload, string, []byte, error) {
	var p TokenPayload
	if len(t) > maxToken || !strings.HasPrefix(t, tokenPrefix) {
		return p, "", nil, errTokenShape
	}
	dot := strings.LastIndexByte(t, '.')
	if dot <= len(tokenPrefix) {
		return p, "", nil, errTokenShape
	}
	signed, sig := t[:dot], t[dot+1:]
	body, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(signed, tokenPrefix))
	if err != nil {
		return p, "", nil, errTokenShape
	}
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(mac) != macBytes {
		return p, "", nil, errTokenShape
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, "", nil, errTokenShape
	}
	return p, signed, mac, nil
}

// verifyToken checks a token's signature, by its key id, then its
// valid-until date (vu, when set: the quote holds through that day). The
// payload comes back either way when it reads, for the admin's checker,
// with the error saying why it can't be trusted.
func verifyToken(ctx context.Context, h host, t string) (TokenPayload, error) {
	p, signed, got, err := decodeToken(t)
	if err != nil {
		return p, err
	}
	k := loadTokenKeys(ctx, h)
	sealed, ok := k.Keys[p.Kid]
	if !ok {
		return p, errTokenKey
	}
	key, err := hex.DecodeString(h.Reveal(sealed))
	if err != nil || len(key) != 32 {
		return p, errTokenKey
	}
	if !hmac.Equal(got, mac(key, signed)) {
		return p, errTokenMAC
	}
	if p.VU != "" {
		if _, err := time.Parse(time.DateOnly, p.VU); err != nil {
			return p, errTokenShape
		}
		if p.VU < tokenToday() {
			return p, errTokenExpired
		}
	}
	return p, nil
}
