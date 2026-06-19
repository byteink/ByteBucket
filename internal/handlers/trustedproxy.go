package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// Config-bucket keys under which the runtime trusted-proxy settings are
// persisted, so an operator's choice survives a restart. The live values
// themselves live in the storage package (trustedproxy.go).
const (
	trustedProxyHeadersKey  = "trustedproxyheaders"
	trustedProxyLeftmostKey = "trustedproxyleftmost"
)

// maxTrustedProxyHeaders bounds the configured header list. A real deployment
// trusts one or two headers; the cap keeps a hostile or fat-fingered PUT from
// growing per-request resolution work without limit (Power-of-10: no unbounded
// allocation driven by input).
const maxTrustedProxyHeaders = 8

// trustedProxyHeaderRe constrains a header name to the RFC 7230 token subset we
// accept: letters, digits and hyphen. This is the chokepoint that keeps CRLF,
// spaces, path separators and other injection bytes out of a value that is later
// fed to http.Header lookups and echoed back to the admin UI.
var trustedProxyHeaderRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// trustedProxyDTO is the admin API wire shape for the trusted-proxy config.
type trustedProxyDTO struct {
	Headers       []string `json:"headers"`
	UseLeftmostIP bool     `json:"useLeftmostIP"`
}

func currentTrustedProxyDTO() trustedProxyDTO {
	cfg := storage.TrustedProxy()
	if cfg.Headers == nil {
		cfg.Headers = []string{}
	}
	return trustedProxyDTO{Headers: cfg.Headers, UseLeftmostIP: cfg.UseLeftmostIP}
}

// sanitizeTrustedProxyHeaders validates and de-duplicates the header list. It
// returns the cleaned list and true when every entry is a well-formed header
// token and the count is within bounds; otherwise false (reject, do not clamp,
// so the UI cannot silently store something it did not get back). An empty list
// is valid and means "trust no header".
func sanitizeTrustedProxyHeaders(in []string) ([]string, bool) {
	if len(in) > maxTrustedProxyHeaders {
		return nil, false
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, h := range in {
		h = strings.TrimSpace(h)
		if !trustedProxyHeaderRe.MatchString(h) {
			return nil, false
		}
		lower := strings.ToLower(h)
		if _, dup := seen[lower]; dup {
			continue
		}
		seen[lower] = struct{}{}
		out = append(out, h)
	}
	return out, true
}

// InitTrustedProxyFromStore applies any persisted trusted-proxy override over
// the env-seeded baseline at startup, so a UI change survives a restart. Returns
// the effective config for the caller to log.
func InitTrustedProxyFromStore() (trustedProxyDTO, error) {
	cfg := storage.TrustedProxy()
	if raw, err := storage.GetConfigValue(trustedProxyHeadersKey); err != nil {
		return trustedProxyDTO{}, err
	} else if raw != nil {
		var headers []string
		if json.Unmarshal(raw, &headers) == nil {
			if clean, ok := sanitizeTrustedProxyHeaders(headers); ok {
				cfg.Headers = clean
			}
		}
	}
	if raw, err := storage.GetConfigValue(trustedProxyLeftmostKey); err != nil {
		return trustedProxyDTO{}, err
	} else if raw != nil {
		cfg.UseLeftmostIP = string(raw) == "true"
	}
	storage.SetTrustedProxy(cfg)
	return currentTrustedProxyDTO(), nil
}

// GetTrustedProxyHandler returns the effective trusted-proxy config.
func GetTrustedProxyHandler(c *gin.Context) {
	c.JSON(http.StatusOK, currentTrustedProxyDTO())
}

// PutTrustedProxyHandler validates, persists and applies the trusted-proxy
// config live on both surfaces. Invalid header names are rejected (not clamped)
// so a malformed entry can never reach the request-path header lookup.
func PutTrustedProxyHandler(c *gin.Context) {
	var d trustedProxyDTO
	if err := c.ShouldBindJSON(&d); err != nil {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Invalid request body")
		return
	}
	headers, ok := sanitizeTrustedProxyHeaders(d.Headers)
	if !ok {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Invalid trusted-proxy header list")
		return
	}

	blob, err := json.Marshal(headers)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to encode trusted-proxy headers")
		return
	}
	leftmost := "false"
	if d.UseLeftmostIP {
		leftmost = "true"
	}
	for k, v := range map[string][]byte{
		trustedProxyHeadersKey:  blob,
		trustedProxyLeftmostKey: []byte(leftmost),
	} {
		if err := storage.PutConfigValue(k, v); err != nil {
			respondError(c, http.StatusInternalServerError, "InternalError", "Failed to persist trusted-proxy setting")
			return
		}
	}
	storage.SetTrustedProxy(storage.TrustedProxyConfig{Headers: headers, UseLeftmostIP: d.UseLeftmostIP})
	recordAudit(c, "config.trustedproxy", strings.Join(headers, ","), "")
	c.JSON(http.StatusOK, currentTrustedProxyDTO())
}
