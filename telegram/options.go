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

	gate := NewConnectionGate()
	dial := new(net.Dialer).DialContext

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
		dial = dc.DialContext
	}
	resolver := dcs.Plain(dcs.PlainOptions{ //nolint:exhaustruct
		Dial: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			if err := gate.Wait(ctx); nil != err {
				return nil, err
			}

			return dial(ctx, network, addr)
		},
	})

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
		OnDead: func(err error) {
			if IsTransportFlood(err) {
				until, started := gate.Block()
				if started {
					logger.
						Warn().
						Err(err).
						Time("retry_at", until).
						Msg("Telegram transport flooded; pausing new connection attempts")
				} else {
					logger.Debug().Err(err).Msg("Telegram transport flood cooldown is already active")
				}

				return
			}
			if gate.Blocked() {
				logger.Debug().Err(err).Msg("Telegram connection was lost during transport flood cooldown")

				return
			}

			logger.Warn().Err(err).Msg("Connection to Telegram server was lost")
		},
		Logger:         nil,
		SessionStorage: storage,
	}, nil
}
