package httputil

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/goccy/go-json"
)

func readResponseBody(resp *http.Response) ([]byte, error) {
	respBody, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	return respBody, nil
}

func ReadResponseBody(resp *http.Response) ([]byte, error) {
	respBody, err := readResponseBody(resp)
	if nil != err {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("unexpected empty response body")
		}
	}

	return respBody, nil
}

func ReadOptionalResponseBody(resp *http.Response) ([]byte, error) {
	respBody, err := ReadResponseBody(resp)
	if nil != err && !errors.Is(err, io.EOF) {
		return nil, err
	}

	return respBody, nil
}

func IsTokenExpiredResponse(b []byte) (bool, error) {
	var body struct {
		Status      int    `json:"status"`
		SubStatus   int    `json:"subStatus"`
		UserMessage string `json:"userMessage"`
	}
	if err := json.Unmarshal(b, &body); nil != err {
		return false, fmt.Errorf("failed to decode 401 status code response body: %v", err)
	}

	return body.Status == 401 &&
		body.SubStatus == 11003 &&
		body.UserMessage == "The token has expired. (Expired on time)", nil
}

func IsTokenInvalidResponse(b []byte) (bool, error) {
	var body struct {
		Status      int    `json:"status"`
		SubStatus   int    `json:"subStatus"`
		UserMessage string `json:"userMessage"`
	}
	if err := json.Unmarshal(b, &body); nil != err {
		return false, fmt.Errorf("failed to decode 401 status code response body: %v", err)
	}

	return body.Status == 401 &&
		body.SubStatus == 11002 &&
		body.UserMessage == "Token could not be verified", nil
}
