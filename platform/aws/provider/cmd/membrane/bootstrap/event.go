package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type httpEvent struct {
	RawPath        string   `json:"rawPath"`
	RawQueryString string   `json:"rawQueryString"`
	Cookies        []string `json:"cookies"`
	RequestContext struct {
		HTTP struct {
			Method string `json:"method"`
		} `json:"http"`
	} `json:"requestContext"`

	Path                            string              `json:"path"`
	HTTPMethod                      string              `json:"httpMethod"`
	QueryStringParameters           map[string]string   `json:"queryStringParameters"`
	MultiValueQueryStringParameters map[string][]string `json:"multiValueQueryStringParameters"`

	Headers           map[string]string   `json:"headers"`
	MultiValueHeaders map[string][]string `json:"multiValueHeaders"`

	Body            string `json:"body"`
	IsBase64Encoded bool   `json:"isBase64Encoded"`
}

func parseEvent(payload []byte) (*httpEvent, error) {
	var ev httpEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("bad event payload: %w", err)
	}
	return &ev, nil
}

func (ev *httpEvent) method() string {
	if m := ev.RequestContext.HTTP.Method; m != "" {
		return m
	}
	return ev.HTTPMethod
}

func (ev *httpEvent) path() string {
	if ev.RawPath != "" {
		return ev.RawPath
	}
	return ev.Path
}

func (ev *httpEvent) query() string {
	if ev.RawQueryString != "" {
		return ev.RawQueryString
	}
	values := url.Values{}
	if len(ev.MultiValueQueryStringParameters) > 0 {
		for name, repeated := range ev.MultiValueQueryStringParameters {
			for _, value := range repeated {
				values.Add(name, value)
			}
		}
		return values.Encode()
	}
	for name, value := range ev.QueryStringParameters {
		values.Set(name, value)
	}
	return values.Encode()
}

func (ev *httpEvent) header() http.Header {
	h := http.Header{}
	for name, value := range ev.Headers {
		h.Set(name, value)
	}
	for name, repeated := range ev.MultiValueHeaders {
		h.Del(name)
		for _, value := range repeated {
			h.Add(name, value)
		}
	}
	if len(ev.Cookies) > 0 {
		h.Set("Cookie", strings.Join(ev.Cookies, "; "))
	}
	return h
}

func (ev *httpEvent) decodedBody() ([]byte, error) {
	if !ev.IsBase64Encoded {
		return []byte(ev.Body), nil
	}
	b, err := base64.StdEncoding.DecodeString(ev.Body)
	if err != nil {
		return nil, fmt.Errorf("decode base64 body: %w", err)
	}
	return b, nil
}

func buildLoopbackRequest(ctx context.Context, nodePort int, ev *httpEvent) (*http.Request, error) {
	body, err := ev.decodedBody()
	if err != nil {
		return nil, err
	}

	target := fmt.Sprintf("http://127.0.0.1:%d%s", nodePort, ev.path())
	if q := ev.query(); q != "" {
		target += "?" + q
	}

	req, err := http.NewRequestWithContext(ctx, ev.method(), target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		req.Close = true
	}
	req.Header = ev.header()
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
