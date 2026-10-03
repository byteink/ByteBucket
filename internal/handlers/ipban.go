package handlers

import (
	"ByteBucket/internal/middleware"
	"ByteBucket/internal/storage"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ipBanConfigKey is the Config-bucket key under which the runtime override is
// persisted. Absent key means "no override": the environment baseline wins.
const ipBanConfigKey = "ipban"

// banController is the live failed-auth ban controller on the storage surface;
// banEnv is the environment baseline an override replaces. Both are set once
// at startup via SetIPBanController, mirroring SetRateLimitController.
var (
	banController *middleware.IPBanController
	banEnv        middleware.IPBanConfig
)

// ipBanDTO is the admin API wire shape (and the persisted blob shape), kept
// apart from middleware.IPBanConfig so the JSON contract is owned here.
type ipBanDTO struct {
	Enabled       bool `json:"enabled"`
	MaxFailures   int  `json:"maxFailures"`
	WindowSeconds int  `json:"windowSeconds"`
	BanSeconds    int  `json:"banSeconds"`
}

func (d ipBanDTO) toConfig() middleware.IPBanConfig {
	return middleware.IPBanConfig{
		Enabled:       d.Enabled,
		MaxFailures:   d.MaxFailures,
		WindowSeconds: d.WindowSeconds,
		BanSeconds:    d.BanSeconds,
	}
}

func ipBanDTOFrom(c middleware.IPBanConfig) ipBanDTO {
	return ipBanDTO{
		Enabled:       c.Enabled,
		MaxFailures:   c.MaxFailures,
		WindowSeconds: c.WindowSeconds,
		BanSeconds:    c.BanSeconds,
	}
}

// SetIPBanController wires the live controller and the environment baseline
// for the admin endpoints. Called once during startup.
func SetIPBanController(ctrl *middleware.IPBanController, env middleware.IPBanConfig) {
	banController = ctrl
	banEnv = env
}

// InitIPBanFromStore applies a persisted override (if any) at startup so a
// runtime setting survives a restart, and returns the effective config.
func InitIPBanFromStore() (middleware.IPBanConfig, error) {
	ov, ok, err := loadIPBanOverride()
	if err != nil {
		return middleware.IPBanConfig{}, err
	}
	if ok {
		banController.Apply(ov)
	}
	return banController.Current(), nil
}

// loadIPBanOverride reads and validates the persisted override. A stored value
// that no longer validates is an error rather than a silent clamp, so a
// corrupt or tampered blob is noticed instead of quietly changing behaviour.
func loadIPBanOverride() (middleware.IPBanConfig, bool, error) {
	raw, err := storage.GetConfigValue(ipBanConfigKey)
	if err != nil || raw == nil {
		return middleware.IPBanConfig{}, false, err
	}
	var d ipBanDTO
	if err := json.Unmarshal(raw, &d); err != nil {
		return middleware.IPBanConfig{}, false, err
	}
	cfg := d.toConfig()
	if err := cfg.Validate(); err != nil {
		return middleware.IPBanConfig{}, false, err
	}
	return cfg, true, nil
}

// GetIPBanHandler reports the environment baseline, the persisted override
// (null when none), and the effective config currently enforced.
func GetIPBanHandler(c *gin.Context) {
	ov, ok, err := loadIPBanOverride()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read ip ban override"})
		return
	}
	resp := gin.H{
		"env":       ipBanDTOFrom(banEnv),
		"effective": ipBanDTOFrom(banController.Current()),
		"override":  nil,
	}
	if ok {
		resp["override"] = ipBanDTOFrom(ov)
	}
	c.JSON(http.StatusOK, resp)
}

// PutIPBanHandler validates and persists a runtime override, then applies it
// live. The override fully replaces the environment baseline, so the UI always
// submits a complete config. Applying flushes the ban table, lifting every
// active ban.
func PutIPBanHandler(c *gin.Context) {
	var d ipBanDTO
	if err := c.ShouldBindJSON(&d); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	cfg := d.toConfig()
	if err := cfg.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := putConfigJSON(ipBanConfigKey, d); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist override"})
		return
	}
	banController.Apply(cfg)
	recordAudit(c, "config.ipban.set", "", "")
	c.JSON(http.StatusOK, gin.H{"effective": ipBanDTOFrom(banController.Current())})
}

// DeleteIPBanHandler clears the override and reverts to the environment
// baseline.
func DeleteIPBanHandler(c *gin.Context) {
	if err := storage.DeleteConfigValue(ipBanConfigKey); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear override"})
		return
	}
	banController.Apply(banEnv)
	recordAudit(c, "config.ipban.clear", "", "")
	c.JSON(http.StatusOK, gin.H{"effective": ipBanDTOFrom(banController.Current())})
}
