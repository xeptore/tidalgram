package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/lo"
	"gopkg.in/yaml.v3"

	"github.com/xeptore/tidalgram/redact"
)

type Config struct {
	Bot      Bot      `yaml:"bot"`
	Log      Log      `yaml:"log"`
	Tidal    Tidal    `yaml:"tidal"`
	Telegram Telegram `yaml:"telegram"`
}

func (conf *Config) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Dict("bot", conf.Bot.ToDict()).
		Dict("log", conf.Log.ToDict()).
		Dict("tidal", conf.Tidal.ToDict()).
		Dict("telegram", conf.Telegram.ToDict())
}

func (conf *Config) setDefaults() {
	conf.Bot.setDefaults()
	conf.Log.setDefaults()
	conf.Tidal.setDefaults()
	conf.Telegram.setDefaults()
}

func (conf *Config) validate() error {
	if err := conf.Bot.validate(); nil != err {
		return fmt.Errorf("bot config validation: %v", err)
	}

	if err := conf.Log.validate(); nil != err {
		return fmt.Errorf("log config validation: %v", err)
	}

	if err := conf.Tidal.validate(); nil != err {
		return fmt.Errorf("tidal config validation: %v", err)
	}

	if err := conf.Telegram.validate(); nil != err {
		return fmt.Errorf("telegram config validation: %v", err)
	}

	return nil
}

type Bot struct {
	PapaID       int64    `yaml:"papa_id"`
	APIURL       string   `yaml:"api_url"`
	Token        string   `yaml:"-"`
	CredsDir     string   `yaml:"creds_dir"`
	DownloadsDir string   `yaml:"downloads_dir"`
	Proxy        BotProxy `yaml:"proxy"`
}

func (b *Bot) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Int64("papa_id", b.PapaID).
		Str("api_url", b.APIURL).
		Str("token", redact.String(b.Token)).
		Str("creds_dir", b.CredsDir).
		Str("downloads_dir", b.DownloadsDir).
		Dict("proxy", b.Proxy.ToDict())
}

func (b *Bot) setDefaults() {
	if b.APIURL == "" {
		b.APIURL = "https://api.telegram.org"
	}

	if b.CredsDir == "" {
		b.CredsDir = "./creds"
	}

	if b.DownloadsDir == "" {
		b.DownloadsDir = "./downloads"
	}

	b.Proxy.setDefaults()
}

type BotProxy struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

func (bp *BotProxy) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Str("host", bp.Host).
		Int("port", bp.Port).
		Str("username", redact.String(bp.Username)).
		Str("password", redact.String(bp.Password))
}

func (bp *BotProxy) setDefaults() {}

func (bp *BotProxy) validate() error {
	if len(bp.Host) > 0 {
		if bp.Port == 0 {
			return errors.New("port is required if host is set")
		}
	}

	if bp.Port != 0 {
		if bp.Port < 0 {
			return errors.New("port must be greater than or equal to 0")
		}

		if bp.Port > 65535 {
			return errors.New("port must be less than or equal to 65535")
		}

		if bp.Host == "" {
			return errors.New("host is required if port is set")
		}
	}

	return nil
}

func (b *Bot) validate() error {
	if b.PapaID == 0 {
		return errors.New("papa_id is required")
	}

	if b.Token == "" {
		return errors.New("make sure the BOT_TOKEN environment variable is set")
	}

	if i, err := os.Lstat(b.CredsDir); nil != err {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("creds_dir does not exist")
		}

		return fmt.Errorf("stat creds_dir: %v", err)
	} else if !i.IsDir() {
		return errors.New("creds_dir must be a directory")
	}

	if i, err := os.Lstat(b.DownloadsDir); nil != err {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("downloads_dir does not exist")
		}

		return fmt.Errorf("stat downloads_dir: %v", err)
	} else if !i.IsDir() {
		return errors.New("downloads_dir must be a directory")
	}

	if err := b.Proxy.validate(); nil != err {
		return fmt.Errorf("proxy config validation: %v", err)
	}

	return nil
}

type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

func (l *Log) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Str("level", l.Level).
		Str("format", l.Format)
}

func (l *Log) setDefaults() {
	if l.Level == "" {
		l.Level = "info"
	}

	if l.Format == "" {
		l.Format = "pretty"
	}
}

func (l *Log) validate() error {
	if !slices.Contains([]string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}, l.Level) {
		return fmt.Errorf(
			"level must be one of: trace, debug, info, warn, error, fatal, panic, got: %s",
			l.Level,
		)
	}

	if !slices.Contains([]string{"json", "pretty"}, l.Format) {
		return fmt.Errorf("format must be 'json' or 'pretty', got: %s", l.Format)
	}

	return nil
}

type Tidal struct {
	Downloader TidalDownloader `yaml:"downloader"`
}

func (t *Tidal) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Dict("downloader", t.Downloader.ToDict())
}

func (t *Tidal) setDefaults() {
	t.Downloader.setDefaults()
}

func (t *Tidal) validate() error {
	if err := t.Downloader.validate(); nil != err {
		return fmt.Errorf("downloader config validation: %v", err)
	}

	return nil
}

type TidalDownloader struct {
	Timeouts    TidalDownloadTimeouts    `yaml:"timeouts"`
	Concurrency TidalDownloadConcurrency `yaml:"concurrency"`
}

func (td *TidalDownloader) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Dict("timeouts", td.Timeouts.ToDict()).
		Dict("concurrency", td.Concurrency.ToDict())
}

func (td *TidalDownloader) setDefaults() {
	td.Timeouts.setDefaults()
	td.Concurrency.setDefaults()
}

func (td *TidalDownloader) validate() error {
	if err := td.Timeouts.validate(); nil != err {
		return fmt.Errorf("timeouts config validation: %v", err)
	}

	if err := td.Concurrency.validate(); nil != err {
		return fmt.Errorf("concurrency config validation: %v", err)
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

func (tdt *TidalDownloadTimeouts) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Int("get_track_credits", tdt.GetTrackCredits).
		Int("get_track_lyrics", tdt.GetTrackLyrics).
		Int("download_cover", tdt.DownloadCover).
		Int("get_album_info", tdt.GetAlbumInfo).
		Int("get_stream_urls", tdt.GetStreamURLs).
		Int("get_playlist_info", tdt.GetPlaylistInfo).
		Int("get_mix_info", tdt.GetMixInfo).
		Int("get_paged_tracks", tdt.GetPagedTracks).
		Int("download_dash_segment", tdt.DownloadDashSegment).
		Int("get_vnd_track_file_size", tdt.GetVNDTrackFileSize).
		Int("download_vnd_segment", tdt.DownloadVNDSegment)
}

func (tdt *TidalDownloadTimeouts) setDefaults() {
	if tdt.GetTrackCredits == 0 {
		tdt.GetTrackCredits = 2
	}

	if tdt.GetTrackLyrics == 0 {
		tdt.GetTrackLyrics = 2
	}

	if tdt.DownloadCover == 0 {
		tdt.DownloadCover = 10
	}

	if tdt.GetAlbumInfo == 0 {
		tdt.GetAlbumInfo = 2
	}

	if tdt.GetStreamURLs == 0 {
		tdt.GetStreamURLs = 2
	}

	if tdt.GetPlaylistInfo == 0 {
		tdt.GetPlaylistInfo = 2
	}

	if tdt.GetMixInfo == 0 {
		tdt.GetMixInfo = 2
	}

	if tdt.GetPagedTracks == 0 {
		tdt.GetPagedTracks = 2
	}

	if tdt.DownloadDashSegment == 0 {
		tdt.DownloadDashSegment = 60
	}

	if tdt.GetVNDTrackFileSize == 0 {
		tdt.GetVNDTrackFileSize = 5
	}

	if tdt.DownloadVNDSegment == 0 {
		tdt.DownloadVNDSegment = 60
	}
}

func (tdt *TidalDownloadTimeouts) validate() error {
	if tdt.GetTrackCredits < 0 {
		return errors.New("get_track_credits must be greater than 0")
	}

	if tdt.GetTrackLyrics < 0 {
		return errors.New("get_track_lyrics must be greater than 0")
	}

	if tdt.DownloadCover < 0 {
		return errors.New("download_cover must be greater than 0")
	}

	if tdt.GetAlbumInfo < 0 {
		return errors.New("get_album_info must be greater than 0")
	}

	if tdt.GetStreamURLs < 0 {
		return errors.New("get_stream_urls must be greater than 0")
	}

	if tdt.GetPlaylistInfo < 0 {
		return errors.New("get_playlist_info must be greater than 0")
	}

	if tdt.GetMixInfo < 0 {
		return errors.New("get_mix_info must be greater than 0")
	}

	if tdt.GetPagedTracks < 0 {
		return errors.New("get_page_tracks must be greater than 0")
	}

	if tdt.DownloadDashSegment < 0 {
		return errors.New("download_dash_segment must be greater than 0")
	}

	if tdt.GetVNDTrackFileSize < 0 {
		return errors.New("get_vnd_track_file_size must be greater than 0")
	}

	if tdt.DownloadVNDSegment < 0 {
		return errors.New("download_vnd_segment must be greater than 0")
	}

	return nil
}

type TidalDownloadConcurrency struct {
	AlbumTracks    int `yaml:"album_tracks"`
	PlaylistTracks int `yaml:"playlist_tracks"`
	MixTracks      int `yaml:"mix_tracks"`
	VNDTrackParts  int `yaml:"vnd_track_parts"`
}

func (tdc *TidalDownloadConcurrency) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Int("album_tracks", tdc.AlbumTracks).
		Int("playlist_tracks", tdc.PlaylistTracks).
		Int("mix_tracks", tdc.MixTracks).
		Int("vnd_track_parts", tdc.VNDTrackParts)
}

func (tdc *TidalDownloadConcurrency) setDefaults() {
	if tdc.AlbumTracks == 0 {
		tdc.AlbumTracks = 20
	}

	if tdc.PlaylistTracks == 0 {
		tdc.PlaylistTracks = 20
	}

	if tdc.MixTracks == 0 {
		tdc.MixTracks = 20
	}

	if tdc.VNDTrackParts == 0 {
		tdc.VNDTrackParts = 5
	}
}

func (tdc *TidalDownloadConcurrency) validate() error {
	if tdc.AlbumTracks < 0 {
		return errors.New("album_tracks must be greater than 0")
	}

	if tdc.PlaylistTracks < 0 {
		return errors.New("playlist_tracks must be greater than 0")
	}

	if tdc.MixTracks < 0 {
		return errors.New("mix_tracks must be greater than 0")
	}

	if tdc.VNDTrackParts < 0 {
		return errors.New("vnd_track_parts must be greater than 0")
	}

	return nil
}

type Telegram struct {
	AppID   int             `yaml:"app_id"`
	AppHash string          `yaml:"app_hash"`
	Storage TelegramStorage `yaml:"storage"`
	Proxy   TelegramProxy   `yaml:"proxy"`
	Upload  TelegramUpload  `yaml:"upload"`
}

func (tg *Telegram) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Int("app_id", tg.AppID).
		Str("app_hash", tg.AppHash).
		Dict("storage", tg.Storage.ToDict()).
		Dict("proxy", tg.Proxy.ToDict()).
		Dict("upload", tg.Upload.ToDict())
}

func (tg *Telegram) setDefaults() {
	tg.Storage.setDefaults()
	tg.Proxy.setDefaults()
	tg.Upload.setDefaults()
}

func (tg *Telegram) validate() error {
	if tg.AppID == 0 {
		return errors.New("app_id is required")
	}

	if tg.AppHash == "" {
		return errors.New("app_hash is required")
	}

	if err := tg.Storage.validate(); nil != err {
		return fmt.Errorf("storage config validation: %v", err)
	}

	if err := tg.Proxy.validate(); nil != err {
		return fmt.Errorf("proxy config validation: %v", err)
	}

	if err := tg.Upload.validate(); nil != err {
		return fmt.Errorf("upload config validation: %v", err)
	}

	return nil
}

type TelegramStorage struct {
	Path string `yaml:"path"`
}

func (ts *TelegramStorage) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Str("path", ts.Path)
}

func (ts *TelegramStorage) setDefaults() {
	if ts.Path == "" {
		ts.Path = "./telegram.db"
	}
}

func (ts *TelegramStorage) validate() error {
	return nil
}

type TelegramProxy struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

func (tp *TelegramProxy) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Str("host", tp.Host).
		Int("port", tp.Port).
		Str("username", redact.String(tp.Username)).
		Str("password", redact.String(tp.Password))
}

func (tp *TelegramProxy) setDefaults() {}

func (tp *TelegramProxy) validate() error {
	if len(tp.Host) > 0 {
		if tp.Port == 0 {
			return errors.New("port is required if host is set")
		}

		if tp.Port < 0 {
			return errors.New("port must be greater than or equal to 0")
		}
	}

	if tp.Port != 0 {
		if tp.Port < 0 {
			return errors.New("port must be greater than or equal to 0")
		}

		if tp.Port > 65535 {
			return errors.New("port must be less than or equal to 65535")
		}

		if tp.Host == "" {
			return errors.New("host is required if port is set")
		}
	}

	return nil
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); nil != err {
		return fmt.Errorf("parse duration: %v", err)
	}

	parsed, err := time.ParseDuration(s)
	if nil != err {
		return fmt.Errorf("parse duration: %v", err)
	}

	d.Duration = parsed

	return nil
}

type TelegramUpload struct {
	Threads       int                `yaml:"threads"`
	PoolSize      int                `yaml:"pool_size"`
	Limit         int                `yaml:"limit"`
	Signature     string             `yaml:"signature"`
	Peer          TelegramUploadPeer `yaml:"peer"`
	PauseDuration Duration           `yaml:"pause_duration"`
}

func (tu *TelegramUpload) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Int("threads", tu.Threads).
		Int("pool_size", tu.PoolSize).
		Int("limit", tu.Limit).
		Str("signature", tu.Signature).
		Dict("peer", tu.Peer.ToDict()).
		Dur("pause_duration", tu.PauseDuration.Duration)
}

func (tu *TelegramUpload) setDefaults() {
	if tu.Threads == 0 {
		tu.Threads = 8
	}

	if tu.PoolSize == 0 {
		tu.PoolSize = 8
	}

	if tu.Limit == 0 {
		tu.Limit = 4
	}

	if tu.PauseDuration.Duration == 0 {
		tu.PauseDuration.Duration = 1500 * time.Millisecond
	}

	tu.Peer.setDefaults()
}

func (tu *TelegramUpload) validate() error {
	if tu.Threads < 0 {
		return errors.New("threads must be greater than 0")
	}

	if tu.PoolSize < 0 {
		return errors.New("pool_size must be greater than 0")
	}

	if tu.Limit < 0 {
		return errors.New("limit must be greater than 0")
	}

	if tu.PauseDuration.Duration < 0 {
		return errors.New("pause_duration must be greater than 0")
	}

	if err := tu.Peer.validate(); nil != err {
		return fmt.Errorf("peer config validation: %v", err)
	}

	return nil
}

type TelegramUploadPeer struct {
	ID   int64  `yaml:"id"`
	Kind string `yaml:"kind"`
}

func (tup *TelegramUploadPeer) ToDict() *zerolog.Event {
	return zerolog.
		Dict().
		Int64("id", tup.ID).
		Str("kind", tup.Kind)
}

func (tup *TelegramUploadPeer) setDefaults() {}

func (tup *TelegramUploadPeer) validate() error {
	if tup.ID == 0 {
		return errors.New("id is required")
	}

	if tup.Kind == "" {
		return errors.New("kind is required")
	} else if !slices.Contains([]string{"user", "chat", "channel"}, tup.Kind) {
		return fmt.Errorf("invalid peer kind: %s. must be one of: user, chat, channel", tup.Kind)
	}

	return nil
}

func Load(filename string) (*Config, error) {
	data, err := os.ReadFile(lo.Ternary(len(filename) > 0, filename, "config.yaml"))
	if nil != err {
		return nil, fmt.Errorf("read config file %s: %v", filename, err)
	}

	var conf Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&conf); nil != err {
		return nil, fmt.Errorf("parse config file %s: %v", filename, err)
	}

	conf.Bot.Token = os.Getenv("BOT_TOKEN")
	conf.setDefaults()

	if err := conf.validate(); nil != err {
		return nil, fmt.Errorf("configuration validation: %v", err)
	}

	return &conf, nil
}
