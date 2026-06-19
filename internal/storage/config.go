package storage

import bolt "go.etcd.io/bbolt"

// configBucket is the BoltDB bucket holding operator settings as opaque blobs
// keyed by name. Storage stays a dumb key/value store here: it neither parses
// nor validates the values, so adding a new setting needs no storage change.
const configBucket = "Config"

// Config IO is routed through these package-level function values so a test can
// simulate a BoltDB read/write/delete failure and exercise the persistence error
// paths in the settings handlers (which otherwise only fire on real disk
// failure). Production always uses the Bolt-backed implementations below.
var (
	configGet    = boltGetConfigValue
	configPut    = boltPutConfigValue
	configDelete = boltDeleteConfigValue
)

// GetConfigValue returns the stored bytes for key, or (nil, nil) when the key
// is absent. The returned slice is a copy safe to retain after the read txn:
// Bolt's value is only valid for the transaction's lifetime.
func GetConfigValue(key string) ([]byte, error) { return configGet(key) }

// PutConfigValue stores val under key, replacing any existing value.
func PutConfigValue(key string, val []byte) error { return configPut(key, val) }

// DeleteConfigValue removes key. Deleting an absent key is not an error, so
// callers can clear an override without first checking for its presence.
func DeleteConfigValue(key string) error { return configDelete(key) }

// SetConfigStoreFaultForTest makes config reads, writes and deletes return err
// until the returned restore func is called. It exists solely so the
// persistence-failure branches in the settings handlers can be covered by tests;
// production never calls it.
func SetConfigStoreFaultForTest(err error) (restore func()) {
	prevGet, prevPut, prevDelete := configGet, configPut, configDelete
	configGet = func(string) ([]byte, error) { return nil, err }
	configPut = func(string, []byte) error { return err }
	configDelete = func(string) error { return err }
	return func() {
		configGet, configPut, configDelete = prevGet, prevPut, prevDelete
	}
}

func boltGetConfigValue(key string) ([]byte, error) {
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

func boltPutConfigValue(key string, val []byte) error {
	return userDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(configBucket))
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		return b.Put([]byte(key), val)
	})
}

func boltDeleteConfigValue(key string) error {
	return userDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(configBucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(key))
	})
}
