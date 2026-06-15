package storage

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"
)

// The unified event log lives in its own BoltDB file (logs.db), separate from
// users.db, so the high-volume data-plane access firehose cannot bloat or
// contend with the auth source of truth, and the log file can be pruned and
// compacted independently. Two buckets keep retention isolated per category:
// a burst of access events must never evict control-plane audit history.
//
//	EventControl — admin mutations (user CRUD, config changes). Always recorded.
//	               Count-capped, like the former AuditLog. Low volume.
//	EventData    — object access (Get/Put/Delete/List ...). Opt-in, batched,
//	               count + age capped. High volume.
//
// Keys mirror the audit scheme: an 8-byte big-endian unix-nano timestamp
// followed by an 8-byte per-bucket sequence, so records sort chronologically
// and two events in the same nanosecond never collide.
const (
	eventControlBucket = "EventControl"
	eventDataBucket    = "EventData"
)

// Event categories.
const (
	EventControl = "control"
	EventData    = "data"
)

// eventDB is the dedicated handle for logs.db. Distinct from userDB on purpose
// (see the bucket comment above).
var eventDB *bolt.DB

// maxControlEvents caps the control-plane trail so the embedded store cannot
// grow without bound. A var (not const) so tests can shrink it; oldest records
// are pruned first. Mirrors the former maxAuditEvents.
var maxControlEvents = 10000

// eventChanCap bounds the in-memory buffer for data events. A send that would
// block (flusher behind) is dropped rather than stalling the request path; the
// drop is counted and logged, never silent.
const eventChanCap = 8192

var (
	eventCh        chan Event
	eventsDropped  atomic.Int64
	accessEnabled  atomic.Bool
	accessMaxCount atomic.Int64 // 0 disables the count cap
	accessMaxAgeNs atomic.Int64 // 0 disables the age cap
)

func init() {
	// Defaults: data-plane logging off; generous count and 30-day age caps so an
	// operator who enables it without tuning still gets bounded growth.
	accessMaxCount.Store(100000)
	accessMaxAgeNs.Store(int64(30 * 24 * time.Hour))
}

// Event is one record in the unified log. Category selects the bucket and which
// fields are meaningful: control events use Op/Target/Detail; data events use
// Op/Bucket/Key/Status/ErrorCode and the request envelope. omitempty keeps each
// category's JSON free of the other's unused fields.
type Event struct {
	TimeUnixNano int64   `json:"ts"`
	Category     string  `json:"category"`
	Actor        string  `json:"actor,omitempty"`
	Op           string  `json:"op"`
	Target       string  `json:"target,omitempty"`
	Bucket       string  `json:"bucket,omitempty"`
	Key          string  `json:"key,omitempty"`
	Status       int     `json:"status,omitempty"`
	ErrorCode    string  `json:"error_code,omitempty"`
	ClientIP     string  `json:"client_ip,omitempty"`
	BytesIn      int64   `json:"bytes_in,omitempty"`
	BytesOut     int64   `json:"bytes_out,omitempty"`
	DurationMs   float64 `json:"duration_ms,omitempty"`
	UserAgent    string  `json:"user_agent,omitempty"`
	Detail       string  `json:"detail,omitempty"`
}

// InitEventStore opens logs.db and ensures both buckets exist. Mirrors
// InitUserStore's path resolution so the file sits beside users.db.
func InitEventStore(fileName string) error {
	dbPath := getStoragePath(fileName)
	if err := ensureDataDirExists(dbPath); err != nil {
		return err
	}
	var err error
	eventDB, err = bolt.Open(dbPath, 0600, nil)
	if err != nil {
		return err
	}
	eventCh = make(chan Event, eventChanCap)
	return eventDB.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(eventControlBucket)); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists([]byte(eventDataBucket))
		return err
	})
}

// Access-log live settings. Kept in the storage package (not handlers) because
// both the capture middleware and the flusher must read them without an import
// cycle; the handlers package only sets them.
func AccessLogEnabled() bool { return accessEnabled.Load() }

// SetAccessLogEnabled toggles data-plane capture. Control-plane auditing is
// unaffected — it is always on.
func SetAccessLogEnabled(on bool) { accessEnabled.Store(on) }

func AccessLogMaxEvents() int { return int(accessMaxCount.Load()) }

// SetAccessLogMaxEvents sets the data count cap. A non-positive value disables
// the count cap (age may still bound growth).
func SetAccessLogMaxEvents(n int) {
	if n < 0 {
		n = 0
	}
	accessMaxCount.Store(int64(n))
}

func AccessLogMaxAge() time.Duration { return time.Duration(accessMaxAgeNs.Load()) }

// SetAccessLogMaxAge sets the data age cap. A non-positive value disables it.
func SetAccessLogMaxAge(d time.Duration) {
	if d < 0 {
		d = 0
	}
	accessMaxAgeNs.Store(int64(d))
}

func encodeEventKey(nano int64, seq uint64) []byte {
	k := make([]byte, 16)
	binary.BigEndian.PutUint64(k[0:8], uint64(nano))
	binary.BigEndian.PutUint64(k[8:16], seq)
	return k
}

func bucketFor(category string) string {
	if category == EventData {
		return eventDataBucket
	}
	return eventControlBucket
}

// AppendEvent writes one event synchronously and prunes its bucket. Used for
// control-plane events, where immediacy matters (an admin action's audit row
// must be queryable the moment the action returns) and volume is trivial.
func AppendEvent(e Event) error {
	return eventDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketFor(e.Category)))
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		if err := putEvent(b, e); err != nil {
			return err
		}
		return pruneBucket(b, e.Category, time.Unix(0, e.TimeUnixNano))
	})
}

// EnqueueEvent buffers one data event for the async flusher. Non-blocking: if
// the buffer is full (flusher behind), the event is dropped and counted so the
// request path never stalls on disk. A no-op when data logging is disabled.
func EnqueueEvent(e Event) {
	if !accessEnabled.Load() || eventCh == nil {
		return
	}
	select {
	case eventCh <- e:
	default:
		eventsDropped.Add(1)
	}
}

// putEvent appends one event under a fresh sequence within an open write txn.
func putEvent(b *bolt.Bucket, e Event) error {
	seq, err := b.NextSequence()
	if err != nil {
		return err
	}
	val, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return b.Put(encodeEventKey(e.TimeUnixNano, seq), val)
}

// flushEvents writes a batch of data events in a single transaction (one fsync
// amortised across many events) and prunes once afterward.
func flushEvents(batch []Event) error {
	if len(batch) == 0 {
		return nil
	}
	return eventDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(eventDataBucket))
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		for _, e := range batch {
			if err := putEvent(b, e); err != nil {
				return err
			}
		}
		return pruneBucket(b, EventData, time.Now())
	})
}

// pruneBucket bounds a bucket's growth. Control uses a fixed count cap; data
// uses the configurable count + age caps. Age is applied first (cheap prefix
// compare) then count, so both ceilings hold.
func pruneBucket(b *bolt.Bucket, category string, now time.Time) error {
	if category == EventData {
		if age := AccessLogMaxAge(); age > 0 {
			if err := pruneByAge(b, now.Add(-age).UnixNano()); err != nil {
				return err
			}
		}
		return pruneByCount(b, AccessLogMaxEvents())
	}
	return pruneByCount(b, maxControlEvents)
}

// pruneByAge deletes records whose timestamp is older than cutoffNano. Keys
// sort by timestamp, so deletion stops at the first in-window record. Keys are
// collected before deletion so the cursor is never mutated mid-iteration.
func pruneByAge(b *bolt.Bucket, cutoffNano int64) error {
	var stale [][]byte
	c := b.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		if int64(binary.BigEndian.Uint64(k[0:8])) >= cutoffNano {
			break
		}
		stale = append(stale, append([]byte(nil), k...))
	}
	for _, k := range stale {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// pruneByCount deletes the oldest records until at most max remain. A
// non-positive max disables the cap. The count is taken with a cursor (not
// b.Stats, whose page-based KeyN lags an in-txn Put).
func pruneByCount(b *bolt.Bucket, max int) error {
	if max <= 0 {
		return nil
	}
	count := 0
	cnt := b.Cursor()
	for k, _ := cnt.First(); k != nil; k, _ = cnt.Next() {
		count++
	}
	over := count - max
	if over <= 0 {
		return nil
	}
	c := b.Cursor()
	for k, _ := c.First(); k != nil && over > 0; k, _ = c.Next() {
		if err := c.Delete(); err != nil {
			return err
		}
		over--
	}
	return nil
}

// QueryEvents returns up to limit events of the given category, newest-first.
// When beforeNano > 0, only events strictly older than it are returned, so a
// caller paginates by passing the timestamp of the oldest event it has seen.
func QueryEvents(category string, limit int, beforeNano int64) ([]Event, error) {
	if limit <= 0 {
		return nil, nil
	}
	var out []Event
	err := eventDB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketFor(category)))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Last(); k != nil && len(out) < limit; k, v = c.Prev() {
			nano := int64(binary.BigEndian.Uint64(k[0:8]))
			if beforeNano > 0 && nano >= beforeNano {
				continue
			}
			var e Event
			if err := json.Unmarshal(v, &e); err != nil {
				continue // skip a corrupt record rather than fail the whole query
			}
			out = append(out, e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RunEventFlusher drains buffered data events and writes them in batches until
// ctx is cancelled, then does a final bounded drain so a clean shutdown does not
// lose the last partial batch. A batch is flushed when it reaches maxBatch or
// when flushInterval elapses, whichever comes first — bounding both write
// amplification and worst-case visibility latency. Flush failures are logged,
// never fatal: a dropped batch must not take the process down.
func RunEventFlusher(ctx context.Context, flushInterval time.Duration, maxBatch int) {
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	batch := make([]Event, 0, maxBatch)
	flush := func() {
		if err := flushEvents(batch); err != nil {
			slog.Warn("event flush failed", "n", len(batch), "err", err.Error())
		}
		batch = batch[:0]
		if d := eventsDropped.Swap(0); d > 0 {
			slog.Warn("access-log events dropped: buffer full", "count", d)
		}
	}
	for {
		select {
		case <-ctx.Done():
			// Final drain. The channel is bounded by eventChanCap, so this loop
			// is statically bounded.
			for i := 0; i <= eventChanCap; i++ {
				select {
				case e := <-eventCh:
					batch = append(batch, e)
					if len(batch) >= maxBatch {
						flush()
					}
				default:
					flush()
					return
				}
			}
			flush()
			return
		case e := <-eventCh:
			batch = append(batch, e)
			if len(batch) >= maxBatch {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}
