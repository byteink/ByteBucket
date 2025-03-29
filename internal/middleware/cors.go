package middleware

import (
	"ByteBucket/internal/storage"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORSMiddleware returns a Gin middleware function that handles CORS requests
// based on the dynamic configuration stored in CORS settings
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Load current CORS configuration
		corsConfig, err := storage.LoadCORSConfig()
		if err != nil {
			// Use default config if there's an error
			corsConfig = storage.DefaultCORSConfig()
		}

		origin := c.Request.Header.Get("Origin")
		if origin == "" {
			// No Origin header, continue with the request
			c.Next()
			return
		}

		// Check if origin is allowed
		if storage.IsOriginAllowed(origin, corsConfig.AllowedOrigins) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", strings.Join(corsConfig.AllowedMethods, ", "))
			c.Header("Access-Control-Allow-Headers", strings.Join(corsConfig.AllowedHeaders, ", "))
			c.Header("Access-Control-Expose-Headers", strings.Join(corsConfig.ExposeHeaders, ", "))
			c.Header("Access-Control-Max-Age", strconv.Itoa(corsConfig.MaxAge))
			c.Header("Access-Control-Allow-Credentials", "true")
		}

		// Handle preflight OPTIONS requests
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204) // No content status for OPTIONS
			return
		}

		c.Next()
	}
}
