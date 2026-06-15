package handlers

import (
	"net/http"
	"strconv"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// logEventDTO is the admin API wire shape for a unified-log record. omitempty
// keeps each category's payload free of the other's unused fields, matching the
// storage.Event layout.
type logEventDTO struct {
	Ts         int64   `json:"ts"` // unix nano; pagination cursor for ?before=
	Time       string  `json:"time"`
	Category   string  `json:"category"`
	Actor      string  `json:"actor,omitempty"`
	Op         string  `json:"op"`
	Target     string  `json:"target,omitempty"`
	Bucket     string  `json:"bucket,omitempty"`
	Key        string  `json:"key,omitempty"`
	Status     int     `json:"status,omitempty"`
	ErrorCode  string  `json:"errorCode,omitempty"`
	ClientIP   string  `json:"clientIp,omitempty"`
	BytesIn    int64   `json:"bytesIn,omitempty"`
	BytesOut   int64   `json:"bytesOut,omitempty"`
	DurationMs float64 `json:"durationMs,omitempty"`
	UserAgent  string  `json:"userAgent,omitempty"`
	Detail     string  `json:"detail,omitempty"`
}

type logListDTO struct {
	Events []logEventDTO `json:"events"`
}

func toLogDTO(e storage.Event) logEventDTO {
	return logEventDTO{
		Ts:         e.TimeUnixNano,
		Time:       time.Unix(0, e.TimeUnixNano).UTC().Format(time.RFC3339),
		Category:   e.Category,
		Actor:      e.Actor,
		Op:         e.Op,
		Target:     e.Target,
		Bucket:     e.Bucket,
		Key:        e.Key,
		Status:     e.Status,
		ErrorCode:  e.ErrorCode,
		ClientIP:   e.ClientIP,
		BytesIn:    e.BytesIn,
		BytesOut:   e.BytesOut,
		DurationMs: e.DurationMs,
		UserAgent:  e.UserAgent,
		Detail:     e.Detail,
	}
}

// GetLogsHandler returns recent events of one category newest-first. ?category
// (control|data) is required; ?limit bounds the page; ?before (unix nano) pages
// into older events. The category split mirrors the storage buckets, so the UI
// toggles between the control-plane audit trail and the data-plane access log.
func GetLogsHandler(c *gin.Context) {
	category := c.Query("category")
	if category != storage.EventControl && category != storage.EventData {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "category must be control or data")
		return
	}
	limit := auditLimit(c.Query("limit"))
	var before int64
	if raw := c.Query("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			respondError(c, http.StatusBadRequest, "InvalidArgument", "before must be a unix-nano integer")
			return
		}
		before = n
	}

	events, err := storage.QueryEvents(category, limit, before)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to read event log")
		return
	}

	out := make([]logEventDTO, 0, len(events))
	for _, e := range events {
		out = append(out, toLogDTO(e))
	}
	c.JSON(http.StatusOK, logListDTO{Events: out})
}
