package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

func getConfigPublicBaseURL(t *testing.T) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	GetConfigHandler(c)
	if w.Code != http.StatusOK {
		t.Fatalf("config: %d", w.Code)
	}
	var body struct {
		PublicBaseURL string `json:"publicBaseURL"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return body.PublicBaseURL
}

func TestGetConfig_ReflectsPublicBaseURLAndTrimsSlash(t *testing.T) {
	SetPublicBaseURL("https://cdn.example.com/")
	if got := getConfigPublicBaseURL(t); got != "https://cdn.example.com" {
		t.Fatalf("publicBaseURL=%q want trailing slash trimmed", got)
	}

	SetPublicBaseURL("https://other.example.com")
	if got := getConfigPublicBaseURL(t); got != "https://other.example.com" {
		t.Fatalf("publicBaseURL=%q want updated value", got)
	}
}
