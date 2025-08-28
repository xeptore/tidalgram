package telegram

import (
	"context"
	"fmt"
	"time"

	"go.etcd.io/bbolt"
)

var (
	sessionBucketName = []byte("session")
	sessionKeyName    = []byte("session")
)

type Storage struct {
	db *bbolt.DB
}

func NewStorage(path string) (*Storage, error) {
	opts := &bbolt.Options{ //nolint:exhaustruct
		NoFreelistSync: true,
		ReadOnly:       false,
		Timeout:        1 * time.Second,
		NoGrowSync:     false,
		FreelistType:   bbolt.FreelistArrayType,
	}
	db, err := bbolt.Open(path, 0o600, opts)
	if nil != err {
		return nil, fmt.Errorf("failed to open database: %v", err)
	}

	if err := createBuckets(db); nil != err {
		return nil, fmt.Errorf("failed to create buckets: %v", err)
	}

	return &Storage{db: db}, nil
}

func createBuckets(db *bbolt.DB) error {
	err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(sessionBucketName)
		if nil != err {
			return fmt.Errorf("failed to create session bucket: %v", err)
		}

		return nil
	})
	if nil != err {
		return fmt.Errorf("failed to create buckets: %v", err)
	}

	return nil
}

func (s *Storage) Close() error {
	if err := s.db.Close(); nil != err {
		return fmt.Errorf("failed to close database: %v", err)
	}

	return nil
}

func (s *Storage) LoadSession(_ context.Context) ([]byte, error) {
	var session []byte
	err := s.db.View(func(tx *bbolt.Tx) error {
		session = tx.Bucket(sessionBucketName).Get(sessionKeyName)
		return nil
	})
	if nil != err {
		return nil, fmt.Errorf("failed to load session: %v", err)
	}

	return session, nil
}

func (s *Storage) StoreSession(_ context.Context, session []byte) error {
	err := s.db.Update(func(tx *bbolt.Tx) error {
		if err := tx.Bucket(sessionBucketName).Put(sessionKeyName, session); nil != err {
			return fmt.Errorf("failed to store session: %v", err)
		}

		return nil
	})
	if nil != err {
		return fmt.Errorf("failed to store session: %v", err)
	}

	return nil
}

func (s *Storage) DeleteSession(_ context.Context) error {
	err := s.db.Update(func(tx *bbolt.Tx) error {
		if err := tx.Bucket(sessionBucketName).Delete(sessionKeyName); nil != err {
			return fmt.Errorf("failed to delete session: %v", err)
		}

		return nil
	})
	if nil != err {
		return fmt.Errorf("failed to delete session: %v", err)
	}

	return nil
}
