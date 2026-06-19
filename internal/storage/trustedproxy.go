package storage

import "sync/atomic"

// TrustedProxyConfig declares which request headers may be trusted to carry the
// real client IP when this server sits behind one or more reverse proxies, and
// whether to read the leftmost or rightmost address from a multi-value header
// such as X-Forwarded-For. Empty Headers means "trust no header": the socket
// peer (RemoteAddr) is the only source, the safe default for a directly-exposed
// server where any forwarding header is attacker-supplied.
//
// The live value lives here, not in the rate-limit controller, because client-IP
// resolution is a server-wide concern shared by every consumer (rate limiter,
// access log, request log) — they must all agree on who the client is.
type TrustedProxyConfig struct {
	Headers       []string
	UseLeftmostIP bool
}

// trustedProxy holds the live config behind an atomic pointer so request-path
// reads never lock and an admin update is visible immediately on both surfaces.
// A nil pointer (never set) reads as the empty config.
var trustedProxy atomic.Pointer[TrustedProxyConfig]

// TrustedProxy returns a copy of the live trusted-proxy config. The Headers
// slice is copied so a caller cannot mutate the shared value.
func TrustedProxy() TrustedProxyConfig {
	p := trustedProxy.Load()
	if p == nil {
		return TrustedProxyConfig{}
	}
	return TrustedProxyConfig{
		Headers:       append([]string(nil), p.Headers...),
		UseLeftmostIP: p.UseLeftmostIP,
	}
}

// SetTrustedProxy replaces the live config. The Headers slice is copied so later
// mutation of the caller's slice cannot race a request-path read.
func SetTrustedProxy(cfg TrustedProxyConfig) {
	stored := TrustedProxyConfig{
		Headers:       append([]string(nil), cfg.Headers...),
		UseLeftmostIP: cfg.UseLeftmostIP,
	}
	trustedProxy.Store(&stored)
}
