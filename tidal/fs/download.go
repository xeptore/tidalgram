package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-json"

	"github.com/xeptore/tidalgram/tidal/types"
)

type DownloadsDir string

func DownloadsDirFrom(d string) DownloadsDir {
	return DownloadsDir(d)
}

func (d DownloadsDir) Album(id string) Album {
	dirPath := d.path()

	return Album{
		DirPath:  dirPath,
		InfoFile: InfoFile[types.StoredAlbum]{Path: filepath.Join(dirPath, id+".json")},
		Cover:    Cover{Path: filepath.Join(dirPath, id+".jpg")},
	}
}

type Album struct {
	DirPath  string
	InfoFile InfoFile[types.StoredAlbum]
	Cover    Cover
}

func (a Album) Track(vol int, id string) AlbumTrack {
	trackPath := filepath.Join(a.DirPath, id)

	return AlbumTrack{
		Path:     trackPath,
		InfoFile: InfoFile[types.StoredAlbumTrack]{Path: trackPath + ".json"},
	}
}

type AlbumTrack struct {
	Path     string
	InfoFile InfoFile[types.StoredAlbumTrack]
}

func (t AlbumTrack) AlreadyDownloaded() (bool, error) {
	return fileExists(t.Path)
}

func (t AlbumTrack) Remove() error {
	if err := os.Remove(t.Path); nil != err && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove album track: %v", err)
	}

	return nil
}

func (d DownloadsDir) Track(id string) Track {
	trackPath := filepath.Join(d.path(), id)

	return Track{
		Path:     trackPath,
		InfoFile: InfoFile[types.StoredTrack]{Path: trackPath + ".json"},
		Cover:    Cover{Path: trackPath + ".jpg"},
	}
}

func (d DownloadsDir) Playlist(id string) Playlist {
	dirPath := d.path()

	return Playlist{
		DirPath:  dirPath,
		InfoFile: InfoFile[types.StoredPlaylist]{Path: filepath.Join(dirPath, id+".json")},
	}
}

type Playlist struct {
	DirPath  string
	InfoFile InfoFile[types.StoredPlaylist]
}

func (p Playlist) Track(id string) Track {
	trackPath := filepath.Join(p.DirPath, id)

	return Track{
		Path:     trackPath,
		InfoFile: InfoFile[types.StoredTrack]{Path: trackPath + ".json"},
		Cover:    Cover{Path: trackPath + ".jpg"},
	}
}

func (d DownloadsDir) Mix(id string) Mix {
	dirPath := d.path()

	return Mix{
		DirPath:  dirPath,
		InfoFile: InfoFile[types.StoredMix]{Path: filepath.Join(dirPath, id+".json")},
	}
}

func (d DownloadsDir) path() string {
	return string(d)
}

type Mix struct {
	DirPath  string
	InfoFile InfoFile[types.StoredMix]
}

func (d Mix) Track(id string) Track {
	trackPath := filepath.Join(d.DirPath, id)

	return Track{
		Path:     trackPath,
		InfoFile: InfoFile[types.StoredTrack]{Path: trackPath + ".json"},
		Cover:    Cover{Path: trackPath + ".jpg"},
	}
}

type Cover struct {
	Path string
}

func (c Cover) AlreadyDownloaded() (bool, error) {
	return fileExists(c.Path)
}

func fileExists(path string) (bool, error) {
	if _, err := os.Lstat(path); nil != err {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}

		return false, fmt.Errorf("stat file: %v", err)
	}

	return true, nil
}

func (c Cover) Write(b []byte) (err error) {
	f, err := os.OpenFile(c.Path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_SYNC, 0o600)
	if nil != err {
		return fmt.Errorf("open cover file for write: %v", err)
	}
	defer func() {
		if nil != err {
			if removeErr := os.Remove(c.Path); nil != removeErr {
				if !errors.Is(removeErr, os.ErrNotExist) {
					err = errors.Join(err, fmt.Errorf("remove cover file: %v", removeErr))
				}
			}
		} else if closeErr := f.Close(); nil != closeErr {
			err = fmt.Errorf("close cover file: %v", closeErr)
		}
	}()

	if _, err := f.Write(b); nil != err {
		return fmt.Errorf("write cover file: %v", err)
	}

	if err := f.Sync(); nil != err {
		return fmt.Errorf("sync cover file: %v", err)
	}

	return nil
}

func (c Cover) Read() ([]byte, error) {
	b, err := os.ReadFile(c.Path)
	if nil != err {
		return nil, fmt.Errorf("read cover file: %v", err)
	}

	return b, nil
}

type Track struct {
	Path     string
	InfoFile InfoFile[types.StoredTrack]
	Cover    Cover
}

func (t Track) AlreadyDownloaded() (bool, error) {
	return fileExists(t.Path)
}

func (t Track) Remove() error {
	if err := os.Remove(t.Path); nil != err && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove single track: %v", err)
	}

	return nil
}

type InfoFile[T any] struct {
	Path string
}

func (p InfoFile[T]) Read() (*T, error) {
	return readInfoFile(p)
}

func readInfoFile[T any](file InfoFile[T]) (t *T, err error) {
	filePath := file.Path

	f, err := os.OpenFile(filePath, os.O_RDONLY, 0o0600)
	if nil != err {
		return nil, fmt.Errorf("open info file for read: %v", err)
	}
	defer func() {
		if closeErr := f.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("close info file: %v", closeErr))
		}
	}()

	var out T
	if err := json.NewDecoder(f).Decode(&out); nil != err {
		return nil, fmt.Errorf("decode info file contents: %v", err)
	}

	return &out, nil
}

func (p InfoFile[T]) Write(v T) error {
	return writeInfoFile(p, v)
}

func writeInfoFile[T any](file InfoFile[T], obj any) (err error) {
	filePath := file.Path

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o0600)
	if nil != err {
		return fmt.Errorf("open info file for write: %v", err)
	}
	defer func() {
		if nil != err {
			if removeErr := os.Remove(filePath); nil != removeErr {
				if !errors.Is(removeErr, os.ErrNotExist) {
					err = errors.Join(err, fmt.Errorf("remove info file: %v", removeErr))
				}
			}
		} else if closeErr := f.Close(); nil != closeErr {
			err = fmt.Errorf("close info file: %v", closeErr)
		}
	}()

	if err := json.NewEncoder(f).Encode(obj); nil != err {
		return fmt.Errorf("write info content: %v", err)
	}

	if err := f.Sync(); nil != err {
		return fmt.Errorf("sync info file: %v", err)
	}

	return nil
}
