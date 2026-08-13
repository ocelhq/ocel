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
	if len(body) > 0 {
		req.Close = true
	}
	for k, v := range ev.Headers {
		req.Header.Set(k, v)
	}
	if len(ev.Cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(ev.Cookies, "; "))
	}
	if host := publicHost(req.Header); host != "" {
		req.Host = host
	}
	return req, nil
}

func publicHost(h http.Header) string {
	for _, name := range []string{"X-Forwarded-Host", "Host"} {
		if v := strings.TrimSpace(strings.Split(h.Get(name), ",")[0]); v != "" {
			return v
		}
	}
	return ""
}
