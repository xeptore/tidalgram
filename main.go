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
	"github.com/xeptore/tidalgram/constants"
	"github.com/xeptore/tidalgram/log"
	"github.com/xeptore/tidalgram/telegram"
	"github.com/xeptore/tidalgram/tidal"
)

func main() {
	logger := log.NewDefault()

	//nolint:exhaustruct
	app := &cli.Command{
		Name:    "tidalgram",
		Version: constants.Version,
		Metadata: map[string]any{
			"compiled_at": constants.CompileTime,
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
						Action: tdLogin,
					},
					{
						Name:   "logout",
						Usage:  "Logout from Telegram",
						Action: tdLogout,
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
						Description: strings.Join([]string{
							"Execute before you want to move the bot from the cloud Bot API server.",
							"Otherwise there is no guarantee that the bot will receive updates.",
							"After a successful call, you can immediately log in on a local server,",
							"but will not be able to log in back to the cloud Bot API server for 10 minutes.",
						}, "\n"),
						Action: botLogout,
					},
					{
						Name:  "close",
						Usage: "Closes the bot",
						Description: strings.Join([]string{
							"Execute before you want to move the bot from one local server to another.",
							"Errors if execute in the first 10 minutes of the bot being launched.",
						}, "\n"),
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

func tdLogin(ctx context.Context, cmd *cli.Command) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := log.NewDefault()

	if err := godotenv.Load(); nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to load .env file: %v", err)
		}
		logger.Info().Msg(".env file was not found")
	} else {
		logger.Debug().Msg(".env file was loaded")
	}

	conf, err := config.Load(cmd.String("config"))
	if nil != err {
		return fmt.Errorf("failed to load config: %v", err)
	}
	logger.Debug().Dict("config", conf.ToDict()).Msg("Config loaded")

	logger = log.FromConfig(conf.Log)

	if err := telegram.Login(ctx, logger, conf.TD); nil != err {
		if errors.Is(err, syscall.ENOTTY) {
			logger.Error().Msg("No TTY detected. Please run the container with `--tty` or set `tty: true` in Docker Compose.")
			return exitCodeError(1)
		}

		return fmt.Errorf("failed to login to telegram: %v", err)
	}

	logger.Info().Msg("Telegram client logged in successfully")

	return nil
}

func tdLogout(ctx context.Context, cmd *cli.Command) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := log.NewDefault()

	if err := godotenv.Load(); nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to load .env file: %v", err)
		}
		logger.Info().Msg(".env file was not found")
	} else {
		logger.Debug().Msg(".env file was loaded")
	}

	conf, err := config.Load(cmd.String("config"))
	if nil != err {
		return fmt.Errorf("failed to load config: %v", err)
	}
	logger.Debug().Dict("config", conf.ToDict()).Msg("Config loaded")

	logger = log.FromConfig(conf.Log)

	if err := telegram.Logout(ctx, logger, conf.TD); nil != err {
		if errors.Is(err, syscall.ENOTTY) {
			logger.Error().Msg("No TTY detected. Please run the container with `--tty` or set `tty: true` in Docker Compose.")
			return exitCodeError(1)
		}

		return fmt.Errorf("failed to login to telegram: %v", err)
	}

	logger.Info().Msg("Telegram client logged in successfully")

	return nil
}

func botRun(ctx context.Context, cmd *cli.Command) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := log.NewDefault()

	if err := godotenv.Load(); nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to load .env file: %v", err)
		}
		logger.Info().Msg(".env file was not found")
	} else {
		logger.Debug().Msg(".env file was loaded")
	}

	conf, err := config.Load(cmd.String("config"))
	if nil != err {
		return fmt.Errorf("failed to load config: %v", err)
	}
	logger.Debug().Dict("config", conf.ToDict()).Msg("Config loaded")

	logger = log.FromConfig(conf.Log)

	td, err := tidal.NewClient(logger, conf.Bot.CredsDir, conf.Bot.DownloadsDir, conf.Tidal)
	if nil != err {
		return fmt.Errorf("failed to create tidal client: %v", err)
	}
	logger.Debug().Msg("Tidal client created")

	up, err := telegram.NewUploader(ctx, logger, conf.TD)
	if nil != err {
		return fmt.Errorf("failed to create telegram uploader: %v", err)
	}
	logger.Debug().Msg("Telegram uploader created")

	b, err := bot.New(ctx, logger, conf.Bot, td, up)
	if nil != err {
		return fmt.Errorf("failed to create tidalgram bot: %v", err)
	}
	logger.Info().Dict("account", b.Account.ToDict()).Msg("Bot instance created")

	logger.Debug().Msg("Starting TidalGram bot")
	if err := b.Start(); nil != err {
		return fmt.Errorf("failed to start tidalgram bot: %w", err)
	}
	logger.Info().Msg("TidalGram bot started and listening for updates")

	<-ctx.Done()
	logger.Info().Msg("Stopping TidalGram application")

	if err := b.Stop(); nil != err {
		return fmt.Errorf("failed to stop tidalgram bot: %w", err)
	}
	logger.Info().Msg("TidalGram bot stopped successfully")

	return nil
}

func botLogout(ctx context.Context, cmd *cli.Command) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := log.NewDefault()

	if err := godotenv.Load(); nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to load .env file: %v", err)
		}
		logger.Info().Msg(".env file was not found")
	} else {
		logger.Debug().Msg(".env file was loaded")
	}

	conf, err := config.Load(cmd.String("config"))
	if nil != err {
		return fmt.Errorf("failed to load config: %v", err)
	}
	logger.Debug().Dict("config", conf.ToDict()).Msg("Config loaded")

	logger = log.FromConfig(conf.Log)

	b, err := bot.NewAPI(ctx, logger, conf.Bot)
	if nil != err {
		return fmt.Errorf("failed to create tidalgram API bot: %v", err)
	}
	logger.Info().Dict("account", b.Account.ToDict()).Msg("Bot instance created")

	if err := b.Logout(ctx); nil != err {
		return fmt.Errorf("failed to logout tidalgram API bot: %w", err)
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
			return fmt.Errorf("failed to load .env file: %v", err)
		}
		logger.Info().Msg(".env file was not found")
	} else {
		logger.Info().Msg(".env file was loaded")
	}

	conf, err := config.Load(cmd.String("config"))
	if nil != err {
		return fmt.Errorf("failed to load config: %v", err)
	}
	logger.Debug().Dict("config", conf.ToDict()).Msg("Config loaded")

	logger = log.FromConfig(conf.Log)

	b, err := bot.NewAPI(ctx, logger, conf.Bot)
	if nil != err {
		return fmt.Errorf("failed to create tidalgram API bot: %v", err)
	}
	logger.Info().Dict("account", b.Account.ToDict()).Msg("Bot instance created")

	if err := b.Close(ctx); nil != err {
		return fmt.Errorf("failed to close tidalgram API bot: %w", err)
	}
	logger.
		Info().
		Msg("Bot instance closed successfully. You can now move the bot to another local server.")

	return nil
}
