package handlers

import (
	"net/http"

	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// whoAmIDTO reports how the server resolves the *current* request's client IP,
// alongside the raw signals that fed the decision. The admin Settings page reads
// it to validate a trusted-proxy setup: an operator can see at a glance whether
// the resolved IP is their real address (good) or the proxy's (needs a header
// configured), and whether the configured header matches the detected one.
type whoAmIDTO struct {
	IP             string   `json:"ip"`
	RemoteAddr     string   `json:"remoteAddr"`
	ForwardedFor   string   `json:"forwardedFor"`
	DetectedHeader string   `json:"detectedHeader"`
	TrustedHeaders []string `json:"trustedHeaders"`
	UseLeftmostIP  bool     `json:"useLeftmostIP"`
}

// GetWhoAmIHandler returns the resolved client IP for this request plus the raw
// inputs, so the trusted-proxy configuration can be validated live.
func GetWhoAmIHandler(c *gin.Context) {
	cfg := storage.TrustedProxy()
	headers := cfg.Headers
	if headers == nil {
		headers = []string{}
	}
	c.JSON(http.StatusOK, whoAmIDTO{
		IP:             middleware.ResolveClientIP(c.Request),
		RemoteAddr:     middleware.RemoteIP(c.Request),
		ForwardedFor:   c.GetHeader("X-Forwarded-For"),
		DetectedHeader: middleware.DetectProxyHeader(c.Request, cfg.Headers),
		TrustedHeaders: headers,
		UseLeftmostIP:  cfg.UseLeftmostIP,
	})
}
