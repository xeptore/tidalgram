package telegram

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/rs/zerolog"
	"github.com/xeptore/tidalgram/config"
	"golang.org/x/net/proxy"
	"golang.org/x/time/rate"
)

func defaultNoUpdatesClientOpts(ctx context.Context, logger zerolog.Logger, storage *Storage, conf config.Telegram) telegram.Options {
	const maxReconnects = 1_000

	var resolver dcs.Resolver

	if len(conf.Proxy.Host) > 0 && conf.Proxy.Port > 0 {
		var proxyAuth *proxy.Auth
		if len(conf.Proxy.Username) > 0 && len(conf.Proxy.Password) > 0 {
			proxyAuth = &proxy.Auth{
				User:     conf.Proxy.Username,
				Password: conf.Proxy.Password,
			}
		}
		sock5, _ := proxy.SOCKS5(
			"tcp",
			net.JoinHostPort(conf.Proxy.Host, strconv.Itoa(conf.Proxy.Port)),
			proxyAuth,
			proxy.Direct,
		)
		dc := sock5.(proxy.ContextDialer)
		resolver = dcs.Plain(dcs.PlainOptions{
			Dial: dc.DialContext,
		})
	}

	return telegram.Options{ //nolint:exhaustruct
		Device: telegram.DeviceConfig{ //nolint:exhaustruct
			DeviceModel:    "Desktop",
			SystemVersion:  "Windows 11 x64",
			AppVersion:     "6.0.2 x64",
			LangCode:       "en",
			SystemLangCode: "en-US",
			LangPack:       "tdesktop",
		},
		NoUpdates:     true,
		UpdateHandler: nil,
		Resolver:      resolver,
		ReconnectionBackoff: func() backoff.BackOff {
			return backoff.WithContext(
				backoff.WithMaxRetries(
					backoff.NewExponentialBackOff(
						backoff.WithInitialInterval(time.Second*1),
						backoff.WithMaxInterval(time.Minute*7),
						backoff.WithMaxElapsedTime(time.Minute*30),
					),
					maxReconnects,
				),
				ctx,
			)
		},
		OnDead: func() {
			logger.Error().Msg("Connection to Telegram server was lost")
		},
		Logger:         nil,
		SessionStorage: storage,
		Middlewares: []telegram.Middleware{
			// floodwait.
			// 	NewWaiter().
			// 	WithCallback(func(ctx context.Context, wait floodwait.FloodWait) {
			// 		logger.
			// 			Error().
			// 			Int("seconds", int(wait.Duration.Seconds())).
			// 			Msg("Got FLOOD_WAIT. Will retry after")
			// 	}),
			ratelimit.New(rate.Every(time.Millisecond*100), 5),
		},
	}
}
