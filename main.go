package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/urfave/cli/v3"

	"github.com/xeptore/tidalgram/bot"
	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/constant"
	"github.com/xeptore/tidalgram/log"
	"github.com/xeptore/tidalgram/telegram"
	"github.com/xeptore/tidalgram/tidal"
)

func main() {
	logger := log.NewDefault()

	//nolint:exhaustruct
	app := &cli.Command{
		Name:    "tidalgram",
		Version: constant.Version,
		Metadata: map[string]any{
			"compiled_at": constant.CompileTime,
		},
		Suggest:                    true,
		Usage:                      "Telegram Tidal Downloader",
		EnableShellCompletion:      true,
		ShellCompletionCommandName: "shell-completion",
		AllowExtFlags:              false,
		Flags: []cli.Flag{
			//nolint:exhaustruct
			&cli.StringFlag{
				Name:     "config",
				Usage:    "Config file path",
				Required: false,
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "telegram",
				Usage: "Telegram commands",
				Commands: []*cli.Command{
					//nolint:exhaustruct
					{
						Name:   "login",
						Usage:  "Login to Telegram",
						Action: telegramLogin,
					},
					{
						Name:   "logout",
						Usage:  "Logout from Telegram",
						Action: telegramLogout,
					},
				},
			},
			{
				Name:  "bot",
				Usage: "Bot commands",
				Commands: []*cli.Command{
					//nolint:exhaustruct
					{
						Name:   "run",
						Usage:  "Run the bot",
						Action: botRun,
					},
					{
						Name:  "logout",
						Usage: "Logout the bot",
						Description: strings.Join(
							[]string{
								"Execute before you want to move the bot from the cloud Bot API server.",
								"Otherwise there is no guarantee that the bot will receive updates.",
								"After a successful call, you can immediately log in on a local server,",
								"but will not be able to log in back to the cloud Bot API server for 10 minutes.",
							},
							"\n",
						),
						Action: botLogout,
					},
					{
						Name:  "close",
						Usage: "Closes the bot",
						Description: strings.Join(
							[]string{
								"Execute before you want to move the bot from one local server to another.",
								"Errors if execute in the first 10 minutes of the bot being launched.",
							},
							"\n",
						),
						Action: botClose,
					},
				},
			},
		},
	}

	if err := app.Run(context.Background(), os.Args); nil != err {
		if errors.Is(err, context.Canceled) {
			logger.Trace().Msg("Application was canceled")
			os.Exit(1)
		}

		var exitCode exitCodeError
		if errors.As(err, &exitCode) {
			os.Exit(int(exitCode))
		}

		logger.Error().Err(err).Msg("Application exited with error")
		os.Exit(10)
	}
}

type exitCodeError int

func (e exitCodeError) Error() string {
	return "error with exit code: " + strconv.Itoa(int(e))
}

func telegramLogin(ctx context.Context, cmd *cli.Command) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := log.NewDefault()

	if err := godotenv.Load(); nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("load .env file: %v", err)
		}
		logger.Info().Msg(".env file was not found")
	} else {
		logger.Debug().Msg(".env file was loaded")
	}

	conf, err := config.Load(cmd.String("config"))
	if nil != err {
		return fmt.Errorf("load config: %v", err)
	}

	logger = log.FromConfig(conf.Log)

	logger.Debug().Dict("config", conf.ToDict()).Msg("Config loaded")

	if err := telegram.Login(ctx, logger, conf.Telegram); nil != err {
		if errors.Is(err, syscall.ENOTTY) {
			logger.Error().Msg("No TTY detected. Please run the container with `--tty` or set `tty: true` in Docker Compose.")
			return exitCodeError(1)
		}

		return fmt.Errorf("login to telegram: %w", err)
	}

	return nil
}

func telegramLogout(ctx context.Context, cmd *cli.Command) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := log.NewDefault()

	if err := godotenv.Load(); nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("load .env file: %v", err)
		}
		logger.Info().Msg(".env file was not found")
	} else {
		logger.Debug().Msg(".env file was loaded")
	}

	conf, err := config.Load(cmd.String("config"))
	if nil != err {
		return fmt.Errorf("load config: %v", err)
	}

	logger = log.FromConfig(conf.Log)

	logger.Debug().Dict("config", conf.ToDict()).Msg("Config loaded")

	if err := telegram.Logout(ctx, logger, conf.Telegram); nil != err {
		if errors.Is(err, syscall.ENOTTY) {
			logger.Error().Msg("No TTY detected. Please run the container with `--tty` or set `tty: true` in Docker Compose.")
			return exitCodeError(1)
		}

		return fmt.Errorf("logout from telegram: %w", err)
	}

	logger.Info().Msg("Telegram client logged out successfully")

	return nil
}

func botRun(ctx context.Context, cmd *cli.Command) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := log.NewDefault()

	if err := godotenv.Load(); nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("load .env file: %v", err)
		}
		logger.Info().Msg(".env file was not found")
	} else {
		logger.Debug().Msg(".env file was loaded")
	}

	conf, err := config.Load(cmd.String("config"))
	if nil != err {
		return fmt.Errorf("load config: %v", err)
	}

	logger = log.FromConfig(conf.Log)

	logger.Debug().Dict("config", conf.ToDict()).Msg("Config loaded")

	td, err := tidal.NewClient(logger, conf.Bot.CredsDir, conf.Bot.DownloadsDir, conf.Tidal)
	if nil != err {
		return fmt.Errorf("create tidal client: %v", err)
	}
	logger.Debug().Msg("Tidal client created")

	b, err := bot.New(ctx, logger, conf.Bot)
	if nil != err {
		return fmt.Errorf("create tidalgram bot: %w", err)
	}
	logger.Info().Dict("account", b.Account.ToDict()).Msg("Bot instance created")

	up, err := telegram.NewUploader(ctx, logger, conf.Telegram)
	if nil != err {
		if errors.Is(err, telegram.ErrUnauthorized) {
			logger.Error().Msg("Telegram client is not authorized. Please login to Telegram.")
			return exitCodeError(2)
		}

		if errors.Is(err, telegram.ErrPeerNotFound) {
			switch kind := conf.Telegram.Upload.Peer.Kind; kind {
			case "channel":
				logger.
					Error().
					Int64("channel_id", conf.Telegram.Upload.Peer.ID).
					Msg("Telegram channel not found. Please make sure you are an admin of the channel.")

				return exitCodeError(3)
			case "chat":
				logger.
					Error().
					Int64("chat_id", conf.Telegram.Upload.Peer.ID).
					Msg("Telegram chat (legacy group) not found. Please make sure you are a member of the chat.")

				return exitCodeError(3)
			case "user":
				logger.
					Error().
					Int64("user_id", conf.Telegram.Upload.Peer.ID).
					Msg("Telegram user not found. Please make sure you have already have a private chat with the user.")

				return exitCodeError(3)
			default:
				panic("invalid peer kind: %s" + kind)
			}
		}

		return fmt.Errorf("create telegram uploader: %w", err)
	}
	defer func() {
		if err := up.Close(); nil != err {
			logger.Error().Err(err).Msg("close telegram uploader")
		}
	}()
	logger.Debug().Msg("Telegram uploader created")

	worker := bot.NewWorker(1)

	b.RegisterHandlers(ctx, logger, conf.Bot, td, up, worker)

	logger.Debug().Msg("Starting Tidalgram bot")
	if err := b.Start(ctx); nil != err {
		return fmt.Errorf("start tidalgram bot: %w", err)
	}
	logger.Info().Msg("Tidalgram bot started and listening for updates")

	<-ctx.Done()
	logger.Warn().Msg("Stopping Tidalgram application")

	if err := b.Stop(); nil != err {
		return fmt.Errorf("stop tidalgram bot: %v", err)
	}
	logger.Info().Msg("Tidalgram bot stopped successfully")

	return nil
}

func botLogout(ctx context.Context, cmd *cli.Command) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := log.NewDefault()

	if err := godotenv.Load(); nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("load .env file: %v", err)
		}
		logger.Info().Msg(".env file was not found")
	} else {
		logger.Debug().Msg(".env file was loaded")
	}

	conf, err := config.Load(cmd.String("config"))
	if nil != err {
		return fmt.Errorf("load config: %v", err)
	}

	logger = log.FromConfig(conf.Log)

	logger.Debug().Dict("config", conf.ToDict()).Msg("Config loaded")

	b, err := bot.NewAPI(ctx, logger, conf.Bot)
	if nil != err {
		return fmt.Errorf("create tidalgram API bot: %w", err)
	}
	logger.Info().Dict("account", b.Account.ToDict()).Msg("Bot instance created")

	if err := b.Logout(ctx); nil != err {
		return fmt.Errorf("logout tidalgram API bot: %w", err)
	}
	logger.Info().Msg("Bot instance logged out successfully. You can now run the bot locally.")

	return nil
}

func botClose(ctx context.Context, cmd *cli.Command) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := log.NewDefault()

	if err := godotenv.Load(); nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("load .env file: %v", err)
		}
		logger.Info().Msg(".env file was not found")
	} else {
		logger.Info().Msg(".env file was loaded")
	}

	conf, err := config.Load(cmd.String("config"))
	if nil != err {
		return fmt.Errorf("load config: %v", err)
	}

	logger = log.FromConfig(conf.Log)

	logger.Debug().Dict("config", conf.ToDict()).Msg("Config loaded")

	b, err := bot.NewAPI(ctx, logger, conf.Bot)
	if nil != err {
		return fmt.Errorf("create tidalgram API bot: %w", err)
	}
	logger.Info().Dict("account", b.Account.ToDict()).Msg("Bot instance created")

	if err := b.Close(ctx); nil != err {
		return fmt.Errorf("close tidalgram API bot: %w", err)
	}
	logger.
		Info().
		Msg("Bot instance closed successfully. You can now move the bot to another local server.")

	return nil
}
