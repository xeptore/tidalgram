package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"

	"github.com/xeptore/tidalgram/tidal"
)

var ErrNotPapa = errors.New("sender is not papa")

func NewChainHandler(handlers ...handlers.Response) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		for _, handler := range handlers {
			if err := handler(b, u); nil != err {
				if errors.Is(err, ErrNotPapa) {
					return nil
				}

				return err
			}
		}

		return nil
	}
}

func NewPapaOnlyGuard(papaID int64) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		senderID := u.EffectiveSender.Id()
		if senderID != papaID {
			return ErrNotPapa
		}

		return nil
	}
}

func NewTidalURLHandler(ctx context.Context, t *tidal.Client) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		sendOpt := &gotgbot.SendMessageOpts{ //nolint:exhaustruct
			ParseMode: gotgbot.ParseModeMarkdownV2,
			ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
				MessageId: u.EffectiveMessage.MessageId,
			},
		}
		chatID := u.EffectiveMessage.Chat.Id

		link := tidal.ParseLink(u.EffectiveMessage.Text)
		if err := t.TryDownloadLink(ctx, link); nil != err {
			if errors.Is(err, context.DeadlineExceeded) {
				if _, err := b.SendMessageWithContext(ctx, chatID, "Download request timed out. You might need to increase the timeout.", sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrDownloadInProgress) {
				if _, err := b.SendMessageWithContext(ctx, chatID, "Another download is in progress. Try again later.", sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrLoginRequired) {
				if _, err := b.SendMessageWithContext(ctx, chatID, "Tidal login required.", sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrTokenRefreshed) {
				if _, err := b.SendMessageWithContext(ctx, chatID, "Tidal login token just got refreshed. Try again now.", sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrUnsupportedArtistLinkKind) {
				if _, err := b.SendMessageWithContext(ctx, chatID, "Artist links are not supported yet.", sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrUnsupportedVideoLinkKind) {
				if _, err := b.SendMessageWithContext(ctx, chatID, "Video links are not supported yet.", sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			return fmt.Errorf("failed to download link: %v", err)
		}
		if _, err := b.SendMessageWithContext(ctx, chatID, "Tidal link downloaded. Uploading to Telegram...", sendOpt); nil != err {
			return fmt.Errorf("failed to send message: %v", err)
		}

		// TODO: upload

		return nil
	}
}

func NewStartCommandHandler(ctx context.Context, adminID int64) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		sendOpt := &gotgbot.SendMessageOpts{ //nolint:exhaustruct
			ParseMode: gotgbot.ParseModeMarkdownV2,
			ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
				MessageId: u.EffectiveMessage.MessageId,
			},
		}
		senderID := u.EffectiveSender.Id()
		if senderID != adminID {
			if _, err := b.SendMessageWithContext(ctx, u.EffectiveMessage.Chat.Id, "Hello!", sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %v", err)
			}

			return nil
		}
		if _, err := b.SendMessageWithContext(ctx, u.EffectiveMessage.Chat.Id, "Hello, papa!", sendOpt); nil != err {
			return fmt.Errorf("failed to send message: %v", err)
		}

		return nil
	}
}

func NewCancelCommandHandler(ctx context.Context, t *tidal.Client) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		panic("not implemented")
	}
}

func NewAuthorizeCommandHandler(ctx context.Context, t *tidal.Client) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		sendOpt := &gotgbot.SendMessageOpts{ //nolint:exhaustruct
			ParseMode: gotgbot.ParseModeMarkdownV2,
			ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
				MessageId: u.EffectiveMessage.MessageId,
			},
		}
		chatID := u.EffectiveMessage.Chat.Id

		link, wait, err := t.TryInitiateLoginFlow(ctx)
		if nil != err {
			if errors.Is(err, context.DeadlineExceeded) {
				if _, err := b.SendMessageWithContext(ctx, chatID, "Tidal login request timed out. You might need to increase the timeout. Necessary information is logged.", sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				if errors.Is(err, tidal.ErrLoginInProgress) {
					if _, err := b.SendMessageWithContext(ctx, chatID, "Tidal login flow is already in progress. You might need to wait for it to complete.", sendOpt); nil != err {
						return fmt.Errorf("failed to send message: %v", err)
					}

					return nil
				}

				return nil
			}

			if _, err := b.SendMessageWithContext(ctx, chatID, "Failed to initiate login flow. Necessary information is logged.", sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %v", err)
			}

			return fmt.Errorf("failed to initiate login flow: %v", err)
		}

		msg := strings.Join(
			[]string{
				"Tidal login flow initiated. Please visit the following link to authorize the bot:",
				link.URL,
				"",
				"The link will expire in **" + link.ExpiresIn.String() + "**.",
				"You will be notified when the login flow is complete.",
			},
			"\n",
		)
		if _, err = b.SendMessageWithContext(ctx, chatID, msg, sendOpt); nil != err {
			return fmt.Errorf("failed to send message: %v", err)
		}

		select {
		case <-ctx.Done():
			if _, err = b.SendMessageWithContext(ctx, chatID, "Bot is shutting down. Login flow is not completed.", sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %v", err)
			}

			return nil
		case err := <-wait:
			if nil != err {
				if errors.Is(err, tidal.ErrLoginLinkExpired) {
					msg := "Login link expired. You might need to start the login flow again."
					if _, err = b.SendMessageWithContext(ctx, chatID, msg, sendOpt); nil != err {
						return fmt.Errorf("failed to send message: %v", err)
					}

					return nil
				}

				if _, err = b.SendMessageWithContext(ctx, chatID, "Login wait failed due to unexpected error. See logs for details.", sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if _, err = b.SendMessageWithContext(ctx, chatID, "Login successful. You can now use the bot to download Tidal links.", sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %v", err)
			}

			return nil
		}
	}
}

func NewStatusCommandHandler(ctx context.Context, t *tidal.Client) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		// Report Tidal auth status
		// Report worker job processing status
		panic("not implemented")
	}
}
