package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
)

// Two PUTs to the same key must not interleave the object write with the .meta
// sidecar write: the final state must be one writer's bytes paired with that
// same writer's ETag. The invariant checked is "persisted .meta ETag ==
// computeFileETag(object bytes)". Without per-object locking this fails when a
// late object rename pairs with an earlier meta write (or vice versa). Looped
// to make an unlocked interleaving overwhelmingly likely to be caught.
func TestConcurrentPut_ObjectAndMetaStayConsistent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for iter := 0; iter < 40; iter++ {
		withTempObjectsRoot(t)
		const bucket, key = "racebkt", "k.bin"
		if err := os.MkdirAll(filepath.Join(objectsRoot, bucket), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		const writers = 8
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				// Distinct, sizeable bodies so different writers yield different ETags.
				body := []byte(fmt.Sprintf("writer-%d-%s", n, string(make([]byte, 1024))))
				putObject(t, bucket, key, body)
			}(w)
		}
		wg.Wait()

		objPath := filepath.Join(objectsRoot, bucket, key)
		actualETag, err := computeFileETag(objPath)
		if err != nil {
			t.Fatalf("iter %d: compute etag: %v", iter, err)
		}
		raw, err := os.ReadFile(objPath + ".meta")
		if err != nil {
			t.Fatalf("iter %d: read meta: %v", iter, err)
		}
		var meta map[string]string
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("iter %d: unmarshal meta: %v", iter, err)
		}
		if meta[etagMetaKey] != actualETag {
			t.Fatalf("iter %d: torn write — object ETag %s != meta ETag %s",
				iter, actualETag, meta[etagMetaKey])
		}
	}
}
