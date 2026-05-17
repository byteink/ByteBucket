package handlers

import (
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// publicBaseURL is the operator-supplied origin under which objects are
// reachable for anonymous public-read access. The admin UI surfaces this on
// the object detail page so users can copy a shareable link without guessing
// the storage port mapping. Stored in an atomic.Value so the value can be
// initialised once at startup and read by handlers without locking.
var publicBaseURL atomic.Value // string

// SetPublicBaseURL is called once during process startup with the resolved
// PUBLIC_BASE_URL env value. Trailing slashes are stripped so callers can
// concatenate paths without worrying about doubled separators.
func SetPublicBaseURL(v string) {
	publicBaseURL.Store(strings.TrimRight(v, "/"))
}

// GetConfigHandler returns runtime config the admin UI needs but cannot
// derive client-side. Today that is just the public base URL; future fields
// (signed-URL TTL, max upload size hints) belong here too.
func GetConfigHandler(c *gin.Context) {
	v, _ := publicBaseURL.Load().(string)
	c.JSON(http.StatusOK, gin.H{"publicBaseURL": v})
}
