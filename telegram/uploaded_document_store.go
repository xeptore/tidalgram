package telegram

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/goccy/go-json"
	"go.etcd.io/bbolt"
)

// uploadedAudioBucketName is the root bucket in telegram.db.
//
// Schema:
//
//	uploaded_audio/<trackID>/<sha256> = JSON document
//
// Each track bucket holds the documents uploaded for that track. Putting a
// digest replaces that entry and deletes every other digest in the bucket.
var uploadedAudioBucketName = []byte("uploaded_audio")

type storedUploadedDocument struct {
	ID             int64  `json:"id"`
	AccessHash     int64  `json:"access_hash"`
	FileReference  []byte `json:"file_reference"`
	PeerKind       int    `json:"peer_kind"`
	PeerID         int64  `json:"peer_id"`
	PeerAccessHash int64  `json:"peer_access_hash"`
	MessageID      int    `json:"message_id"`
}

func (s *Storage) GetUploadedDocument(trackID string, digest string) (UploadedDocument, bool, error) {
	if err := validateUploadedDocumentIdentity(trackID, digest); nil != err {
		return UploadedDocument{}, false, err
	}

	var (
		doc   UploadedDocument
		found bool
	)
	err := s.db.View(func(tx *bbolt.Tx) error {
		trackBucket := uploadedTrackBucket(tx, trackID)
		if nil == trackBucket {
			return nil
		}

		raw := trackBucket.Get([]byte(digest))
		if nil == raw {
			return nil
		}

		parsed, err := decodeUploadedDocument(raw)
		if nil != err {
			return err
		}

		doc = parsed
		found = true

		return nil
	})
	if nil != err {
		return UploadedDocument{}, false, fmt.Errorf("read uploaded document: %w", err)
	}

	return doc, found, nil
}

func (s *Storage) PutUploadedDocument(trackID string, digest string, doc UploadedDocument) error {
	if err := validateUploadedDocumentIdentity(trackID, digest); nil != err {
		return err
	}
	if err := validateUploadedDocument(doc); nil != err {
		return err
	}

	payload, err := json.Marshal(storedUploadedDocument{
		ID:             doc.ID,
		AccessHash:     doc.AccessHash,
		FileReference:  doc.FileReference,
		PeerKind:       int(doc.Peer.Kind),
		PeerID:         doc.Peer.ID,
		PeerAccessHash: doc.Peer.AccessHash,
		MessageID:      doc.MessageID,
	})
	if nil != err {
		return fmt.Errorf("encode uploaded document: %w", err)
	}

	err = s.db.Update(func(tx *bbolt.Tx) error {
		root := tx.Bucket(uploadedAudioBucketName)
		if nil == root {
			return errors.New("uploaded audio bucket is missing")
		}

		trackBucket, err := root.CreateBucketIfNotExists([]byte(trackID))
		if nil != err {
			return fmt.Errorf("create track bucket: %w", err)
		}

		if err := trackBucket.Put([]byte(digest), payload); nil != err {
			return fmt.Errorf("store uploaded document: %w", err)
		}

		if err := deleteOtherDigests(trackBucket, []byte(digest)); nil != err {
			return err
		}

		return nil
	})
	if nil != err {
		return fmt.Errorf("store uploaded document: %w", err)
	}

	return nil
}

func uploadedTrackBucket(tx *bbolt.Tx, trackID string) *bbolt.Bucket {
	root := tx.Bucket(uploadedAudioBucketName)
	if nil == root {
		return nil
	}

	return root.Bucket([]byte(trackID))
}

func deleteOtherDigests(bucket *bbolt.Bucket, keep []byte) error {
	var stale [][]byte
	err := bucket.ForEach(func(key []byte, _ []byte) error {
		if bytes.Equal(key, keep) {
			return nil
		}

		stale = append(stale, bytes.Clone(key))

		return nil
	})
	if nil != err {
		return fmt.Errorf("list uploaded documents: %w", err)
	}

	for _, key := range stale {
		if err := bucket.Delete(key); nil != err {
			return fmt.Errorf("delete stale uploaded document: %w", err)
		}
	}

	return nil
}

func decodeUploadedDocument(raw []byte) (UploadedDocument, error) {
	var stored storedUploadedDocument
	if err := json.Unmarshal(raw, &stored); nil != err {
		return UploadedDocument{}, fmt.Errorf("decode uploaded document: %w", err)
	}

	kind, err := peerKindFromStored(stored.PeerKind)
	if nil != err {
		return UploadedDocument{}, err
	}

	doc := UploadedDocument{
		ID:            stored.ID,
		AccessHash:    stored.AccessHash,
		FileReference: bytes.Clone(stored.FileReference),
		Peer: MessagePeer{
			Kind:       kind,
			ID:         stored.PeerID,
			AccessHash: stored.PeerAccessHash,
		},
		MessageID: stored.MessageID,
	}
	if err := validateUploadedDocument(doc); nil != err {
		return UploadedDocument{}, err
	}

	return doc, nil
}

func validateUploadedDocumentIdentity(trackID string, digest string) error {
	if len(trackID) == 0 {
		return errors.New("track id is empty")
	}
	if len(digest) == 0 {
		return errors.New("content sha256 is empty")
	}

	return nil
}

func validateUploadedDocument(doc UploadedDocument) error {
	if doc.ID == 0 {
		return errors.New("uploaded document id is empty")
	}
	if doc.MessageID == 0 {
		return errors.New("uploaded document message id is empty")
	}
	if len(doc.FileReference) == 0 {
		return errors.New("uploaded document file reference is empty")
	}

	switch doc.Peer.Kind {
	case PeerKindUser, PeerKindChat, PeerKindChannel:
		return nil
	default:
		return fmt.Errorf("invalid telegram peer kind %d", doc.Peer.Kind)
	}
}

func peerKindFromStored(kind int) (PeerKind, error) {
	switch PeerKind(kind) {
	case PeerKindUser, PeerKindChat, PeerKindChannel:
		return PeerKind(kind), nil
	default:
		return 0, fmt.Errorf("invalid telegram peer kind %d", kind)
	}
}
