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
	Tidal   Tidal   `yaml:"tidal"`
}

func (c *Config) setDefaults() {
	c.Bot.setDefaults()
	c.Logging.setDefaults()
	c.Tidal.setDefaults()
}

func (c *Config) validate() error {
	if err := c.Bot.validate(); nil != err {
		return fmt.Errorf("bot config validation failed: %v", err)
	}

	if err := c.Logging.validate(); nil != err {
		return fmt.Errorf("logging config validation failed: %v", err)
	}

	if err := c.Tidal.validate(); nil != err {
		return fmt.Errorf("tidal config validation failed: %v", err)
	}

	return nil
}

type Bot struct {
	PapaID       int64  `yaml:"papa_id"`
	APIURL       string `yaml:"api_url"`
	Token        string `yaml:"-"`
	CredsDir     string `yaml:"creds_dir"`
	DownloadsDir string `yaml:"downloads_dir"`
}

func (c *Bot) setDefaults() {
	if c.APIURL == "" {
		c.APIURL = "https://api.telegram.org"
	}

	if c.CredsDir == "" {
		c.CredsDir = "./creds"
	}

	if c.DownloadsDir == "" {
		c.DownloadsDir = "./downloads"
	}
}

func (c *Bot) validate() error {
	if c.PapaID == 0 {
		return errors.New("papa_id is required")
	}

	if c.Token == "" {
		return errors.New("make sure the BOT_TOKEN environment variable is set")
	}

	if i, err := os.Stat(c.CredsDir); nil != err {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("creds_dir does not exist")
		}

		return fmt.Errorf("failed to stat creds_dir: %v", err)
	} else if !i.IsDir() {
		return errors.New("creds_dir must be a directory")
	}

	if i, err := os.Stat(c.DownloadsDir); nil != err {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("downloads_dir does not exist")
		}

		return fmt.Errorf("failed to stat downloads_dir: %v", err)
	} else if !i.IsDir() {
		return errors.New("downloads_dir must be a directory")
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
	if !slices.Contains([]string{"debug", "info", "warn", "error", "fatal", "panic"}, c.Level) {
		return fmt.Errorf(
			"level must be one of: debug, info, warn, error, fatal, panic, got: %s",
			c.Level,
		)
	}

	if !slices.Contains([]string{"json", "pretty"}, c.Format) {
		return fmt.Errorf("format must be 'json' or 'pretty', got: %s", c.Format)
	}

	return nil
}

type Tidal struct {
	Downloader TidalDownloader `yaml:"downloader"`
}

func (c *Tidal) setDefaults() {
	c.Downloader.setDefaults()
}

func (c *Tidal) validate() error {
	if err := c.Downloader.validate(); nil != err {
		return fmt.Errorf("downloader config validation failed: %v", err)
	}

	return nil
}

type TidalDownloader struct {
	Timeouts TidalDownloadTimeouts `yaml:"timeouts"`
}

func (c *TidalDownloader) setDefaults() {
	c.Timeouts.setDefaults()
}

func (c *TidalDownloader) validate() error {
	if err := c.Timeouts.validate(); nil != err {
		return fmt.Errorf("timeouts config validation failed: %v", err)
	}

	return nil
}

type TidalDownloadTimeouts struct {
	GetTrackCredits     int `yaml:"get_track_credits"`
	GetTrackLyrics      int `yaml:"get_track_lyrics"`
	DownloadCover       int `yaml:"download_cover"`
	GetAlbumInfo        int `yaml:"get_album_info"`
	GetStreamURLs       int `yaml:"get_stream_urls"`
	GetPlaylistInfo     int `yaml:"get_playlist_info"`
	GetMixInfo          int `yaml:"get_mix_info"`
	GetPagedTracks      int `yaml:"get_paged_tracks"`
	DownloadDashSegment int `yaml:"download_dash_segment"`
	GetVNDTrackFileSize int `yaml:"get_vnd_track_file_size"`
	DownloadVNDSegment  int `yaml:"download_vnd_segment"`
}

func (c *TidalDownloadTimeouts) setDefaults() {
	if c.GetTrackCredits == 0 {
		c.GetTrackCredits = 2
	}

	if c.GetTrackLyrics == 0 {
		c.GetTrackLyrics = 2
	}

	if c.DownloadCover == 0 {
		c.DownloadCover = 3
	}

	if c.GetAlbumInfo == 0 {
		c.GetAlbumInfo = 2
	}

	if c.GetStreamURLs == 0 {
		c.GetStreamURLs = 2
	}

	if c.GetPlaylistInfo == 0 {
		c.GetPlaylistInfo = 2
	}

	if c.GetMixInfo == 0 {
		c.GetMixInfo = 2
	}

	if c.GetPagedTracks == 0 {
		c.GetPagedTracks = 2
	}

	if c.DownloadDashSegment == 0 {
		c.DownloadDashSegment = 10
	}

	if c.GetVNDTrackFileSize == 0 {
		c.GetVNDTrackFileSize = 2
	}

	if c.DownloadVNDSegment == 0 {
		c.DownloadVNDSegment = 2
	}
}

func (c *TidalDownloadTimeouts) validate() error {
	if c.GetTrackCredits < 0 {
		return errors.New("get_track_credits must be greater than 0")
	}

	if c.GetTrackLyrics < 0 {
		return errors.New("get_track_lyrics must be greater than 0")
	}

	if c.DownloadCover < 0 {
		return errors.New("download_cover must be greater than 0")
	}

	if c.GetAlbumInfo < 0 {
		return errors.New("get_album_info must be greater than 0")
	}

	if c.GetStreamURLs < 0 {
		return errors.New("get_stream_urls must be greater than 0")
	}

	if c.GetPlaylistInfo < 0 {
		return errors.New("get_playlist_info must be greater than 0")
	}

	if c.GetMixInfo < 0 {
		return errors.New("get_mix_info must be greater than 0")
	}

	if c.GetPagedTracks < 0 {
		return errors.New("get_page_tracks must be greater than 0")
	}

	if c.DownloadDashSegment < 0 {
		return errors.New("download_dash_segment must be greater than 0")
	}

	if c.GetVNDTrackFileSize < 0 {
		return errors.New("get_vnd_track_file_size must be greater than 0")
	}

	if c.DownloadVNDSegment < 0 {
		return errors.New("download_vnd_segment must be greater than 0")
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
