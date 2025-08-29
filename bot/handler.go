package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
	"github.com/rs/zerolog"
	"golang.org/x/sync/semaphore"

	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/telegram"
	"github.com/xeptore/tidalgram/tidal"
)

var ErrNotPapa = errors.New("sender is not papa")

func NewChainHandler(handlers ...handlers.Response) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		for _, handler := range handlers {
			if err := handler(b, u); nil != err {
				if errors.Is(err, ErrNotPapa) {
					return ext.EndGroups
				}

				return err
			}
		}

		return ext.ContinueGroups
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

func NewTidalURLHandler(
	ctx context.Context,
	logger zerolog.Logger,
	td *tidal.Client,
	conf config.Bot,
	up *telegram.Uploader,
	worker *Worker,
) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		logger = logger.
			With().
			Int64("chat_id", u.EffectiveMessage.Chat.Id).
			Int64("message_id", u.EffectiveMessage.MessageId).
			Int64("sender_id", u.EffectiveSender.Id()).
			Logger()

		msgID := u.EffectiveMessage.MessageId
		sendOpt := &gotgbot.SendMessageOpts{ //nolint:exhaustruct
			ParseMode: gotgbot.ParseModeMarkdown,
			ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
				MessageId: msgID,
			},
		}
		chatID := u.EffectiveMessage.Chat.Id

		ctx, ok := worker.TryAcquireJob(ctx)
		if !ok {
			msg := "⏳ Another download is in progress. Try again later."
			if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %w", err)
			}

			return nil
		}
		defer worker.CancelJob()

		link := tidal.ParseLink(getMessageURL(u.EffectiveMessage))

		msg := "🚦 Downloading " + link.Kind.String() + " link..."
		if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
			return fmt.Errorf("failed to send message: %w", err)
		}

		logger.Debug().Str("link_id", link.ID).Str("link_kind", link.Kind.String()).Msg("Parsed link")
		if err := td.TryDownloadLink(ctx, logger, link); nil != err {
			if errors.Is(err, context.DeadlineExceeded) {
				msg := "⌛️ Download request timed out. You might need to increase the timeout."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %w", err)
				}

				return nil
			}

			if errors.Is(err, context.Canceled) {
				if cause := context.Cause(ctx); errors.Is(cause, ErrJobCanceled) {
					msg := "⏹️ Download was canceled."
					if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
						return fmt.Errorf("failed to send message: %w", err)
					}

					return nil
				}

				msg := "🛑 Bot is shutting down. Download was not completed. Try again after bot restart."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %w", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrLoginRequired) {
				msg := "🔑 Tidal login required. Use /authorize command to authorize the bot."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %w", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrTokenRefreshed) {
				msg := "🔄 Tidal login token just got refreshed. Retry now."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %w", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrUnsupportedArtistLinkKind) {
				msg := "🚫 Artist links are not supported yet."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %w", err)
				}

				return nil
			}

			if errors.Is(err, tidal.ErrUnsupportedVideoLinkKind) {
				msg := "🚫 Video links are not supported yet."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %w", err)
				}

				return nil
			}

			msg := "❌ Failed to download link. Insult logs for details."
			if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %w", err)
			}

			logger.Error().Err(err).Msg("failed to download link")

			return nil
		}

		msg = "📤 Tidal link downloaded. Uploading to Telegram."
		if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
			return fmt.Errorf("failed to send message: %w", err)
		}

		if err := up.Upload(ctx, logger, td.DownloadsDirFs, link); nil != err {
			if errors.Is(err, context.DeadlineExceeded) {
				msg := "⌛️ Upload request timed out. You might need to increase the timeout."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %w", err)
				}

				return nil
			}

			if errors.Is(err, context.Canceled) {
				msg := "🛑 Bot is shutting down. Upload was not completed. Try again after bot restart."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %w", err)
				}

				return nil
			}

			msg := "❌ Failed to upload to Telegram. Insult logs for details."
			if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %w", err)
			}

			logger.Error().Err(err).Msg("failed to upload to Telegram")

			return nil
		}

		msg = "✅ Tidal link was successfully uploaded."
		if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
			return fmt.Errorf("failed to send message: %w", err)
		}

		return nil
	}
}

func NewHelloCommandHandler(ctx context.Context, adminID int64) handlers.Response {
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
			if _, err := b.SendMessage(chatID, "Hello, Papa! 👋🏻", sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %w", err)
			}

			return nil
		}

		if _, err := b.SendMessage(chatID, "Hello! 👋🏻", sendOpt); nil != err {
			return fmt.Errorf("failed to send message: %w", err)
		}

		return nil
	}
}

func NewCancelCommandHandler(ctx context.Context, worker *Worker) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		worker.CancelJob()

		return nil
	}
}

func NewTidalLoginCommandHandler(ctx context.Context, logger zerolog.Logger, td *tidal.Client) handlers.Response {
	sem := semaphore.NewWeighted(1)

	return func(b *gotgbot.Bot, u *ext.Context) error {
		logger = logger.
			With().
			Int64("chat_id", u.EffectiveMessage.Chat.Id).
			Int64("message_id", u.EffectiveMessage.MessageId).
			Int64("sender_id", u.EffectiveSender.Id()).
			Logger()

		sendOpt := &gotgbot.SendMessageOpts{ //nolint:exhaustruct
			ParseMode: gotgbot.ParseModeMarkdown,
			ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
				MessageId: u.EffectiveMessage.MessageId,
			},
		}
		chatID := u.EffectiveMessage.Chat.Id

		if !sem.TryAcquire(1) {
			msg := "⏳ Another login flow is in progress. Try again later."
			if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %w", err)
			}

			return nil
		}
		defer sem.Release(1)

		link, wait, err := td.TryInitiateLoginFlow(ctx, logger)
		if nil != err {
			if errors.Is(err, context.DeadlineExceeded) {
				msg := "⏳ Tidal login request timed out. You might need to increase the timeout."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %w", err)
				}

				return nil
			}

			if errors.Is(err, context.Canceled) {
				msg := "🛑 Bot is shutting down. Login flow is not completed."
				if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %w", err)
				}

				return nil
			}

			msg := "❌ Failed to initiate login flow. Necessary information is logged."
			if _, err := b.SendMessage(chatID, msg, sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %w", err)
			}

			logger.Error().Err(err).Msg("failed to initiate login flow")

			return nil
		}

		msg := strings.Join(
			[]string{
				"🚀 Tidal login flow initiated. Please visit the following link to authorize the bot:",
				"🔗 " + link.URL,
				"",
				"⏳ The link will expire in **" + link.ExpiresIn.String() + "**.",
				"🔔 You will be notified when the login flow is complete.",
			},
			"\n",
		)
		if _, err = b.SendMessage(chatID, msg, sendOpt); nil != err {
			return fmt.Errorf("failed to send message: %w", err)
		}

		if err := <-wait; nil != err {
			if errors.Is(err, tidal.ErrLoginLinkExpired) {
				msg := "⏳ Login link expired. You might need to start the login flow again."
				if _, err = b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %w", err)
				}

				return nil
			}

			if errors.Is(err, context.Canceled) {
				msg := "🛑 Bot is shutting down. Login flow is not completed."
				if _, err = b.SendMessage(chatID, msg, sendOpt); nil != err {
					return fmt.Errorf("failed to send message: %w", err)
				}

				return nil
			}

			msg := "❌ Login wait failed due to unexpected error. See logs for details."
			if _, err = b.SendMessage(chatID, msg, sendOpt); nil != err {
				return fmt.Errorf("failed to send message: %w", err)
			}

			logger.Error().Err(err).Msg("failed to login wait")

			return nil
		}

		msg = "✅ Login successful. You can now use the bot to download Tidal links."
		if _, err = b.SendMessage(chatID, msg, sendOpt); nil != err {
			return fmt.Errorf("failed to send message: %w", err)
		}

		return nil
	}
}

func NewTidalAuthStatusCommandHandler(ctx context.Context, logger zerolog.Logger, td *tidal.Client) handlers.Response {
	return func(b *gotgbot.Bot, u *ext.Context) error {
		panic("not implemented")
	}
}
