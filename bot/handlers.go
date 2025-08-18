package bot

import (
	"context"
	"errors"
	"fmt"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"

	"github.com/xeptore/tidalgram/tidal"
)

var ErrNotAdmin = errors.New("sender is not an admin")

func NewChainHandler(handlers ...handlers.Response) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		for _, handler := range handlers {
			if err := handler(b, u); nil != err {
				return err
			}
		}

		return nil
	}
}

func NewAdminOnlyGuard(adminID int64) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		senderID := u.EffectiveSender.Id()
		if senderID != adminID {
			return ErrNotAdmin
		}

		return nil
	}
}

func NewTidalURLHandler(ctx context.Context, tidal *tidal.Client) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		link := u.EffectiveMessage.Text
		_, err := tidal.DownloadLink(ctx, link)
		if nil != err {
			// handle different errors
			return fmt.Errorf("failed to download link: %v", err)
		}
		_, err = b.SendMessageWithContext(
			ctx,
			u.EffectiveMessage.Chat.Id,
			"link downloaded",
			&gotgbot.SendMessageOpts{ //nolint:exhaustruct
				ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
					MessageId: u.EffectiveMessage.MessageId,
				},
			},
		)
		if nil != err {
			return fmt.Errorf("failed to send message: %v", err)
		}

		return nil
	}
}

func NewStartCommandHandler(ctx context.Context, adminID int64) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		senderID := u.EffectiveSender.Id()
		if senderID != adminID {
			_, err := b.SendMessageWithContext(
				context.Background(),
				u.EffectiveMessage.Chat.Id,
				"Hello!",
				&gotgbot.SendMessageOpts{ //nolint:exhaustruct
					ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
						MessageId: u.EffectiveMessage.MessageId,
					},
				},
			)
			if nil != err {
				return fmt.Errorf("failed to send message: %v", err)
			}

			return nil
		}
		_, err := b.SendMessageWithContext(
			context.Background(),
			u.EffectiveMessage.Chat.Id,
			"Hello admin!",
			&gotgbot.SendMessageOpts{ //nolint:exhaustruct
				ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
					MessageId: u.EffectiveMessage.MessageId,
				},
			},
		)
		if nil != err {
			return fmt.Errorf("failed to send message: %v", err)
		}

		return nil
	}
}

func NewCancelCommandHandler(ctx context.Context, tidal *tidal.Client) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		panic("not implemented")
	}
}

func NewAuthorizeCommandHandler(ctx context.Context, tidal *tidal.Client) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		panic("not implemented")
	}
}

func NewStatusCommandHandler(ctx context.Context, tidal *tidal.Client) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		// Report Tidal auth status
		// Report worker job processing status
		panic("not implemented")
	}
}
