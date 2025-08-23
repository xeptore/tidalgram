package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"
	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/tidal/fs"
)

const (
	clientID      = "zU4XHVVkc2tDPo4t"
	clientSecret  = "VJKhDFqJPqvsPVNBV6ukXTJmwlvbttP7wlMlrc72se4=" //nolint:gosec
	baseURL       = "https://auth.tidal.com/v1/oauth2"
	tokenFileName = "tidal.json"
)

var (
	ErrUnauthorized     = errors.New("unauthorized")
	ErrLoginLinkExpired = errors.New("login link has expired")
	ErrLoginInProgress  = errors.New("another login flow is in progress")
)

type Auth struct {
	credsDir    string
	credentials atomic.Pointer[Credentials]
}

type Credentials struct {
	Token        string
	RefreshToken string
	ExpiresAt    time.Time
}

func New(logger zerolog.Logger, dir string) (*Auth, error) {
	content, err := fs.AuthFileFrom(dir, tokenFileName).Read()
	if nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("failed to read auth credentials file: %v", err)
		}
		// can be ignored as it will be filled with defaults
		logger.Debug().Msg("auth credentials file not found. Using defaults.")
	}

	creds := &Credentials{
		Token:        "",
		RefreshToken: "",
		ExpiresAt:    time.Time{},
	}
	if content != nil {
		creds = &Credentials{
			Token:        content.Token,
			RefreshToken: content.RefreshToken,
			ExpiresAt:    time.Unix(content.ExpiresAt, 0),
		}
	}

	a := &Auth{
		credentials: atomic.Pointer[Credentials]{},
		credsDir:    dir,
	}
	a.credentials.Store(creds)

	return a, nil
}

func (a *Auth) Credentials() *Credentials {
	return a.credentials.Load()
}

func extractExpiresAt(accessToken string) (time.Time, error) {
	splits := strings.SplitN(accessToken, ".", 3)
	if len(splits) != 3 {
		return time.Time{}, fmt.Errorf("unexpected access token format: %s", accessToken)
	}
	var obj struct {
		ExpiresAt int64 `json:"exp"`
	}
	payload := strings.NewReader(splits[1])
	dec := base64.NewDecoder(base64.StdEncoding, payload)
	if err := json.NewDecoder(dec).Decode(&obj); nil != err {
		return time.Time{}, fmt.Errorf("failed to decode access token payload: %v", err)
	}

	return time.Unix(obj.ExpiresAt, 0).UTC(), nil
}
