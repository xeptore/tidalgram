package bot

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/message"
	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/constant"
	"github.com/xeptore/tidalgram/telegram"
	"github.com/xeptore/tidalgram/tidal"
)

var ErrChatNotFound = errors.New("chat not found")

type Bot struct {
	bot        *gotgbot.Bot
	updater    *ext.Updater
	dispatcher *ext.Dispatcher
	logger     zerolog.Logger
	papaChatID int64
	Account    Account
}

type Account struct {
	ID        int64
	Username  string
	IsBot     bool
	IsPremium bool
	FirstName string
	LastName  string
}

func (a *Account) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Int64("id", a.ID).
		Str("username", a.Username).
		Bool("is_bot", a.IsBot).
		Bool("is_premium", a.IsPremium).
		Str("first_name", a.FirstName).
		Str("last_name", a.LastName)
}

func New(ctx context.Context, logger zerolog.Logger, conf config.Bot) (*Bot, error) {
	proxy := func(*http.Request) (*url.URL, error) { return nil, nil }
	if len(conf.Proxy.Host) > 0 && conf.Proxy.Port > 0 {
		proxy = func(_ *http.Request) (*url.URL, error) {
			return url.Parse("socks5://" + net.JoinHostPort(conf.Proxy.Host, strconv.Itoa(conf.Proxy.Port)))
		}
	}

	b, err := gotgbot.NewBot(conf.Token, &gotgbot.BotOpts{ //nolint:exhaustruct
		BotClient: &gotgbot.BaseBotClient{
			Client: http.Client{ //nolint:exhaustruct
				Transport: &http.Transport{ //nolint:exhaustruct
					Proxy: proxy,
				},
			},
			UseTestEnvironment: false,
			DefaultRequestOpts: &gotgbot.RequestOpts{
				Timeout: 10 * time.Minute,
				APIURL:  conf.APIURL,
			},
		},
	})
	if nil != err {
		return nil, fmt.Errorf("failed to create bot: %v", err)
	}

	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{ //nolint:exhaustruct
		Error: func(_ *gotgbot.Bot, _ *ext.Context, err error) ext.DispatcherAction {
			if ctxErr := ctx.Err(); nil != ctxErr && errors.Is(ctxErr, context.Canceled) && errors.Is(err, context.Canceled) {
				logger.Warn().Msg("Context cancelled while handling update")
				return ext.DispatcherActionEndGroups
			}

			logger.Error().Err(err).Msg("An error occurred while handling update")

			return ext.DispatcherActionNoop
		},
		Panic: func(_ *gotgbot.Bot, _ *ext.Context, r any) {
			logger.Error().Any("panic", r).Msg("Panic occurred while handling update")
		},
		MaxRoutines: 10,
	})
	updater := ext.NewUpdater(dispatcher, nil)

	return &Bot{
		bot:        b,
		updater:    updater,
		dispatcher: dispatcher,
		logger:     logger,
		papaChatID: conf.PapaID,
		Account:    fillAccount(b),
	}, nil
}

func fillAccount(b *gotgbot.Bot) Account {
	return Account{
		ID:        b.Id,
		Username:  b.Username,
		IsBot:     b.IsBot,
		IsPremium: b.IsPremium,
		FirstName: b.FirstName,
		LastName:  b.LastName,
	}
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
		return fmt.Errorf("failed to start polling: %v", err)
	}

	sendOpts := &gotgbot.SendMessageOpts{ //nolint:exhaustruct
		ParseMode: gotgbot.ParseModeMarkdownV2,
	}
	compiledAt, _ := time.Parse(time.RFC3339, constant.CompileTime)
	msg := strings.Join([]string{
		`I'm online, Papa 🙂`,
		``,
		``,
		"> 🏷️ Version: `" + constant.Version + "`",
		"> 🕒 Compiled At: `" + compiledAt.Format("2006/01/02 15:04:05") + " UTC`",
	}, "\n")
	if _, err := b.bot.SendMessage(b.papaChatID, msg, sendOpts); nil != err {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

func (b *Bot) Stop() error {
	if err := b.updater.Stop(); nil != err {
		return fmt.Errorf("failed to bot stop updater: %v", err)
	}

	sendOpts := &gotgbot.SendMessageOpts{ //nolint:exhaustruct
		ParseMode: gotgbot.ParseModeMarkdown,
	}
	if _, err := b.bot.SendMessage(b.papaChatID, "I'm going offline, Papa 🥲", sendOpts); nil != err {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

type APIBot struct {
	bot     *gotgbot.Bot
	Account Account
}

func NewAPI(ctx context.Context, logger zerolog.Logger, conf config.Bot) (*APIBot, error) {
	b, err := gotgbot.NewBot(conf.Token, &gotgbot.BotOpts{ //nolint:exhaustruct
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
		return nil, fmt.Errorf("failed to create bot: %v", err)
	}

	return &APIBot{
		bot:     b,
		Account: fillAccount(b),
	}, nil
}

// Logout logs out from the cloud Bot API server before launching the bot locally.
// You must log out the bot before running it locally,
// otherwise there is no guarantee that the bot will receive updates.
// After a successful call, you can immediately log in on a local server,
// but will not be able to log in back to the cloud Bot API server for 10 minutes.
func (b *APIBot) Logout(ctx context.Context) error {
	if _, err := b.bot.LogOutWithContext(ctx, nil); nil != err {
		return fmt.Errorf("failed to log out: %w", err)
	}

	return nil
}

// Close closes the bot instance before moving it from one local server to another.
// The method will return error 429 in the first 10 minutes after the bot is launched.
func (b *APIBot) Close(ctx context.Context) error {
	if _, err := b.bot.DeleteWebhookWithContext(ctx, nil); nil != err {
		return fmt.Errorf("failed to delete webhook: %w", err)
	}

	if _, err := b.bot.CloseWithContext(ctx, nil); nil != err {
		return fmt.Errorf("failed to close bot: %w", err)
	}

	return nil
}

func (b *Bot) RegisterHandlers(
	ctx context.Context,
	logger zerolog.Logger,
	conf config.Bot,
	td *tidal.Client,
	up *telegram.Uploader,
	worker *Worker,
) {
	b.dispatcher.AddHandler(
		handlers.
			NewMessage(
				tidalURLFilter,
				NewChainHandler(
					NewPapaOnlyGuard(conf.PapaID),
					NewTidalURLHandler(ctx, logger, td, conf, up, worker),
				),
			).
			SetAllowChannel(false).
			SetAllowEdited(false),
	)

	b.dispatcher.AddHandler(
		handlers.
			NewCommand(
				"start",
				NewChainHandler(
					NewStartCommandHandler(ctx, conf.PapaID),
				),
			).
			SetAllowChannel(false).
			SetAllowEdited(false),
	)

	b.dispatcher.AddHandler(
		handlers.
			NewCommand(
				"cancel",
				NewChainHandler(
					NewPapaOnlyGuard(conf.PapaID),
					NewCancelCommandHandler(ctx, worker),
				),
			).
			SetAllowChannel(false).
			SetAllowEdited(false),
	)

	b.dispatcher.AddHandler(
		handlers.
			NewCommand(
				"tidal_login",
				NewChainHandler(
					NewPapaOnlyGuard(conf.PapaID),
					NewTidalLoginCommandHandler(ctx, logger, td),
				),
			).
			SetAllowChannel(false).
			SetAllowEdited(false),
	)

	b.dispatcher.AddHandler(
		handlers.
			NewCommand(
				"tidal_auth_status",
				NewChainHandler(
					NewPapaOnlyGuard(conf.PapaID),
					NewTidalAuthStatusCommandHandler(ctx, logger, td),
				),
			).
			SetAllowChannel(false).
			SetAllowEdited(false),
	)
}

func tidalURLFilter(msg *gotgbot.Message) bool {
	return message.Text(msg) && !message.Command(msg) && message.Entity("url")(msg) && isTidalURL(msg.Text)
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

func getMessageURL(msg *gotgbot.Message) string {
	for _, ent := range msg.Entities {
		if ent.Type != "url" {
			continue
		}

		return gotgbot.ParseEntity(msg.Text, ent).Text
	}
	panic("expected message to contain URL at this point")
}
