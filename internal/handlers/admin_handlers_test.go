package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestUpdateUserHandler_NotFoundReturns404 pins the contract that updating a
// user that does not exist is a client error (404), not a server fault (500).
// The store distinguishes "not found" from a real persistence failure via a
// sentinel error; the handler must map that to 404.
func TestUpdateUserHandler_NotFoundReturns404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupHandlerStore(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "accessKeyID", Value: "does-not-exist"}}
	c.Request = httptest.NewRequest(http.MethodPut, "/api/users/does-not-exist",
		strings.NewReader(`{"acl":[{"effect":"Allow","buckets":["*"],"actions":["*"]}]}`))
	c.Request.Header.Set("Content-Type", "application/json")

	UpdateUserHandler(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("update nonexistent user = %d, want 404", w.Code)
	}
}
