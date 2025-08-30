package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/AlecAivazis/survey/v2"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/mattn/go-isatty"
	"github.com/rs/zerolog"
	"github.com/skip2/go-qrcode"

	"github.com/xeptore/tidalgram/config"
)

func Login(ctx context.Context, logger zerolog.Logger, conf config.Telegram) (err error) {
	var (
		stdin  = os.Stdin
		stdout = os.Stdout
	)

	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return syscall.ENOTTY
	}

	storage, err := NewStorage(conf.Storage.Path)
	if nil != err {
		return fmt.Errorf("create storage: %v", err)
	}
	defer func() {
		if closeErr := storage.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("close storage: %v", closeErr))
		}
	}()

	opts, err := newClientOptions(ctx, logger, storage, conf)
	if nil != err {
		return fmt.Errorf("get client options: %w", err)
	}

	opts.Middlewares = []telegram.Middleware{
		newSimpleWaiterMiddleware(),
	}
	dispatcher := tg.NewUpdateDispatcher()
	opts.UpdateHandler = dispatcher
	client := telegram.NewClient(conf.AppID, conf.AppHash, *opts)

	err = client.Run(ctx, func(ctx context.Context) error {
		var lines int
		_, err = client.QR().Auth(
			ctx,
			qrlogin.OnLoginToken(dispatcher),
			func(ctx context.Context, token qrlogin.Token) error {
				qr, err := qrcode.New(token.URL(), qrcode.Highest)
				if nil != err {
					return fmt.Errorf("create qr code: %v", err)
				}

				const noInverseColor = false
				code := qr.ToSmallString(noInverseColor)
				lines = strings.Count(code, "\n")

				fmt.Fprint(stdout, code)
				fmt.Fprint(stdout, strings.Repeat(text.CursorUp.Sprint(), lines))

				return nil
			},
		)

		// Clear the QR code from the console
		var out strings.Builder
		for range lines {
			out.WriteString(text.EraseLine.Sprint())
			out.WriteString(text.CursorDown.Sprint())
		}
		out.WriteString(text.CursorUp.Sprintn(lines))
		fmt.Fprint(stdout, out.String())

		if nil != err {
			// https://core.telegram.org/api/auth#2fa
			if !tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
				return fmt.Errorf("unknown error from QR code login: %w", err)
			}

			var pwd string
			prompt := &survey.Password{ //nolint:exhaustruct
				Message: "Enter 2FA Password:",
			}
			askOpts := []survey.AskOpt{
				survey.WithValidator(survey.Required),
				survey.WithHideCharacter('*'),
				survey.WithStdio(stdin, stdout, stdout),
				survey.WithShowCursor(true),
			}
			if err = survey.AskOne(prompt, &pwd, askOpts...); nil != err {
				return fmt.Errorf("ask for 2fa password: %v", err)
			}

			if _, err = client.Auth().Password(ctx, pwd); nil != err {
				return fmt.Errorf("finalize login with 2fa password: %v", err)
			}
		}

		user, err := client.Self(ctx)
		if nil != err {
			return fmt.Errorf("get logged in user: %w", err)
		}

		fmt.Fprint(stdout, text.EraseLine.Sprint())

		logger.
			Info().
			Int64("id", user.ID).
			Str("username", user.Username).
			Str("first_name", user.FirstName).
			Str("last_name", user.LastName).
			Bool("premium", user.Premium).
			Bool("verified", user.Verified).
			Msg("Login successfully!")

		return nil
	})
	if nil != err {
		return fmt.Errorf("login: %w", err)
	}

	return nil
}

func Logout(ctx context.Context, logger zerolog.Logger, conf config.Telegram) (err error) {
	storage, err := NewStorage(conf.Storage.Path)
	if nil != err {
		return fmt.Errorf("create storage: %v", err)
	}
	defer func() {
		if closeErr := storage.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("close storage: %v", closeErr))
		}
	}()

	opts, err := newClientOptions(ctx, logger, storage, conf)
	if nil != err {
		return fmt.Errorf("get client options: %w", err)
	}

	opts.Middlewares = []telegram.Middleware{
		newSimpleWaiterMiddleware(),
	}

	client := telegram.NewClient(conf.AppID, conf.AppHash, *opts)

	err = client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if nil != err {
			return fmt.Errorf("get auth status: %w", err)
		}
		if !status.Authorized {
			return nil
		}

		if _, err := client.API().AuthLogOut(ctx); nil != err {
			return fmt.Errorf("logout: %w", err)
		}

		if err := storage.DeleteSession(ctx); nil != err {
			return fmt.Errorf("delete session: %w", err)
		}

		return nil
	})
	if nil != err {
		return fmt.Errorf("logout: %w", err)
	}

	return nil
}
