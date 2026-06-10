package storage

import bolt "go.etcd.io/bbolt"

// configBucket is the BoltDB bucket holding operator settings as opaque blobs
// keyed by name. Storage stays a dumb key/value store here: it neither parses
// nor validates the values, so adding a new setting needs no storage change.
const configBucket = "Config"

// GetConfigValue returns the stored bytes for key, or (nil, nil) when the key
// is absent. The returned slice is a copy safe to retain after the read txn:
// Bolt's value is only valid for the transaction's lifetime.
func GetConfigValue(key string) ([]byte, error) {
	var out []byte
	err := userDB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(configBucket))
		if b == nil {
			return nil
		}
		v := b.Get([]byte(key))
		if v == nil {
			return nil
		}
		out = append([]byte(nil), v...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PutConfigValue stores val under key, replacing any existing value.
func PutConfigValue(key string, val []byte) error {
	return userDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(configBucket))
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		return b.Put([]byte(key), val)
	})
}

// DeleteConfigValue removes key. Deleting an absent key is not an error, so
// callers can clear an override without first checking for its presence.
func DeleteConfigValue(key string) error {
	return userDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(configBucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(key))
	})
}
