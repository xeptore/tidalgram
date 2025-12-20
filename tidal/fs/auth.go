package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-json"
)

type AuthFile string

func AuthFileFrom(dir, filename string) AuthFile {
	return AuthFile(filepath.Join(dir, filename))
}

type AuthFileContent struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	CountryCode  string `json:"country_code"`
}

func (f AuthFile) Read() (c *AuthFileContent, err error) {
	file, err := os.OpenFile(f.path(), os.O_RDONLY, 0o0600)
	if nil != err {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}

		return nil, fmt.Errorf("open token file: %v", err)
	}
	defer func() {
		if closeErr := file.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("close token file: %v", closeErr))
		}
	}()

	dec := json.NewDecoder(file)
	dec.DisallowUnknownFields()
	if err := dec.DecodeWithOption(&c, json.DecodeFieldPriorityFirstWin()); nil != err {
		return nil, fmt.Errorf("decode token file contents: %v", err)
	}

	return c, nil
}

func (f AuthFile) Write(c AuthFileContent) (err error) {
	file, err := os.OpenFile(f.path(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_SYNC, 0o0600)
	if nil != err {
		return fmt.Errorf("open token file: %v", err)
	}
	defer func() {
		if closeErr := file.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("close token file: %v", closeErr))
		}
	}()

	if err := json.NewEncoder(file).EncodeWithOption(c); nil != err {
		return fmt.Errorf("encode token file: %v", err)
	}

	return nil
}

func (f AuthFile) path() string {
	return string(f)
}
