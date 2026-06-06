package handlers

import (
	"net/http"
	"sync/atomic"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// syncWritesConfigKey is the Config-bucket key under which the runtime
// durability override is persisted ("true"/"false"). Absent means no override:
// the SYNC_WRITES environment baseline (default on) wins.
const syncWritesConfigKey = "syncwrites"

// syncWrites is the live durability setting. When true, object writes are
// fsync'd (data file + parent directory) before the PUT/Copy response returns,
// so an acknowledged write survives power loss. Default true; an operator may
// trade durability for throughput via SYNC_WRITES or the admin settings UI.
var syncWrites atomic.Bool

func init() { syncWrites.Store(true) }

// SetSyncWrites seeds the startup default from the SYNC_WRITES env var.
func SetSyncWrites(on bool) { syncWrites.Store(on) }

// syncWritesEnabled reports the live setting; read on every object write.
func syncWritesEnabled() bool { return syncWrites.Load() }

// InitSyncWritesFromStore applies a persisted runtime override (if any) over
// the env default at startup so a UI toggle survives a restart. Returns the
// effective value for the caller to log.
func InitSyncWritesFromStore() (bool, error) {
	raw, err := storage.GetConfigValue(syncWritesConfigKey)
	if err != nil {
		return false, err
	}
	if raw != nil {
		syncWrites.Store(string(raw) == "true")
	}
	return syncWritesEnabled(), nil
}

// syncWritesDTO is the admin API wire shape for the durability toggle.
type syncWritesDTO struct {
	Enabled bool `json:"enabled"`
}

// GetSyncWritesHandler returns the effective durability setting.
func GetSyncWritesHandler(c *gin.Context) {
	c.JSON(http.StatusOK, syncWritesDTO{Enabled: syncWritesEnabled()})
}

// PutSyncWritesHandler sets and persists the durability setting so it both
// applies live and survives a restart.
func PutSyncWritesHandler(c *gin.Context) {
	var d syncWritesDTO
	if err := c.ShouldBindJSON(&d); err != nil {
		respondError(c, http.StatusBadRequest, "InvalidArgument", "Invalid request body")
		return
	}
	val := "false"
	if d.Enabled {
		val = "true"
	}
	if err := storage.PutConfigValue(syncWritesConfigKey, []byte(val)); err != nil {
		respondError(c, http.StatusInternalServerError, "InternalError", "Failed to persist durability setting")
		return
	}
	syncWrites.Store(d.Enabled)
	c.JSON(http.StatusOK, syncWritesDTO{Enabled: d.Enabled})
}
