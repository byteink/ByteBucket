package middleware

import (
	"net"
	"net/http"

	"ByteBucket/internal/storage"
)

// proxyHeaderFallbacks are the well-known vendor/CDN headers the whoami
// validation view probes (after any explicitly-configured headers) to guess
// which header a deployment actually sits behind, so the UI can flag a
// configured-vs-detected mismatch. Order is most-specific first.
var proxyHeaderFallbacks = []string{"CF-Connecting-IP", "Fly-Client-IP", "True-Client-IP", "X-Real-IP", "X-Forwarded-For"}

// ResolveClientIP returns the real client IP for r per the live trusted-proxy
// config. Every IP consumer (rate limiter, access log, request log) goes through
// here so they agree on the client identity from one source of truth.
func ResolveClientIP(r *http.Request) string {
	cfg := storage.TrustedProxy()
	return resolveClientIP(r, cfg.Headers, cfg.UseLeftmostIP)
}

// resolveClientIP mirrors PocketBase's RealIP: for each trusted header in order,
// read its last instance, split the comma list, and return the first valid IP
// scanning from the configured end — leftmost when useLeftmost, otherwise
// rightmost. The rightmost entry is the address the nearest trusted proxy wrote
// and a client cannot forge it by prepending, which is why rightmost is the safe
// default. Falls back to the socket peer when no configured header yields a valid
// address, so an unset or spoofed header can never override the truth.
func resolveClientIP(r *http.Request, headers []string, useLeftmost bool) string {
	for _, h := range headers {
		values := r.Header.Values(h)
		if len(values) == 0 {
			continue
		}
		parts := splitTrim(values[len(values)-1])
		if ip := pickIP(parts, useLeftmost); ip != "" {
			return ip
		}
	}
	return remoteAddrIP(r)
}

// pickIP returns the first parseable IP scanning parts from the requested end,
// or "" when none parse. Bounded by len(parts), itself bounded by the header
// size net/http caps via MaxHeaderBytes.
func pickIP(parts []string, useLeftmost bool) string {
	if useLeftmost {
		for i := 0; i < len(parts); i++ {
			if ip := net.ParseIP(parts[i]); ip != nil {
				return ip.String()
			}
		}
		return ""
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if ip := net.ParseIP(parts[i]); ip != nil {
			return ip.String()
		}
	}
	return ""
}

// DetectProxyHeader reports the first proxy header actually present on r,
// checking the configured headers first and then the well-known vendor headers.
// Used only by the whoami validation view; "" means no proxy header was seen, so
// the deployment appears to sit directly on the network.
func DetectProxyHeader(r *http.Request, configured []string) string {
	for _, h := range configured {
		if r.Header.Get(h) != "" {
			return h
		}
	}
	for _, h := range proxyHeaderFallbacks {
		if r.Header.Get(h) != "" {
			return h
		}
	}
	return ""
}

// RemoteIP returns the bare socket-peer IP (no port) for r. Exposed for the
// whoami view so the admin can compare the raw peer against the resolved IP and
// see whether their trusted-header configuration is taking effect.
func RemoteIP(r *http.Request) string {
	return remoteAddrIP(r)
}
