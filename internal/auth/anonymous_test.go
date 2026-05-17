package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ByteBucket/internal/storage"

	"github.com/gin-gonic/gin"
)

// withAnonymousFixtures stands up a temp ObjectsRoot containing one public
// bucket with both a public-by-inheritance object and an explicitly-private
// override. It returns the temp dir so tests can also reach files directly.
func withAnonymousFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := storage.ObjectsRoot
	storage.ObjectsRoot = dir
	t.Cleanup(func() { storage.ObjectsRoot = orig })

	if err := os.MkdirAll(filepath.Join(dir, "pub"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "priv"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Mark "pub" as public-read.
	if err := storage.PutBucketACL("pub", &storage.BucketACL{Canned: storage.ACLPublicRead}); err != nil {
		t.Fatalf("bucket acl: %v", err)
	}
	// Objects: one inheriting public, one overridden private.
	publicObj := filepath.Join(dir, "pub", "open.txt")
	if err := os.WriteFile(publicObj, []byte("ok"), 0644); err != nil {
		t.Fatalf("write public obj: %v", err)
	}
	privateObj := filepath.Join(dir, "pub", "locked.txt")
	if err := os.WriteFile(privateObj, []byte("nope"), 0644); err != nil {
		t.Fatalf("write private obj: %v", err)
	}
	if err := storage.SetObjectACL(privateObj, storage.ACLPrivate); err != nil {
		t.Fatalf("object acl override: %v", err)
	}
	// And one object in the private bucket.
	if err := os.WriteFile(filepath.Join(dir, "priv", "x.txt"), []byte("secret"), 0644); err != nil {
		t.Fatalf("write priv obj: %v", err)
	}
	return dir
}

// newAnonymousRouter mounts AuthMiddleware in front of a stub handler so the
// only signal under test is the middleware's authorisation decision. The
// stub returns 200 with the routed bucket/key so tests can confirm that the
// request actually reached the handler chain.
func newAnonymousRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", AuthMiddleware, func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/:bucket", AuthMiddleware, func(c *gin.Context) { c.Status(http.StatusOK) })
	r.HEAD("/:bucket", AuthMiddleware, func(c *gin.Context) { c.Status(http.StatusOK) })
	r.PUT("/:bucket", AuthMiddleware, func(c *gin.Context) { c.Status(http.StatusOK) })
	r.DELETE("/:bucket", AuthMiddleware, func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/:bucket/*objectKey", AuthMiddleware, func(c *gin.Context) { c.Status(http.StatusOK) })
	r.HEAD("/:bucket/*objectKey", AuthMiddleware, func(c *gin.Context) { c.Status(http.StatusOK) })
	r.PUT("/:bucket/*objectKey", AuthMiddleware, func(c *gin.Context) { c.Status(http.StatusOK) })
	r.DELETE("/:bucket/*objectKey", AuthMiddleware, func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestAnonymousGet_PublicObject_Allowed(t *testing.T) {
	withAnonymousFixtures(t)
	r := newAnonymousRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pub/open.txt", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestAnonymousGet_PrivateOverride_Denied(t *testing.T) {
	withAnonymousFixtures(t)
	r := newAnonymousRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pub/locked.txt", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAnonymousGet_PrivateBucket_Denied(t *testing.T) {
	withAnonymousFixtures(t)
	r := newAnonymousRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/priv/x.txt", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAnonymousListObjects_PublicBucket_Allowed(t *testing.T) {
	withAnonymousFixtures(t)
	r := newAnonymousRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pub", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestAnonymousListBuckets_AlwaysDenied(t *testing.T) {
	withAnonymousFixtures(t)
	r := newAnonymousRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAnonymousPut_AlwaysDenied(t *testing.T) {
	withAnonymousFixtures(t)
	r := newAnonymousRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/pub/new.txt", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on anonymous PUT, got %d", w.Code)
	}
}

func TestAnonymousDelete_AlwaysDenied(t *testing.T) {
	withAnonymousFixtures(t)
	r := newAnonymousRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/pub/open.txt", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on anonymous DELETE, got %d", w.Code)
	}
}

func TestAnonymous_AclSubresource_Denied(t *testing.T) {
	withAnonymousFixtures(t)
	r := newAnonymousRouter()
	// Even on a public bucket, the ?acl subresource must require auth so
	// anonymous callers cannot enumerate or modify ACL state.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pub?acl", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on anonymous ?acl, got %d", w.Code)
	}
}

func TestAnonymous_HeadPublicObject_Allowed(t *testing.T) {
	withAnonymousFixtures(t)
	r := newAnonymousRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/pub/open.txt", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
