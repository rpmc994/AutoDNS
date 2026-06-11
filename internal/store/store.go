// Package store wraps bbolt for all persistence needs: settings and log history.
package store

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"autodns/internal/store/models"
)

var (
	bucketSettings = []byte("settings")
	bucketLogs     = []byte("logs")
	keySettings    = []byte("config")
)

const maxLogEntries = 20

// Store is the single access point to the bbolt database.
type Store struct {
	db *bolt.DB
}

// Open opens (or creates) the bbolt database at path.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("opening bolt db: %w", err)
	}

	// Ensure buckets exist.
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketSettings, bucketLogs} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return fmt.Errorf("creating bucket %s: %w", name, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// --- Settings ---

// GetSettings returns the stored settings, or sensible defaults if none exist.
func (s *Store) GetSettings() (models.Settings, error) {
	var cfg models.Settings
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSettings)
		v := b.Get(keySettings)
		if v == nil {
			// Return defaults.
			cfg = models.Settings{IntervalMin: 15}
			return nil
		}
		return json.Unmarshal(v, &cfg)
	})
	if err != nil {
		return cfg, fmt.Errorf("reading settings: %w", err)
	}
	return cfg, nil
}

// SaveSettings persists settings to the database.
func (s *Store) SaveSettings(cfg models.Settings) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshalling settings: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSettings).Put(keySettings, data)
	})
}

// --- Log Entries ---

// AppendLog writes a new log entry and prunes entries beyond maxLogEntries.
func (s *Store) AppendLog(entry models.LogEntry) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketLogs)

		// Auto-increment ID.
		id, err := b.NextSequence()
		if err != nil {
			return fmt.Errorf("next sequence: %w", err)
		}
		entry.ID = id

		data, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("marshalling log entry: %w", err)
		}

		key := itob(id)
		if err := b.Put(key, data); err != nil {
			return fmt.Errorf("writing log entry: %w", err)
		}

		// Prune oldest entries so we never keep more than maxLogEntries.
		count := b.Stats().KeyN
		if count > maxLogEntries {
			c := b.Cursor()
			for k, _ := c.First(); k != nil && count > maxLogEntries; k, _ = c.Next() {
				if err := b.Delete(k); err != nil {
					return fmt.Errorf("pruning log: %w", err)
				}
				count--
			}
		}

		return nil
	})
}

// GetLastSuccessIP returns the NewIP from the most recent "Success" log entry,
// or an empty string if no such entry exists. Used to seed the monitor's
// last-known IP on startup to prevent a spurious Cloudflare update.
func (s *Store) GetLastSuccessIP() (string, error) {
	var ip string
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketLogs)
		c := b.Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var e models.LogEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return fmt.Errorf("unmarshalling log entry: %w", err)
			}
			if e.Status == "Success" {
				ip = e.NewIP
				return nil
			}
		}
		return nil
	})
	return ip, err
}

// GetLogs returns all log entries ordered newest-first.
func (s *Store) GetLogs() ([]models.LogEntry, error) {
	entries := []models.LogEntry{}
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketLogs)
		c := b.Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var e models.LogEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return fmt.Errorf("unmarshalling log entry: %w", err)
			}
			entries = append(entries, e)
		}
		return nil
	})
	return entries, err
}

// itob converts a uint64 to an 8-byte big-endian key suitable for bbolt.
func itob(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}
