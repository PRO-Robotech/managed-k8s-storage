package store

import (
	"encoding/json"
	bolt "go.etcd.io/bbolt"
	"os"
	"path/filepath"
	"time"
)

type DB struct{ *bolt.DB }

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{"state", "audit"} {
			if _, e := tx.CreateBucketIfNotExists([]byte(name)); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db}, nil
}
func Read(tx *bolt.Tx, key string, out any) error {
	b := tx.Bucket([]byte("state")).Get([]byte(key))
	if b == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}
func Write(tx *bolt.Tx, key string, in any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return tx.Bucket([]byte("state")).Put([]byte(key), b)
}
func Audit(tx *bolt.Tx, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	bucket := tx.Bucket([]byte("audit"))
	seq, err := bucket.NextSequence()
	if err != nil {
		return err
	}
	return bucket.Put([]byte(time.Now().UTC().Format(time.RFC3339Nano)+"/"+fmtSeq(seq)), b)
}
func fmtSeq(n uint64) string {
	const digits = "0123456789abcdef"
	b := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		b[i] = digits[n&15]
		n >>= 4
	}
	return string(b)
}
