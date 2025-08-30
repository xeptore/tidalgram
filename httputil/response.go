package httputil

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"slices"

	"github.com/goccy/go-json"
)

func IsTokenExpiredResponse(b []byte) (bool, error) {
	var body struct {
		Status      int    `json:"status"`
		SubStatus   int    `json:"subStatus"`
		UserMessage string `json:"userMessage"`
	}
	if err := json.Unmarshal(b, &body); nil != err {
		return false, fmt.Errorf("decode response body: %v", err)
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
		return false, fmt.Errorf("decode response body: %v", err)
	}

	return body.Status == 401 &&
		body.SubStatus == 11002 &&
		body.UserMessage == "Token could not be verified", nil
}

func IsTooManyErrorResponse(resp *http.Response, respBody []byte) (bool, error) {
	if !slices.Equal(resp.Header.Values("Content-Type"), []string{"application/xml"}) {
		return false, nil
	}
	if !slices.Equal(resp.Header.Values("Server"), []string{"AmazonS3"}) {
		return false, nil
	}

	var responseBody struct {
		XMLName   xml.Name `xml:"Error"`
		Code      string   `xml:"Code"`
		Message   string   `xml:"Message"`
		RequestID string   `xml:"RequestId"`
		HostID    string   `xml:"HostId"`
	}
	if err := xml.Unmarshal(respBody, &responseBody); nil != err {
		return false, fmt.Errorf("unmarshal response body: %v", err)
	}

	return responseBody.Code == "AccessDenied" && responseBody.Message == "Access Denied", nil
}
