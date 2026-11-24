package handlers

import (
	"hash/fnv"
	"sync"
)

// objectLockStripes is the fixed number of mutexes guarding object writes.
// A fixed array bounds memory (no per-key allocation that a hostile key space
// could grow without limit) at the cost of occasional false sharing between
// unrelated keys that hash to the same stripe. Request bodies are staged before
// locking; only the predicate check and object/sidecar commit serialize, so a
// slow upload cannot block an unrelated key sharing its stripe.
const objectLockStripes = 256

// objectLocks serializes mutations of a single object so its data file and
// .meta sidecar are always updated as a consistent pair. Shared by the write
// path (finalizeObjectWrite) and the delete path (removeObject) so a PUT and a
// DELETE of the same key cannot interleave into a torn object/metadata state.
var objectLocks [objectLockStripes]sync.Mutex

// lockObjectPath locks the stripe for the given on-disk object path and returns
// the unlock function. Callers do: defer lockObjectPath(p)().
func lockObjectPath(path string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(path))
	m := &objectLocks[h.Sum32()%objectLockStripes]
	m.Lock()
	return m.Unlock
}
