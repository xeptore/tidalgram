package telegram

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/rs/zerolog"
	"golang.org/x/net/proxy"

	"github.com/xeptore/tidalgram/config"
)

func newClientOptions(
	ctx context.Context,
	logger zerolog.Logger,
	storage *Storage,
	conf config.Telegram,
) (*telegram.Options, error) {
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
		dc, ok := sock5.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("failed to cast proxy to ContextDialer")
		}
		resolver = dcs.Plain(dcs.PlainOptions{ //nolint:exhaustruct
			Dial: dc.DialContext,
		})
	}

	return &telegram.Options{ //nolint:exhaustruct
		Device: telegram.DeviceConfig{ //nolint:exhaustruct
			DeviceModel:    "Tidalgram",
			SystemVersion:  "Windows 11 x64",
			AppVersion:     "6.1.3 x64",
			LangCode:       "en",
			SystemLangCode: "en-US",
			LangPack:       "tdesktop",
		},
		NoUpdates:     false,
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
			logger.Warn().Msg("Connection to Telegram server was lost")
		},
		Logger:         nil,
		SessionStorage: storage,
	}, nil
}
