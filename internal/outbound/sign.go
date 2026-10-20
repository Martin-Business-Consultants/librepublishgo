package outbound

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"
)

// The headers a signed post carries. Signature is the original one, over
// the body alone, kept as it was for receivers that check it. SignatureV2
// is over "<Timestamp>.<body>", so a receiver that checks it and the
// timestamp's age can refuse a post replayed later; Delivery names the
// delivery (the same on each retry of it), for a receiver to drop repeats.
const (
	SignatureHeader   = "X-CMS-Signature"
	SignatureV2Header = "X-CMS-Signature-V2"
	TimestampHeader   = "X-CMS-Timestamp"
	DeliveryHeader    = "X-CMS-Delivery"
)

// Sign sets h's signature headers for body, signed with secret: the
// timestamp is now (Unix seconds), id the delivery's.
func Sign(h http.Header, secret string, body []byte, id string) {
	SignAt(h, secret, body, id, time.Now())
}

// SignAt is Sign at a given time (tests).
func SignAt(h http.Header, secret string, body []byte, id string, at time.Time) {
	ts := strconv.FormatInt(at.Unix(), 10)
	h.Set(SignatureHeader, "sha256="+mac(secret, body))
	h.Set(SignatureV2Header, "sha256="+mac(secret, []byte(ts+"."), body))
	h.Set(TimestampHeader, ts)
	if id != "" {
		h.Set(DeliveryHeader, id)
	}
}

func mac(secret string, parts ...[]byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	for _, p := range parts {
		m.Write(p)
	}
	return hex.EncodeToString(m.Sum(nil))
}

// NewDeliveryID is a random id for a delivery: 32 hex characters.
func NewDeliveryID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails
	return hex.EncodeToString(b)
}
