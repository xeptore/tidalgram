package telegram

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/gotd/td/tg"
)

func messagePeerFrom(peer InputPeer) (MessagePeer, error) {
	switch p := peer.InputPeerClass.(type) {
	case *tg.InputPeerUser:
		return MessagePeer{
			Kind:       PeerKindUser,
			ID:         p.UserID,
			AccessHash: p.AccessHash,
		}, nil
	case *tg.InputPeerChat:
		return MessagePeer{
			Kind:       PeerKindChat,
			ID:         p.ChatID,
			AccessHash: 0,
		}, nil
	case *tg.InputPeerChannel:
		return MessagePeer{
			Kind:       PeerKindChannel,
			ID:         p.ChannelID,
			AccessHash: p.AccessHash,
		}, nil
	default:
		return MessagePeer{}, fmt.Errorf("unsupported telegram peer %T", peer.InputPeerClass)
	}
}

func uploadedDocumentFrom(document *tg.Document, messageID int, peer MessagePeer) (UploadedDocument, error) {
	if nil == document {
		return UploadedDocument{}, fmt.Errorf("telegram message %d has no document", messageID)
	}
	if document.ID == 0 {
		return UploadedDocument{}, fmt.Errorf("telegram message %d document id is empty", messageID)
	}
	if messageID == 0 {
		return UploadedDocument{}, fmt.Errorf("telegram document %d message id is empty", document.ID)
	}
	if len(document.FileReference) == 0 {
		return UploadedDocument{}, fmt.Errorf(
			"telegram message %d document %d has an empty file reference",
			messageID,
			document.ID,
		)
	}

	return UploadedDocument{
		ID:            document.ID,
		AccessHash:    document.AccessHash,
		FileReference: bytes.Clone(document.FileReference),
		Peer:          peer,
		MessageID:     messageID,
	}, nil
}

// UploadedDocumentFromUpdates reads the document Telegram assigned after a send.
func UploadedDocumentFromUpdates(updates tg.UpdatesClass, peer InputPeer) (UploadedDocument, error) {
	messagePeer, err := messagePeerFrom(peer)
	if nil != err {
		return UploadedDocument{}, err
	}

	list, err := updateList(updates)
	if nil != err {
		return UploadedDocument{}, err
	}

	for _, upd := range list {
		message, ok, err := messageFromUpdate(upd)
		if nil != err {
			return UploadedDocument{}, err
		}
		if !ok {
			continue
		}

		document, err := documentFromMessage(message)
		if nil != err {
			return UploadedDocument{}, err
		}

		return uploadedDocumentFrom(document, message.ID, messagePeer)
	}

	return UploadedDocument{}, errors.New("sent telegram updates do not contain a document")
}

func updateList(updates tg.UpdatesClass) ([]tg.UpdateClass, error) {
	switch updates := updates.(type) {
	case *tg.Updates:
		return updates.Updates, nil
	case *tg.UpdatesCombined:
		return updates.Updates, nil
	default:
		return nil, fmt.Errorf("unexpected telegram updates %T", updates)
	}
}

func messageFromUpdate(upd tg.UpdateClass) (*tg.Message, bool, error) {
	var class tg.MessageClass
	switch upd := upd.(type) {
	case *tg.UpdateNewMessage:
		class = upd.Message
	case *tg.UpdateNewChannelMessage:
		class = upd.Message
	default:
		return nil, false, nil
	}

	message, ok := class.(*tg.Message)
	if !ok {
		return nil, true, fmt.Errorf("sent telegram message is %T", class)
	}

	return message, true, nil
}

// RefreshedUploadedDocument extracts a fresh file reference for cached from
// the history result that contains cached.MessageID.
func RefreshedUploadedDocument(
	cached UploadedDocument,
	result tg.MessagesMessagesClass,
) (UploadedDocument, error) {
	messages, err := historyMessages(result)
	if nil != err {
		return UploadedDocument{}, err
	}

	sawEmpty := false
	for _, item := range messages {
		switch message := item.(type) {
		case *tg.MessageEmpty:
			if message.ID == cached.MessageID || message.ID == 0 {
				sawEmpty = true
			}
		case *tg.Message:
			if message.ID != cached.MessageID {
				continue
			}

			document, err := documentFromMessage(message)
			if nil != err {
				return UploadedDocument{}, err
			}
			if document.ID != cached.ID {
				return UploadedDocument{}, fmt.Errorf(
					"telegram message %d has document %d, expected %d",
					message.ID,
					document.ID,
					cached.ID,
				)
			}

			return uploadedDocumentFrom(document, cached.MessageID, cached.Peer)
		default:
			continue
		}
	}

	if sawEmpty || len(messages) == 0 {
		return UploadedDocument{}, fmt.Errorf(
			"telegram message %d is not accessible: %w",
			cached.MessageID,
			ErrMessageNotAccessible,
		)
	}

	return UploadedDocument{}, fmt.Errorf("telegram history does not contain message %d", cached.MessageID)
}

func historyMessages(result tg.MessagesMessagesClass) ([]tg.MessageClass, error) {
	switch result := result.(type) {
	case *tg.MessagesMessages:
		return result.Messages, nil
	case *tg.MessagesMessagesSlice:
		return result.Messages, nil
	case *tg.MessagesChannelMessages:
		return result.Messages, nil
	case *tg.MessagesMessagesNotModified:
		return nil, errors.New("telegram returned messagesNotModified")
	default:
		return nil, fmt.Errorf("unexpected telegram messages result %T", result)
	}
}

func documentFromMessage(message *tg.Message) (*tg.Document, error) {
	if nil == message.Media {
		return nil, fmt.Errorf("telegram message %d has no media", message.ID)
	}

	media, ok := message.Media.(*tg.MessageMediaDocument)
	if !ok {
		return nil, fmt.Errorf("telegram message %d media is %T, expected document", message.ID, message.Media)
	}
	if nil == media.Document {
		return nil, fmt.Errorf("telegram message %d document is empty", message.ID)
	}

	document, ok := media.Document.(*tg.Document)
	if !ok {
		return nil, fmt.Errorf("telegram message %d document is %T", message.ID, media.Document)
	}
	if len(document.FileReference) == 0 {
		return nil, fmt.Errorf(
			"telegram message %d document %d has an empty file reference",
			message.ID,
			document.ID,
		)
	}

	return document, nil
}
