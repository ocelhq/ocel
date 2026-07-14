package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// funcURLRequest is the Lambda Function URL invoke event (payload format 2.0).
// Function URLs only ever deliver this shape; the fields we don't consume are
// left off deliberately.
type funcURLRequest struct {
	RawPath        string            `json:"rawPath"`
	RawQueryString string            `json:"rawQueryString"`
	Cookies        []string          `json:"cookies"`
	Headers        map[string]string `json:"headers"`
	RequestContext struct {
		HTTP struct {
			Method string `json:"method"`
		} `json:"http"`
	} `json:"requestContext"`
	Body            string `json:"body"`
	IsBase64Encoded bool   `json:"isBase64Encoded"`
}

func parseEvent(payload []byte) (*funcURLRequest, error) {
	var ev funcURLRequest
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("bad event payload: %w", err)
	}
	return &ev, nil
}

func (ev *funcURLRequest) method() string {
	return ev.RequestContext.HTTP.Method
}

// decodedBody returns the request body, base64-decoded when the event marks it so.
func (ev *funcURLRequest) decodedBody() ([]byte, error) {
	if !ev.IsBase64Encoded {
		return []byte(ev.Body), nil
	}
	b, err := base64.StdEncoding.DecodeString(ev.Body)
	if err != nil {
		return nil, fmt.Errorf("decode base64 body: %w", err)
	}
	return b, nil
}

// buildLoopbackRequest turns the Function URL event into a real HTTP request
// against the user's app on loopback. Cookies are re-joined into a single
// Cookie header (Function URLs split them into the cookies array).
func buildLoopbackRequest(ctx context.Context, nodePort int, ev *funcURLRequest) (*http.Request, error) {
	body, err := ev.decodedBody()
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("http://127.0.0.1:%d%s", nodePort, ev.RawPath)
	if ev.RawQueryString != "" {
		url += "?" + ev.RawQueryString
	}

	req, err := http.NewRequestWithContext(ctx, ev.method(), url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range ev.Headers {
		req.Header.Set(k, v)
	}
	if len(ev.Cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(ev.Cookies, "; "))
	}
	return req, nil
}
