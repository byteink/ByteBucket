package storage

import (
	"encoding/binary"
	"encoding/json"

	"github.com/boltdb/bolt"
)

// auditBucket holds the control-plane audit trail: one record per administrative
// mutation (user CRUD, config changes). Keys are 16 bytes — an 8-byte
// big-endian unix-nano timestamp followed by an 8-byte per-bucket sequence — so
// records sort chronologically and two events in the same nanosecond never
// collide.
const auditBucket = "AuditLog"

// maxAuditEvents caps the trail so an embedded store cannot grow without bound.
// A var (not const) so tests can shrink it; oldest records are pruned first.
var maxAuditEvents = 10000

// AuditEvent is one administrative action. Target and Detail are optional
// context (the affected user/bucket/config key, and a short human note).
type AuditEvent struct {
	TimeUnixNano int64  `json:"ts"`
	Actor        string `json:"actor"`
	Action       string `json:"action"`
	Target       string `json:"target"`
	Detail       string `json:"detail"`
}

func encodeAuditKey(nano int64, seq uint64) []byte {
	k := make([]byte, 16)
	binary.BigEndian.PutUint64(k[0:8], uint64(nano))
	binary.BigEndian.PutUint64(k[8:16], seq)
	return k
}

// AppendAuditEvent records one event and prunes the oldest entries past the cap.
func AppendAuditEvent(e AuditEvent) error {
	return userDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(auditBucket))
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		seq, err := b.NextSequence()
		if err != nil {
			return err
		}
		val, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if err := b.Put(encodeAuditKey(e.TimeUnixNano, seq), val); err != nil {
			return err
		}
		return pruneAudit(b)
	})
}

// pruneAudit deletes the oldest records until the bucket is within the cap.
// The count is taken with a cursor (not b.Stats, whose page-based KeyN does not
// reflect the just-Put key inside this write transaction).
func pruneAudit(b *bolt.Bucket) error {
	count := 0
	cnt := b.Cursor()
	for k, _ := cnt.First(); k != nil; k, _ = cnt.Next() {
		count++
	}
	over := count - maxAuditEvents
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

// QueryAuditEvents returns up to limit events newest-first. When beforeNano > 0,
// only events strictly older than it are returned, so a caller paginates by
// passing the timestamp of the oldest event it has seen.
func QueryAuditEvents(limit int, beforeNano int64) ([]AuditEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	var out []AuditEvent
	err := userDB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(auditBucket))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Last(); k != nil && len(out) < limit; k, v = c.Prev() {
			nano := int64(binary.BigEndian.Uint64(k[0:8]))
			if beforeNano > 0 && nano >= beforeNano {
				continue
			}
			var e AuditEvent
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
