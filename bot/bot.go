package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/message"
	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/tidal"
)

type Bot struct {
	bot     *gotgbot.Bot
	updater *ext.Updater
	logger  *zerolog.Logger
	Account Account
}

type Account struct {
	ID        int64
	IsBot     bool
	IsPremium bool
	FirstName string
	LastName  string
}

func (a *Account) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Int64("id", a.ID).
		Bool("is_bot", a.IsBot).
		Bool("is_premium", a.IsPremium).
		Str("first_name", a.FirstName).
		Str("last_name", a.LastName)
}

func New(
	ctx context.Context,
	logger *zerolog.Logger,
	config *config.Bot,
	token string,
	tidal *tidal.Client,
) (*Bot, error) {
	b, err := gotgbot.NewBot(token, &gotgbot.BotOpts{ //nolint:exhaustruct
		BotClient: &gotgbot.BaseBotClient{
			Client: http.Client{ //nolint:exhaustruct
				Transport: &http.Transport{ //nolint:exhaustruct
					Proxy: func(_ *http.Request) (*url.URL, error) {
						return nil, nil
					},
				},
			},
			UseTestEnvironment: false,
			DefaultRequestOpts: &gotgbot.RequestOpts{
				Timeout: 10 * time.Minute,
				APIURL:  config.APIURL,
			},
		},
	})
	if nil != err {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{ //nolint:exhaustruct
		Error: func(_ *gotgbot.Bot, _ *ext.Context, err error) ext.DispatcherAction {
			logger.Error().Err(err).Msg("An error occurred while handling update")

			return ext.DispatcherActionNoop
		},
		Panic: func(_ *gotgbot.Bot, _ *ext.Context, r any) {
			logger.Error().Any("panic", r).Msg("Panic occurred while handling update")
		},
		MaxRoutines: 10,
	})
	registerHandlers(ctx, dispatcher, config, tidal)
	updater := ext.NewUpdater(dispatcher, nil)

	return &Bot{
		bot:     b,
		updater: updater,
		logger:  logger,
		Account: fillAccount(b),
	}, nil
}

func (b *Bot) Start() error {
	pollOpts := ext.PollingOpts{
		DropPendingUpdates: true,
		GetUpdatesOpts: &gotgbot.GetUpdatesOpts{ //nolint:exhaustruct
			Timeout: 9,
			RequestOpts: &gotgbot.RequestOpts{ //nolint:exhaustruct
				Timeout: time.Second * 10,
			},
			AllowedUpdates: []string{"message"},
		},
		EnableWebhookDeletion: true,
	}
	if err := b.updater.StartPolling(b.bot, &pollOpts); nil != err {
		return fmt.Errorf("failed to start polling: %w", err)
	}

	return nil
}

type APIBot struct {
	bot     *gotgbot.Bot
	Account Account
}

func NewAPI(
	ctx context.Context,
	logger *zerolog.Logger,
	config *config.Bot,
	token string,
) (*APIBot, error) {
	b, err := gotgbot.NewBot(token, &gotgbot.BotOpts{ //nolint:exhaustruct
		BotClient: &gotgbot.BaseBotClient{ //nolint:exhaustruct
			Client: http.Client{ //nolint:exhaustruct
				Transport: &http.Transport{ //nolint:exhaustruct
					Proxy: func(_ *http.Request) (*url.URL, error) {
						return nil, nil
					},
				},
			},
		},
	})
	if nil != err {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	return &APIBot{
		bot:     b,
		Account: fillAccount(b),
	}, nil
}

func fillAccount(bot *gotgbot.Bot) Account {
	return Account{
		ID:        bot.Id,
		IsBot:     bot.IsBot,
		IsPremium: bot.IsPremium,
		FirstName: bot.FirstName,
		LastName:  bot.LastName,
	}
}

// Logout logs out from the cloud Bot API server before launching the bot locally.
// You must log out the bot before running it locally,
// otherwise there is no guarantee that the bot will receive updates.
// After a successful call, you can immediately log in on a local server,
// but will not be able to log in back to the cloud Bot API server for 10 minutes.
func (b *APIBot) Logout(ctx context.Context) error {
	ok, err := b.bot.LogOutWithContext(ctx, nil)
	if nil != err {
		return fmt.Errorf("failed to log out: %w", err)
	}
	if !ok {
		return errors.New("failed to log out")
	}

	return nil
}

// Close closes the bot instance before moving it from one local server to another.
// The method will return error 429 in the first 10 minutes after the bot is launched.
func (b *APIBot) Close(ctx context.Context) error {
	ok, err := b.bot.DeleteWebhookWithContext(ctx, nil)
	if nil != err {
		return fmt.Errorf("failed to delete webhook: %w", err)
	}
	if !ok {
		return errors.New("failed to delete webhook")
	}

	ok, err = b.bot.CloseWithContext(ctx, nil)
	if nil != err {
		return fmt.Errorf("failed to close bot: %w", err)
	}
	if !ok {
		return errors.New("failed to close bot")
	}

	return nil
}

func (b *Bot) Stop(ctx context.Context) error {
	if err := b.updater.Stop(); nil != err {
		return fmt.Errorf("failed to stop updater: %w", err)
	}

	return nil
}

func registerHandlers(
	ctx context.Context,
	d *ext.Dispatcher,
	config *config.Bot,
	tidal *tidal.Client,
) {
	d.AddHandler(
		handlers.
			NewMessage(
				tidalURLFilter,
				NewChainHandler(
					NewAdminOnlyGuard(config.AdminID),
					NewTidalURLHandler(ctx, tidal),
				),
			).
			SetAllowChannel(true).
			SetAllowEdited(true),
	)

	d.AddHandler(
		handlers.
			NewCommand(
				"start",
				NewChainHandler(
					NewStartCommandHandler(ctx, config.AdminID),
				),
			).
			SetAllowChannel(false).
			SetAllowEdited(false),
	)

	d.AddHandler(
		handlers.
			NewCommand(
				"status",
				NewChainHandler(
					NewAdminOnlyGuard(config.AdminID),
					NewStatusCommandHandler(ctx, tidal),
				),
			).
			SetAllowChannel(false).
			SetAllowEdited(false),
	)

	d.AddHandler(
		handlers.
			NewCommand(
				"cancel",
				NewChainHandler(
					NewAdminOnlyGuard(config.AdminID),
					NewCancelCommandHandler(ctx, tidal),
				),
			).
			SetAllowChannel(true).
			SetAllowEdited(false),
	)

	d.AddHandler(
		handlers.
			NewCommand(
				"authorize",
				NewChainHandler(
					NewAdminOnlyGuard(config.AdminID),
					NewAuthorizeCommandHandler(ctx, tidal),
				),
			).
			SetAllowChannel(false).
			SetAllowEdited(false),
	)
}

func tidalURLFilter(msg *gotgbot.Message) bool {
	return message.Text(msg) &&
		!message.Command(msg) &&
		message.Entity("url")(msg) &&
		isTidalURL(msg.Text)
}

func isTidalURL(msg string) bool {
	u, err := url.Parse(msg)
	if nil != err {
		return false
	}

	switch u.Scheme {
	case "https":
	default:
		return false
	}

	switch u.Host {
	case "tidal.com", "www.tidal.com", "listen.tidal.com":
	default:
		return false
	}

	switch pathParts := strings.SplitN(strings.Trim(u.Path, "/"), "/", 3); len(pathParts) {
	case 2:
		switch pathParts[0] {
		case "mix", "playlist", "album", "artist", "track", "video":
		default:
			return false
		}
	case 3:
		switch pathParts[0] {
		case "browse":
		default:
			return false
		}

		switch pathParts[1] {
		case "mix", "playlist", "album", "artist", "track", "video":
		default:
			return false
		}
	}

	return true
}
