package telegram

import (
	"context"
	"errors"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// UploadedDocument is a Telegram document that can be sent again without
// uploading the audio bytes. Peer and MessageID locate a message that still
// contains the document so an expired file reference can be refreshed.
type UploadedDocument struct {
	ID            int64
	AccessHash    int64
	FileReference []byte
	Peer          MessagePeer
	MessageID     int
}

// MessagePeer is the chat that holds the message used to refresh FileReference.
type MessagePeer struct {
	Kind       PeerKind
	ID         int64
	AccessHash int64
}

type PeerKind int

const (
	PeerKindUser PeerKind = iota + 1
	PeerKindChat
	PeerKindChannel
)

// ErrMessageNotAccessible means the Telegram message that held a cached
// document can no longer be retrieved, so the audio must be uploaded again.
var ErrMessageNotAccessible = errors.New("telegram message is not accessible")

// IsFileReferenceError reports Telegram file-reference failures.
// Digit suffixes such as FILE_REFERENCE_0_EXPIRED are included: gotd strips
// the numeric argument before comparing the error type.
func IsFileReferenceError(err error) bool {
	return tgerr.Is(
		err,
		tg.ErrFileReferenceEmpty,
		tg.ErrFileReferenceExpired,
		tg.ErrFileReferenceInvalid,
	)
}

// IsMessageNotAccessible reports errors that mean the cached message cannot
// be loaded again. Timeouts, flood waits, cancellation, and server failures
// are not treated as inaccessible.
func IsMessageNotAccessible(err error) bool {
	if nil == err {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrMessageNotAccessible) {
		return true
	}

	return tgerr.Is(
		err,
		tg.ErrChannelInvalid,
		tg.ErrChannelPrivate,
		tg.ErrChatIDInvalid,
		tg.ErrPeerIDInvalid,
		tg.ErrMessageIDInvalid,
		tg.ErrMsgIDInvalid,
	)
}
