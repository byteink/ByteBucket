package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

const (
	defaultAuditLimit = 50
	maxAuditLimit     = 200
)

// recordAudit appends one control-plane event to the unified log, attributing
// it to the authenticated admin on the context. Best-effort: a failed append is
// logged, never propagated, so auditing can never break the action being
// audited.
func recordAudit(c *gin.Context, action, target, detail string) {
	actor := ""
	if v, ok := c.Get("user"); ok {
		if u, ok := v.(*storage.User); ok {
			actor = u.AccessKeyID
		}
	}
	if err := storage.AppendEvent(storage.Event{
		TimeUnixNano: time.Now().UnixNano(),
		Category:     storage.EventControl,
		Actor:        actor,
		Op:           action,
		Target:       target,
		Detail:       detail,
	}); err != nil {
		slog.Warn("audit append failed", "action", action, "err", err.Error())
	}
}

// auditLimit resolves the page size, defaulting and clamping to a sane range so
// a caller cannot request an unbounded scan.
func auditLimit(raw string) int {
	if raw == "" {
		return defaultAuditLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultAuditLimit
	}
	if n > maxAuditLimit {
		return maxAuditLimit
	}
	return n
}

type auditEventDTO struct {
	Ts     int64  `json:"ts"` // unix nano; pagination cursor for ?before=
	Time   string `json:"time"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Detail string `json:"detail"`
}

type auditListDTO struct {
	Events []auditEventDTO `json:"events"`
}

// GetAuditHandler returns recent audit events newest-first. ?limit bounds the
// page; ?before (unix nano) pages into older events.
func GetAuditHandler(c *gin.Context) {
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

	events, err := storage.QueryEvents(storage.EventControl, limit, before)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to read audit log")
		return
	}

	out := make([]auditEventDTO, 0, len(events))
	for _, e := range events {
		out = append(out, auditEventDTO{
			Ts:     e.TimeUnixNano,
			Time:   time.Unix(0, e.TimeUnixNano).UTC().Format(time.RFC3339),
			Actor:  e.Actor,
			Action: e.Op,
			Target: e.Target,
			Detail: e.Detail,
		})
	}
	c.JSON(http.StatusOK, auditListDTO{Events: out})
}
