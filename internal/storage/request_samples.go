package storage

import (
	"encoding/binary"

	bolt "go.etcd.io/bbolt"
)

// requestSamplesBucket holds per-minute deltas of S3 request outcomes, keyed by
// the 8-byte big-endian unix-minute timestamp so a cursor walks them in
// chronological order. Values are a fixed 12-byte triple of uint32 counts
// (2xx, 4xx, 5xx) — compact enough that 30 days at one sample/minute stays
// under ~600 KiB, with no per-record parse cost.
const requestSamplesBucket = "RequestSamples"

const requestSampleValueLen = 12 // 3 x uint32

// RequestSample is one minute's worth of S3 request-outcome counts. MinuteUnix
// is the unix time of the minute (seconds, already truncated to a minute
// boundary by the caller).
type RequestSample struct {
	MinuteUnix int64
	C2xx       uint32
	C4xx       uint32
	C5xx       uint32
}

// encodeSampleKey renders a minute timestamp as an order-preserving key. Times
// are non-negative, so the unsigned big-endian encoding sorts identically to
// the signed value.
func encodeSampleKey(minuteUnix int64) []byte {
	k := make([]byte, 8)
	binary.BigEndian.PutUint64(k, uint64(minuteUnix))
	return k
}

func encodeSampleValue(s RequestSample) []byte {
	v := make([]byte, requestSampleValueLen)
	binary.BigEndian.PutUint32(v[0:4], s.C2xx)
	binary.BigEndian.PutUint32(v[4:8], s.C4xx)
	binary.BigEndian.PutUint32(v[8:12], s.C5xx)
	return v
}

// PutRequestSample stores one minute's counts, replacing any existing record
// for the same minute.
func PutRequestSample(s RequestSample) error {
	return userDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(requestSamplesBucket))
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		return b.Put(encodeSampleKey(s.MinuteUnix), encodeSampleValue(s))
	})
}

// QueryRequestSamples returns the samples whose minute falls in [fromUnix,
// toUnix) in chronological order. A missing bucket or empty range yields an
// empty slice, not an error.
func QueryRequestSamples(fromUnix, toUnix int64) ([]RequestSample, error) {
	var out []RequestSample
	err := userDB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(requestSamplesBucket))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		start := encodeSampleKey(fromUnix)
		end := encodeSampleKey(toUnix)
		for k, v := c.Seek(start); k != nil && string(k) < string(end); k, v = c.Next() {
			if len(v) != requestSampleValueLen {
				continue // defensively skip a malformed record rather than panic
			}
			out = append(out, RequestSample{
				MinuteUnix: int64(binary.BigEndian.Uint64(k)),
				C2xx:       binary.BigEndian.Uint32(v[0:4]),
				C4xx:       binary.BigEndian.Uint32(v[4:8]),
				C5xx:       binary.BigEndian.Uint32(v[8:12]),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PruneRequestSamples removes every sample strictly older than beforeUnix,
// bounding store growth to the retention window. Keys are collected first and
// deleted in a second pass so the cursor is never mutated mid-iteration.
func PruneRequestSamples(beforeUnix int64) error {
	return userDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(requestSamplesBucket))
		if b == nil {
			return nil
		}
		cutoff := encodeSampleKey(beforeUnix)
		var stale [][]byte
		c := b.Cursor()
		for k, _ := c.First(); k != nil && string(k) < string(cutoff); k, _ = c.Next() {
			stale = append(stale, append([]byte(nil), k...))
		}
		for _, k := range stale {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}
