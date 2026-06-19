package handlers

import (
	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"
	"encoding/json"
	"math"
	"net/http"

	"github.com/gin-gonic/gin"
)

// rateLimitConfigKey is the Config-bucket key under which the runtime override
// is persisted. Absent key means "no override": the environment baseline wins.
const rateLimitConfigKey = "ratelimit"

// Validation bounds for an operator-supplied override. They are generous
// enough never to constrain a real deployment but reject absurd or hostile
// input (negatives, NaN/Inf, values that would overflow the limiter or grow
// per-request work). trustedProxies is capped at a sane chain depth.
const (
	maxRateLimitRPS   = 1_000_000
	maxRateLimitBurst = 1_000_000
)

// rlController is the live controller shared by both HTTP surfaces; rlEnv is
// the environment baseline that an override replaces. Both are set once at
// startup via SetRateLimitController, mirroring SetPublicBaseURL.
var (
	rlController *middleware.RateLimitController
	rlEnv        middleware.RateLimitConfig
)

// rateLimitDTO is the admin API wire shape (and the persisted blob shape). It
// is kept separate from middleware.RateLimitConfig so the JSON contract is
// owned here and the storage layer stays free of a middleware import.
type rateLimitDTO struct {
	Enabled bool    `json:"enabled"`
	RPS     float64 `json:"rps"`
	Burst   int     `json:"burst"`
}

func (d rateLimitDTO) toConfig() middleware.RateLimitConfig {
	return middleware.RateLimitConfig{
		Enabled: d.Enabled,
		RPS:     d.RPS,
		Burst:   d.Burst,
	}
}

func dtoFromConfig(c middleware.RateLimitConfig) rateLimitDTO {
	return rateLimitDTO{
		Enabled: c.Enabled,
		RPS:     c.RPS,
		Burst:   c.Burst,
	}
}

// SetRateLimitController wires the live controller and the environment baseline
// for the admin rate-limit endpoints. Called once during startup.
func SetRateLimitController(ctrl *middleware.RateLimitController, env middleware.RateLimitConfig) {
	rlController = ctrl
	rlEnv = env
}

// InitRateLimitFromStore applies a persisted override (if any) to the live
// controller at startup so a runtime setting survives a restart. It returns
// the resulting effective config for the caller to log.
func InitRateLimitFromStore() (middleware.RateLimitConfig, error) {
	ov, ok, err := loadRateLimitOverride()
	if err != nil {
		return middleware.RateLimitConfig{}, err
	}
	if ok {
		rlController.Apply(ov)
	}
	return rlController.Current(), nil
}

// loadRateLimitOverride reads the persisted override. The bool reports whether
// an override exists at all, which is what distinguishes "override wins" from
// "fall back to env".
func loadRateLimitOverride() (middleware.RateLimitConfig, bool, error) {
	raw, err := storage.GetConfigValue(rateLimitConfigKey)
	if err != nil {
		return middleware.RateLimitConfig{}, false, err
	}
	if raw == nil {
		return middleware.RateLimitConfig{}, false, nil
	}
	var d rateLimitDTO
	if err := json.Unmarshal(raw, &d); err != nil {
		return middleware.RateLimitConfig{}, false, err
	}
	return d.toConfig(), true, nil
}

// GetRateLimitHandler reports the environment baseline, the persisted override
// (null when none), and the effective config currently enforced. The UI uses
// env to label defaults and effective to show what is live.
func GetRateLimitHandler(c *gin.Context) {
	ov, ok, err := loadRateLimitOverride()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read rate-limit override"})
		return
	}
	resp := gin.H{
		"env":       dtoFromConfig(rlEnv),
		"effective": dtoFromConfig(rlController.Current()),
		"override":  nil,
	}
	if ok {
		resp["override"] = dtoFromConfig(ov)
	}
	c.JSON(http.StatusOK, resp)
}

// PutRateLimitHandler validates and persists a runtime override, then applies
// it live. The override fully replaces the environment baseline (whole-object
// precedence), so the UI always submits a complete config.
func PutRateLimitHandler(c *gin.Context) {
	var d rateLimitDTO
	if err := c.ShouldBindJSON(&d); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if msg := validateRateLimit(d); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}

	blob, err := json.Marshal(d)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode override"})
		return
	}
	if err := storage.PutConfigValue(rateLimitConfigKey, blob); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist override"})
		return
	}
	rlController.Apply(d.toConfig())
	recordAudit(c, "config.ratelimit.set", "", "")
	c.JSON(http.StatusOK, gin.H{"effective": dtoFromConfig(rlController.Current())})
}

// DeleteRateLimitHandler clears the override and reverts the live controller to
// the environment baseline.
func DeleteRateLimitHandler(c *gin.Context) {
	if err := storage.DeleteConfigValue(rateLimitConfigKey); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear override"})
		return
	}
	rlController.Apply(rlEnv)
	recordAudit(c, "config.ratelimit.clear", "", "")
	c.JSON(http.StatusOK, gin.H{"effective": dtoFromConfig(rlController.Current())})
}

// validateRateLimit rejects hostile or nonsensical input. It returns an empty
// string when the config is acceptable, or a human-readable reason otherwise.
// The limiter itself clamps zero/negative rps and burst to safe floors, but we
// reject them at the boundary so the UI cannot silently store a value it did
// not get back.
func validateRateLimit(d rateLimitDTO) string {
	if math.IsNaN(d.RPS) || math.IsInf(d.RPS, 0) {
		return "rps must be a finite number"
	}
	if d.RPS < 0 || d.RPS > maxRateLimitRPS {
		return "rps out of range"
	}
	if d.Burst < 0 || d.Burst > maxRateLimitBurst {
		return "burst out of range"
	}
	if d.Enabled && (d.RPS < 1 || d.Burst < 1) {
		return "enabled limiting requires rps and burst of at least 1"
	}
	return ""
}
