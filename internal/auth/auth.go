package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware validates incoming requests by looking up the user,
// decrypting the stored secret, and verifying a simple HMAC-SHA256 signature.
func AuthMiddleware(c *gin.Context) {
	providedAccessKey := c.GetHeader("X-Access-Key")
	providedSignature := c.GetHeader("X-Signature")
	if providedAccessKey == "" || providedSignature == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Missing authentication headers"})
		return
	}

	user, err := storage.GetUser(providedAccessKey)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: user not found"})
		return
	}

	// Decrypt the stored secret.
	secret, err := storage.Decrypt(user.EncryptedSecret)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Internal error"})
		return
	}

	// Compute expected signature: HMAC-SHA256(Method+URL.Path) using the secret.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(c.Request.Method))
	mac.Write([]byte(c.Request.URL.Path))
	expectedMAC := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expectedMAC), []byte(providedSignature)) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: invalid signature"})
		return
	}

	c.Next()
}
