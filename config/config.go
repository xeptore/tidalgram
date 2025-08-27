package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/gotd/td/tg"
	"github.com/rs/zerolog"
	"github.com/samber/lo"
	"gopkg.in/yaml.v3"

	"github.com/xeptore/tidalgram/redact"
)

type Config struct {
	Bot   Bot   `yaml:"bot"`
	Log   Log   `yaml:"log"`
	Tidal Tidal `yaml:"tidal"`
	TD    TD    `yaml:"td"`
}

func (c *Config) ToDict() *zerolog.Event {
	return zerolog.Dict().
		Dict("bot", c.Bot.ToDict()).
		Dict("log", c.Log.ToDict()).
		Dict("tidal", c.Tidal.ToDict()).
		Dict("td", c.TD.ToDict())
}

func (c *Config) setDefaults() {
	c.Bot.setDefaults()
	c.Log.setDefaults()
	c.Tidal.setDefaults()
	c.TD.setDefaults()
}

func (c *Config) validate() error {
	if err := c.Bot.validate(); nil != err {
		return fmt.Errorf("bot config validation failed: %v", err)
	}

	if err := c.Log.validate(); nil != err {
		return fmt.Errorf("log config validation failed: %v", err)
	}

	if err := c.Tidal.validate(); nil != err {
		return fmt.Errorf("tidal config validation failed: %v", err)
	}

	if err := c.TD.validate(); nil != err {
		return fmt.Errorf("td config validation failed: %v", err)
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

func (c *Bot) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Int64("papa_id", c.PapaID).
		Str("api_url", c.APIURL).
		Str("token", redact.String(c.Token)).
		Str("creds_dir", c.CredsDir).
		Str("downloads_dir", c.DownloadsDir)
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

type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

func (c *Log) ToDict() *zerolog.Event {
	return zerolog.Dict().
		Str("level", c.Level).
		Str("format", c.Format)
}

func (c *Log) setDefaults() {
	if c.Level == "" {
		c.Level = "info"
	}

	if c.Format == "" {
		c.Format = "pretty"
	}
}

func (c *Log) validate() error {
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

func (c *Tidal) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Dict("downloader", c.Downloader.ToDict())
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

func (c *TidalDownloader) ToDict() *zerolog.Event {
	return zerolog.Dict().
		Dict("timeouts", c.Timeouts.ToDict())
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

func (c *TidalDownloadTimeouts) ToDict() *zerolog.Event {
	return zerolog.Dict().
		Int("get_track_credits", c.GetTrackCredits).
		Int("get_track_lyrics", c.GetTrackLyrics).
		Int("download_cover", c.DownloadCover).
		Int("get_album_info", c.GetAlbumInfo).
		Int("get_stream_urls", c.GetStreamURLs).
		Int("get_playlist_info", c.GetPlaylistInfo).
		Int("get_mix_info", c.GetMixInfo).
		Int("get_paged_tracks", c.GetPagedTracks).
		Int("download_dash_segment", c.DownloadDashSegment).
		Int("get_vnd_track_file_size", c.GetVNDTrackFileSize).
		Int("download_vnd_segment", c.DownloadVNDSegment)
}

func (c *TidalDownloadTimeouts) setDefaults() {
	if c.GetTrackCredits == 0 {
		c.GetTrackCredits = 2
	}

	if c.GetTrackLyrics == 0 {
		c.GetTrackLyrics = 2
	}

	if c.DownloadCover == 0 {
		c.DownloadCover = 10
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
		c.DownloadDashSegment = 60
	}

	if c.GetVNDTrackFileSize == 0 {
		c.GetVNDTrackFileSize = 5
	}

	if c.DownloadVNDSegment == 0 {
		c.DownloadVNDSegment = 60
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

type TD struct {
	AppID   int       `yaml:"app_id"`
	AppHash string    `yaml:"app_hash"`
	Storage TDStorage `yaml:"storage"`
	Upload  TDUpload  `yaml:"upload"`
}

func (c *TD) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Int("app_id", c.AppID).
		Str("app_hash", c.AppHash).
		Dict("storage", c.Storage.ToDict()).
		Dict("upload", c.Upload.ToDict())
}

func (c *TD) setDefaults() {
	c.Storage.setDefaults()
	c.Upload.setDefaults()
}

func (c *TD) validate() error {
	if c.AppID == 0 {
		return errors.New("app_id is required")
	}

	if c.AppHash == "" {
		return errors.New("app_hash is required")
	}

	if err := c.Storage.validate(); nil != err {
		return fmt.Errorf("storage config validation failed: %v", err)
	}

	if err := c.Upload.validate(); nil != err {
		return fmt.Errorf("upload config validation failed: %v", err)
	}

	return nil
}

type TDStorage struct {
	Path string `yaml:"path"`
}

func (c *TDStorage) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Str("path", c.Path)
}

func (c *TDStorage) setDefaults() {
	if c.Path == "" {
		c.Path = "td.db"
	}
}

func (c *TDStorage) validate() error {
	return nil
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("failed to parse duration: %v", err)
	}

	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("failed to parse duration: %v", err)
	}

	d.Duration = parsed

	return nil
}

type UploadChat struct {
	tg.InputPeerClass
}

func (d *UploadChat) UnmarshalYAML(unmarshal func(any) error) error {
	var id int64
	if err := unmarshal(&id); err != nil {
		return fmt.Errorf("failed to parse chat id: %v", err)
	}

	switch id {
	case 0:
		d.InputPeerClass = &tg.InputPeerSelf{}
	default:
		d.InputPeerClass = &tg.InputPeerChat{ChatID: id}
	}

	return nil
}

type TDUpload struct {
	Threads       int        `yaml:"threads"`
	PoolSize      int        `yaml:"pool_size"`
	Limit         int        `yaml:"limit"`
	Signature     string     `yaml:"signature"`
	PauseDuration Duration   `yaml:"pause_duration"`
	ToChatID      UploadChat `yaml:"to_chat_id"`
}

func (c *TDUpload) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Int("threads", c.Threads).
		Int("pool_size", c.PoolSize).
		Int("limit", c.Limit).
		Str("signature", c.Signature).
		Str("pause_duration", c.PauseDuration.String()).
		Str("to_chat_id", c.ToChatID.String())
}

func (c *TDUpload) setDefaults() {
	if c.Threads == 0 {
		c.Threads = 8
	}

	if c.PoolSize == 0 {
		c.PoolSize = 16
	}

	if c.Limit == 0 {
		c.Limit = 4
	}

	if c.PauseDuration.Duration == 0 {
		c.PauseDuration.Duration = 5 * time.Minute
	}
}

func (c *TDUpload) validate() error {
	if c.Threads < 0 {
		return errors.New("threads must be greater than 0")
	}

	if c.PoolSize < 0 {
		return errors.New("pool_size must be greater than 0")
	}

	if c.Limit < 0 {
		return errors.New("limit must be greater than 0")
	}

	if c.PauseDuration.Duration < 0 {
		return errors.New("pause_duration must be greater than 0")
	}

	return nil
}

func Load(filename string) (*Config, error) {
	data, err := os.ReadFile(lo.Ternary(len(filename) > 0, filename, "config.yaml"))
	if nil != err {
		return nil, fmt.Errorf("failed to read config file %s: %v", filename, err)
	}

	var conf Config
	if err := yaml.Unmarshal(data, &conf); nil != err {
		return nil, fmt.Errorf("failed to parse config file %s: %v", filename, err)
	}

	conf.Bot.Token = os.Getenv("BOT_TOKEN")
	conf.setDefaults()

	if err := conf.validate(); nil != err {
		return nil, fmt.Errorf("configuration validation failed: %v", err)
	}

	return &conf, nil
}
