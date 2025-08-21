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
			ParseMode: gotgbot.ParseModeMarkdown,
			ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
				MessageId: u.EffectiveMessage.MessageId,
			},
		}
		chatID := u.EffectiveMessage.Chat.Id

		link := tidal.ParseLink(u.EffectiveMessage.Text)
		if err := t.TryDownloadLink(ctx, link); nil != err {
			if errors.Is(err, context.DeadlineExceeded) {
				msg := "Download request timed out. You might need to increase the timeout."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, context.Canceled) {
				msg := "Bot is shutting down. Download was not completed. Try again after bot restart."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrDownloadInProgress) {
				msg := "Another download is in progress. Try again later."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrLoginRequired) {
				msg := "Tidal login required."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrTokenRefreshed) {
				msg := "Tidal login token just got refreshed. Try again now."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrUnsupportedArtistLinkKind) {
				msg := "Artist links are not supported yet."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrUnsupportedVideoLinkKind) {
				msg := "Video links are not supported yet."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			return fmt.Errorf("failed to download link: %v", err)
		}

		msg := "Tidal link downloaded. Uploading to Telegram."
		if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
			return fmt.Errorf("failed to send message: %v", err)
		}

		// TODO: upload

		return nil
	}
}

func NewStartCommandHandler(ctx context.Context, adminID int64) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		sendOpt := &gotgbot.SendMessageOpts{ //nolint:exhaustruct
			ParseMode: gotgbot.ParseModeMarkdown,
			ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
				MessageId: u.EffectiveMessage.MessageId,
			},
		}
		senderID := u.EffectiveSender.Id()
		chatID := u.EffectiveMessage.Chat.Id

		if senderID == adminID {
			if _, err := b.SendMessage(chatID, "Hello, Papa!", sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %v", err)
			}

			return nil
		}

		if _, err := b.SendMessage(chatID, "Hello!", sendOpt); nil != err {
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
			ParseMode: gotgbot.ParseModeMarkdown,
			ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
				MessageId: u.EffectiveMessage.MessageId,
			},
		}
		chatID := u.EffectiveMessage.Chat.Id

		link, wait, err := t.TryInitiateLoginFlow(ctx)
		if nil != err {
			if errors.Is(err, context.DeadlineExceeded) {
				msg := "Tidal login request timed out. You might need to increase the timeout. Necessary information is logged."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, context.Canceled) {
				msg := "Bot is shutting down. Login flow is not completed."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrLoginInProgress) {
				msg := "Tidal login flow is already in progress. You might need to wait for it to complete."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			msg := "Failed to initiate login flow. Necessary information is logged."
			if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
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
		if _, err = b.SendMessage(chatID, msg, sendOpt); nil != err {
			return fmt.Errorf("failed to send message: %v", err)
		}

		if err := <-wait; nil != err {
			if errors.Is(err, tidal.ErrLoginLinkExpired) {
				msg := "Login link expired. You might need to start the login flow again."
				if _, err = b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			if errors.Is(err, context.Canceled) {
				msg := "Bot is shutting down. Login flow is not completed."
				if _, err = b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %v", err)
				}

				return nil
			}

			msg := "Login wait failed due to unexpected error. See logs for details."
			if _, err = b.SendMessage(chatID, msg, sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %v", err)
			}

			return nil
		}

		msg = "Login successful. You can now use the bot to download Tidal links."
		if _, err = b.SendMessage(chatID, msg, sendOpt); nil != err {
			return fmt.Errorf("failed to send message: %v", err)
		}

		return nil
	}
}

func NewStatusCommandHandler(ctx context.Context, t *tidal.Client) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		// Report Tidal auth status
		// Report worker job processing status
		panic("not implemented")
	}
}
