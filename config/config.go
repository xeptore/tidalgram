package config

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Bot     Bot     `yaml:"bot"`
	Logging Logging `yaml:"logging"`
}

func (c *Config) setDefaults() {
	c.Bot.setDefaults()
	c.Logging.setDefaults()
}

func (c *Config) validate() error {
	if err := c.Bot.validate(); nil != err {
		return fmt.Errorf("bot config validation failed: %v", err)
	}

	if err := c.Logging.validate(); nil != err {
		return fmt.Errorf("logging config validation failed: %v", err)
	}

	return nil
}

type Bot struct {
	AdminID  int64  `yaml:"admin_id"`
	APIURL   string `yaml:"api_url"`
	Token    string `yaml:"-"`
	CredsDir string `yaml:"creds_dir"`
}

func (c *Bot) setDefaults() {
	if c.APIURL == "" {
		c.APIURL = "https://api.telegram.org"
	}
	if c.CredsDir == "" {
		c.CredsDir = "./creds"
	}
}

func (c *Bot) validate() error {
	if c.AdminID == 0 {
		return errors.New("admin_id is required")
	}
	if c.APIURL == "" {
		return errors.New("api_url is required")
	}
	if c.Token == "" {
		return errors.New("make sure the BOT_TOKEN environment variable is set")
	}
	if c.CredsDir == "" {
		return errors.New("creds_dir is required")
	}

	return nil
}

type Logging struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

func (c *Logging) setDefaults() {
	if c.Level == "" {
		c.Level = "info"
	}
	if c.Format == "" {
		c.Format = "pretty"
	}
}

func (c *Logging) validate() error {
	if c.Level == "" {
		return errors.New("level is required")
	}
	if !slices.Contains([]string{"debug", "info", "warn", "error", "fatal", "panic"}, c.Level) {
		return fmt.Errorf(
			"level must be one of: debug, info, warn, error, fatal, panic, got: %s",
			c.Level,
		)
	}
	if c.Format == "" {
		return errors.New("format is required")
	}
	if !slices.Contains([]string{"json", "pretty"}, c.Format) {
		return fmt.Errorf("format must be 'json' or 'pretty', got: %s", c.Format)
	}

	return nil
}

func Load(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if nil != err {
		return nil, fmt.Errorf("failed to read config file %s: %v", filename, err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); nil != err {
		return nil, fmt.Errorf("failed to parse config file %s: %v", filename, err)
	}

	config.Bot.Token = os.Getenv("BOT_TOKEN")
	config.setDefaults()

	if err := config.validate(); nil != err {
		return nil, fmt.Errorf("configuration validation failed: %v", err)
	}

	return &config, nil
}
