// Package outbound is the HTTP client webhooks, deploys and plugins post
// with, and the check on where it may go.
package outbound

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Client is what webhooks and deploys post with. It reaches public
// addresses only, checked on the address actually dialed, so neither DNS
// nor a redirect gets anywhere else. Loopback and private networks are
// allowed when the operator says so (AllowPrivate, OUTBOUND_ALLOW_PRIVATE);
// link-local and the clouds' metadata services never are.
//
// It doesn't use HTTP(S)_PROXY: through a proxy the dialer sees only the
// proxy's address, and the check would be no check at all.
//
// A redirect is followed only on the same host, and never from https to
// http: custom headers and the signed body aren't resent anywhere the
// webhook didn't name.
var Client = &http.Client{
	Timeout:       15 * time.Second,
	Transport:     Transport,
	CheckRedirect: CheckRedirect,
}

// Transport is Client's: the dialer refuses what blocked says. Plugins'
// clients are built on it, so the same rules hold for them.
var Transport http.RoundTripper = &http.Transport{
	Proxy: nil,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   refuse,
	}).DialContext,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 15 * time.Second,
	MaxIdleConns:          10,
	IdleConnTimeout:       90 * time.Second,
}

// allowPrivate is OUTBOUND_ALLOW_PRIVATE: a site on the same box or
// network may be reached.
var allowPrivate atomic.Bool

// AllowPrivate sets whether loopback and private addresses may be reached
// (OUTBOUND_ALLOW_PRIVATE). Link-local and metadata addresses never may.
func AllowPrivate(on bool) { allowPrivate.Store(on) }

// CheckRedirect follows at most 5 redirects, each on the host first
// asked, never from https to http, and each to an address CheckURL allows.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	for _, v := range via {
		if v.URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("outbound: refused a redirect from https to http (%s)", req.URL.Redacted())
		}
	}
	if !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
		return fmt.Errorf("outbound: refused a redirect to another host (%s): use that address instead", req.URL.Host)
	}
	return CheckURL(req.URL.String())
}

// refuse is the dialer's last word on where a post may go.
func refuse(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("outbound: not an IP address: %q", host)
	}
	if blocked(ip) {
		return fmt.Errorf("outbound: %w", refusal(ip))
	}
	return nil
}

// refusal says why ip is refused, and, for a private address, how an
// operator allows it.
func refusal(ip netip.Addr) error {
	if !blockedWith(ip, true) {
		return fmt.Errorf("%s isn't an address webhooks and deploys may reach (a private one: OUTBOUND_ALLOW_PRIVATE=1 allows it)", ip)
	}
	return fmt.Errorf("%s isn't an address webhooks and deploys may reach", ip)
}

func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// never are refused whatever the operator says: "this host", link-local
// (169.254.169.254 is most clouds' metadata service), multicast, reserved
// and broadcast, and the metadata services that sit elsewhere (AWS's IPv6
// one, Alibaba's).
var never = prefixes(
	"0.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4",
	"fe80::/10", "ff00::/8",
	"fd00:ec2::254/128", "100.100.100.200/32",
)

// private are refused unless AllowPrivate: loopback, RFC 1918, carrier-grade
// NAT, unique-local and site-local IPv6, and NAT64 (which reaches whatever
// IPv4 address it embeds).
var private = prefixes(
	"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
	"::1/128", "fc00::/7", "fec0::/10", "64:ff9b::/96", "64:ff9b:1::/48",
)

func blocked(ip netip.Addr) bool { return blockedWith(ip, allowPrivate.Load()) }

func blockedWith(ip netip.Addr, allowPrivate bool) bool {
	ip = ip.Unmap().WithZone("")
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || in(never, ip) {
		return true
	}
	// An address that embeds an IPv4 one (NAT64, 6to4) goes where that does.
	if v4, ok := embedded(ip); ok && blockedWith(v4, allowPrivate) {
		return true
	}
	if allowPrivate {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || in(private, ip)
}

func in(list []netip.Prefix, ip netip.Addr) bool {
	for _, p := range list {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

var nat64, nat64Local, sixToFour = netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16")

// embedded is the IPv4 address a NAT64 (its last 32 bits) or 6to4 (bits
// 16-48) address stands for.
func embedded(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() {
		return netip.Addr{}, false
	}
	b := ip.As16()
	switch {
	case nat64.Contains(ip) || nat64Local.Contains(ip):
		return netip.AddrFrom4([4]byte(b[12:16])), true
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return netip.Addr{}, false
}

// CheckURL refuses what isn't http(s), and a literal address the
// dialer would refuse anyway (said early, when the URL is saved).
func CheckURL(u string) error {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return fmt.Errorf("only http(s) URLs: %q", u)
	}
	if parsed, err := url.Parse(u); err == nil {
		if ip, err := netip.ParseAddr(strings.Trim(parsed.Hostname(), "[]")); err == nil && blocked(ip) {
			return refusal(ip)
		}
	}
	return nil
}
