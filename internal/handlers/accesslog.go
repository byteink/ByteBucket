package handlers

import (
	"net/http"
	"strconv"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// Config-bucket keys under which the runtime access-log settings are persisted,
// so an operator's choices survive a restart (the live values themselves live
// in the storage package; see events.go).
const (
	accessLogEnabledKey    = "accesslogenabled"
	accessLogMaxEventsKey  = "accesslogmaxevents"
	accessLogMaxAgeDaysKey = "accesslogmaxagedays"
)

// Age bound: 0 disables the age cap (count may still bound growth); 365 refuses
// an accidental "keep forever".
const (
	minAccessLogAgeDays = 0
	maxAccessLogAgeDays = 365
)

func clampAccessLogAgeDays(n int) int {
	if n < minAccessLogAgeDays {
		return minAccessLogAgeDays
	}
	if n > maxAccessLogAgeDays {
		return maxAccessLogAgeDays
	}
	return n
}

// clampAccessLogMaxEvents floors the count cap at 0 (0 disables it).
func clampAccessLogMaxEvents(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func accessLogMaxAgeDays() int { return int(storage.AccessLogMaxAge() / (24 * time.Hour)) }

// accessLogDTO is the admin API wire shape for the access-log config.
type accessLogDTO struct {
	Enabled    bool `json:"enabled"`
	MaxEvents  int  `json:"maxEvents"`
	MaxAgeDays int  `json:"maxAgeDays"`
}

func currentAccessLogDTO() accessLogDTO {
	return accessLogDTO{
		Enabled:    storage.AccessLogEnabled(),
		MaxEvents:  storage.AccessLogMaxEvents(),
		MaxAgeDays: accessLogMaxAgeDays(),
	}
}

// InitAccessLogFromStore applies persisted access-log overrides over the env
// defaults at startup so UI changes survive a restart. Returns the effective
// settings for the caller to log.
func InitAccessLogFromStore() (accessLogDTO, error) {
	if raw, err := storage.GetConfigValue(accessLogEnabledKey); err != nil {
		return accessLogDTO{}, err
	} else if raw != nil {
		storage.SetAccessLogEnabled(string(raw) == "true")
	}
	if raw, err := storage.GetConfigValue(accessLogMaxEventsKey); err != nil {
		return accessLogDTO{}, err
	} else if raw != nil {
		if n, convErr := strconv.Atoi(string(raw)); convErr == nil {
			storage.SetAccessLogMaxEvents(clampAccessLogMaxEvents(n))
		}
	}
	if raw, err := storage.GetConfigValue(accessLogMaxAgeDaysKey); err != nil {
		return accessLogDTO{}, err
	} else if raw != nil {
		if n, convErr := strconv.Atoi(string(raw)); convErr == nil {
			storage.SetAccessLogMaxAge(time.Duration(clampAccessLogAgeDays(n)) * 24 * time.Hour)
		}
	}
	return currentAccessLogDTO(), nil
}

// GetAccessLogHandler returns the effective access-log config.
func GetAccessLogHandler(c *gin.Context) {
	c.JSON(http.StatusOK, currentAccessLogDTO())
}

// PutAccessLogHandler sets and persists the access-log config so it applies live
// and survives a restart. Out-of-range values are clamped, not rejected, so the
// UI can submit freely.
func PutAccessLogHandler(c *gin.Context) {
	var d accessLogDTO
	if err := c.ShouldBindJSON(&d); err != nil {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Invalid request body")
		return
	}
	d.MaxEvents = clampAccessLogMaxEvents(d.MaxEvents)
	d.MaxAgeDays = clampAccessLogAgeDays(d.MaxAgeDays)

	enabled := "false"
	if d.Enabled {
		enabled = "true"
	}
	for k, v := range map[string]string{
		accessLogEnabledKey:    enabled,
		accessLogMaxEventsKey:  strconv.Itoa(d.MaxEvents),
		accessLogMaxAgeDaysKey: strconv.Itoa(d.MaxAgeDays),
	} {
		if err := storage.PutConfigValue(k, []byte(v)); err != nil {
			respondError(c, http.StatusInternalServerError, "InternalError", "Failed to persist access-log setting")
			return
		}
	}
	storage.SetAccessLogEnabled(d.Enabled)
	storage.SetAccessLogMaxEvents(d.MaxEvents)
	storage.SetAccessLogMaxAge(time.Duration(d.MaxAgeDays) * 24 * time.Hour)
	recordAudit(c, "config.accesslog", enabled, "")
	c.JSON(http.StatusOK, currentAccessLogDTO())
}
