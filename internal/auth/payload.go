package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

const payloadBufferSize = 32 << 10

func serveVerifiedPayload(c *gin.Context, payloadHash string) {
	if payloadHash == "UNSIGNED-PAYLOAD" || strings.HasPrefix(payloadHash, "STREAMING-") {
		c.Next()
		return
	}
	file, err := openPayloadSpool()
	if err != nil {
		abortWithError(c, http.StatusInternalServerError, "InternalError", "Error staging request body")
		return
	}
	defer closePayloadSpool(file)
	if !verifyPayloadSpool(c, file, payloadHash) {
		return
	}
	body := c.Request.Body
	defer func() { c.Request.Body = body }()
	c.Request.Body = io.NopCloser(file)
	c.Next()
}

func openPayloadSpool() (*os.File, error) {
	// The scratch image has no /tmp; uploads is writable and outside object listings.
	if err := os.MkdirAll(storage.UploadsRoot, 0700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(storage.UploadsRoot, ".payload-*")
	if err != nil {
		return nil, err
	}
	return unlinkPayloadSpool(file)
}

func unlinkPayloadSpool(file *os.File) (*os.File, error) {
	if err := os.Remove(file.Name()); err != nil {
		closePayloadSpool(file)
		return nil, err
	}
	return file, nil
}

func verifyPayloadSpool(c *gin.Context, file *os.File, wantHash string) bool {
	digest := sha256.New()
	source := &payloadReader{reader: c.Request.Body}
	_, err := io.CopyBuffer(io.MultiWriter(file, digest), source, make([]byte, payloadBufferSize))
	if err != nil {
		if source.err != nil {
			abortWithError(c, http.StatusBadRequest, "IncompleteBody", "Failed to read request body")
		} else {
			abortWithError(c, http.StatusInternalServerError, "InternalError", "Error staging request body")
		}
		return false
	}
	if hex.EncodeToString(digest.Sum(nil)) != wantHash {
		abortWithError(c, http.StatusBadRequest, "XAmzContentSHA256Mismatch", "The provided 'x-amz-content-sha256' header does not match what was computed")
		return false
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		abortWithError(c, http.StatusInternalServerError, "InternalError", "Error staging request body")
		return false
	}
	return true
}

func closePayloadSpool(file *os.File) {
	if err := file.Close(); err != nil {
		slog.Error("request body staging close failed", "error", err)
	}
}

type payloadReader struct {
	reader io.Reader
	err    error
}

func (r *payloadReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}
