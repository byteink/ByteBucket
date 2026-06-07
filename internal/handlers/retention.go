package handlers

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// requestRetentionConfigKey persists the request-sample retention (in days) so
// an operator's choice survives a restart, mirroring the durability toggle.
const requestRetentionConfigKey = "requestretentiondays"

const (
	defaultRetentionDays = 30
	minRetentionDays     = 1
	// 365 caps store growth: at one sample/minute a year is ~7 MiB, still
	// trivial, while refusing an accidental "keep forever" that grows unbounded.
	maxRetentionDays = 365
)

// requestRetentionDays is the live retention window in days. Read by the
// sampler's prune step and by the chart's back-navigation bound.
var requestRetentionDays atomic.Int64

func init() { requestRetentionDays.Store(defaultRetentionDays) }

// requestRetentionWindow returns the retention as a duration.
func requestRetentionWindow() time.Duration {
	return time.Duration(requestRetentionDays.Load()) * 24 * time.Hour
}

// RequestRetentionDays exposes the live retention for the sampler's prune step.
func RequestRetentionDays() int { return int(requestRetentionDays.Load()) }

// clampRetentionDays bounds a requested value to the supported range.
func clampRetentionDays(n int) int {
	if n < minRetentionDays {
		return minRetentionDays
	}
	if n > maxRetentionDays {
		return maxRetentionDays
	}
	return n
}

// InitRequestRetentionFromStore applies a persisted retention over the default
// at startup. Returns the effective value for the caller to log.
func InitRequestRetentionFromStore() (int, error) {
	raw, err := storage.GetConfigValue(requestRetentionConfigKey)
	if err != nil {
		return 0, err
	}
	if raw != nil {
		if n, convErr := strconv.Atoi(string(raw)); convErr == nil {
			requestRetentionDays.Store(int64(clampRetentionDays(n)))
		}
	}
	return RequestRetentionDays(), nil
}

type retentionDTO struct {
	Days int `json:"days"`
}

// GetRetentionHandler returns the effective request-sample retention.
func GetRetentionHandler(c *gin.Context) {
	c.JSON(http.StatusOK, retentionDTO{Days: RequestRetentionDays()})
}

// PutRetentionHandler sets and persists the retention so it applies live and
// survives a restart. Out-of-range values are clamped, not rejected, so the UI
// slider can submit freely.
func PutRetentionHandler(c *gin.Context) {
	var d retentionDTO
	if err := c.ShouldBindJSON(&d); err != nil {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Invalid request body")
		return
	}
	days := clampRetentionDays(d.Days)
	if err := storage.PutConfigValue(requestRetentionConfigKey, []byte(strconv.Itoa(days))); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to persist retention setting")
		return
	}
	requestRetentionDays.Store(int64(days))
	c.JSON(http.StatusOK, retentionDTO{Days: days})
}
