package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/gobwas/glob"
)

// CORSConfig represents the CORS configuration for the ByteBucket server
type CORSConfig struct {
	AllowedOrigins []string `json:"allowed_origins"` // List of allowed origins (can include glob patterns)
	AllowedMethods []string `json:"allowed_methods"` // HTTP methods allowed
	AllowedHeaders []string `json:"allowed_headers"` // HTTP headers allowed
	ExposeHeaders  []string `json:"expose_headers"`  // HTTP headers exposed to browsers
	MaxAge         int      `json:"max_age"`         // Preflight cache duration in seconds
}

// defaultAdminOrigins lists the browser origins the bundled admin UI ships on
// out of the box. They are added to every default CORS config so the UI can
// talk to the S3 API on :9000 without the operator having to hand-edit CORS.
// Users on custom hosts must add their origin via the CORS page.
var defaultAdminOrigins = []string{
	"http://localhost:9001",
	"http://127.0.0.1:9001",
	"http://localhost:5173", // Vite dev server
}

// DefaultCORSConfig returns default CORS settings
func DefaultCORSConfig() CORSConfig {
	// Initialize with environment variable if available
	envOrigins := os.Getenv("CORS_ALLOWED_ORIGINS")
	var origins []string

	if envOrigins != "" {
		origins = strings.Split(envOrigins, ",")
	} else {
		origins = append([]string{}, defaultAdminOrigins...)
	}

	return CORSConfig{
		AllowedOrigins: origins,
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "HEAD", "OPTIONS"},
		AllowedHeaders: []string{
			"Authorization", "Content-Type", "Content-Length", "Accept", "Accept-Encoding",
			"X-CSRF-Token", "X-Requested-With", "X-Amz-Date", "X-Amz-Content-Sha256",
			"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Signature", "X-Amz-Security-Token",
			"X-Amz-User-Agent", "X-Amz-Target", "X-Amz-Expires", "X-Amz-Copy-Source",
			"x-amz-meta-*", "x-amz-*",
		},
		ExposeHeaders: []string{
			"ETag", "Content-Length", "Content-Type", "Connection", "Date",
			"Last-Modified", "X-Amz-Request-Id", "X-Amz-Delete-Marker",
			"X-Amz-Version-Id", "Content-Range", "Server", "x-amz-*",
		},
		MaxAge: 600, // 10 minutes
	}
}

const corsConfigPath = "/data/cors.json"

// SaveCORSConfig persists the CORS configuration to disk
func SaveCORSConfig(config CORSConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	// Ensure directory exists
	dir := filepath.Dir(corsConfigPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(corsConfigPath, data, 0644)
}

// LoadCORSConfig loads the CORS configuration from disk
func LoadCORSConfig() (CORSConfig, error) {
	// Check if config file exists
	if _, err := os.Stat(corsConfigPath); os.IsNotExist(err) {
		// Create default config
		config := DefaultCORSConfig()
		if err := SaveCORSConfig(config); err != nil {
			return config, err
		}
		return config, nil
	}

	// Read and parse the config file
	data, err := os.ReadFile(corsConfigPath)
	if err != nil {
		return DefaultCORSConfig(), err
	}

	var config CORSConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return DefaultCORSConfig(), err
	}

	return config, nil
}

// IsOriginAllowed checks if a given origin matches any allowed origin pattern
func IsOriginAllowed(origin string, allowedOrigins []string) bool {
	if len(allowedOrigins) == 0 {
		return false
	}

	// Check for wildcard
	for _, allowed := range allowedOrigins {
		if allowed == "*" {
			return true
		}

		// Check for glob pattern
		g, err := glob.Compile(allowed)
		if err == nil && g.Match(origin) {
			return true
		}

		// Direct match
		if allowed == origin {
			return true
		}
	}

	return false
}
