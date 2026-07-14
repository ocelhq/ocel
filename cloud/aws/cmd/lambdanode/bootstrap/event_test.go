package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestParseEvent_PayloadV2MethodPathQuery(t *testing.T) {
	payload := []byte(`{
		"version": "2.0",
		"rawPath": "/hello",
		"rawQueryString": "a=1&b=2",
		"headers": {"content-type": "application/json"},
		"requestContext": {"http": {"method": "POST", "path": "/hello"}},
		"body": "hi there",
		"isBase64Encoded": false
	}`)

	ev, err := parseEvent(payload)
	if err != nil {
		t.Fatalf("parseEvent: %v", err)
	}
	if ev.method() != "POST" {
		t.Errorf("method = %q, want POST", ev.method())
	}
	if ev.RawPath != "/hello" {
		t.Errorf("rawPath = %q, want /hello", ev.RawPath)
	}
	if ev.RawQueryString != "a=1&b=2" {
		t.Errorf("query = %q, want a=1&b=2", ev.RawQueryString)
	}
	body, err := ev.decodedBody()
	if err != nil {
		t.Fatalf("decodedBody: %v", err)
	}
	if string(body) != "hi there" {
		t.Errorf("body = %q, want %q", body, "hi there")
	}
}

func TestParseEvent_Base64BodyDecoded(t *testing.T) {
	payload := []byte(`{"version":"2.0","rawPath":"/","requestContext":{"http":{"method":"GET"}},"body":"aGk=","isBase64Encoded":true}`)

	ev, err := parseEvent(payload)
	if err != nil {
		t.Fatalf("parseEvent: %v", err)
	}
	body, err := ev.decodedBody()
	if err != nil {
		t.Fatalf("decodedBody: %v", err)
	}
	if string(body) != "hi" {
		t.Errorf("decoded body = %q, want hi", body)
	}
}

func TestBuildLoopbackRequest_PathQueryHeadersCookies(t *testing.T) {
	ev := &funcURLRequest{
		RawPath:        "/hello",
		RawQueryString: "a=1",
		Headers:        map[string]string{"content-type": "application/json"},
		Cookies:        []string{"s=1", "t=2"},
		Body:           "hi",
	}
	ev.RequestContext.HTTP.Method = "POST"

	req, err := buildLoopbackRequest(t.Context(), 4321, ev)
	if err != nil {
		t.Fatalf("buildLoopbackRequest: %v", err)
	}
	if req.Method != "POST" {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if req.URL.Host != "127.0.0.1:4321" {
		t.Errorf("host = %q, want 127.0.0.1:4321", req.URL.Host)
	}
	if req.URL.Path != "/hello" || req.URL.RawQuery != "a=1" {
		t.Errorf("url = %q, want /hello?a=1", req.URL.RequestURI())
	}
	if req.Header.Get("content-type") != "application/json" {
		t.Errorf("content-type header not forwarded: %q", req.Header.Get("content-type"))
	}
	if got := req.Header.Get("Cookie"); got != "s=1; t=2" {
		t.Errorf("Cookie = %q, want %q", got, "s=1; t=2")
	}
	got, _ := io.ReadAll(req.Body)
	if string(got) != "hi" {
		t.Errorf("body = %q, want hi", got)
	}
}

func TestEncodePrelude_JSONThenEightNullBytes(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	h.Add("Set-Cookie", "s=1")

	out, err := encodePrelude(201, h)
	if err != nil {
		t.Fatalf("encodePrelude: %v", err)
	}
	if !bytes.HasSuffix(out, make([]byte, preludeSeparatorLen)) {
		t.Fatalf("prelude must end with %d null bytes; got %q", preludeSeparatorLen, out)
	}
	jsonPart := out[:len(out)-preludeSeparatorLen]
	if bytes.Contains(jsonPart, []byte{0}) {
		t.Fatalf("prelude JSON must not contain null bytes: %q", jsonPart)
	}
	var p struct {
		StatusCode int               `json:"statusCode"`
		Headers    map[string]string `json:"headers"`
		Cookies    []string          `json:"cookies"`
	}
	if err := json.Unmarshal(jsonPart, &p); err != nil {
		t.Fatalf("prelude JSON invalid: %v (%q)", err, jsonPart)
	}
	if p.StatusCode != 201 {
		t.Errorf("statusCode = %d, want 201", p.StatusCode)
	}
	if p.Headers["Content-Type"] != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", p.Headers["Content-Type"])
	}
	if len(p.Cookies) != 1 || p.Cookies[0] != "s=1" {
		t.Errorf("cookies = %v, want [s=1]", p.Cookies)
	}
}

func TestEncodePrelude_SetCookieHeadersBecomeCookiesNotHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "text/plain")
	h.Add("Set-Cookie", "a=1")
	h.Add("Set-Cookie", "b=2")

	out, err := encodePrelude(200, h)
	if err != nil {
		t.Fatalf("encodePrelude: %v", err)
	}
	jsonPart := out[:len(out)-preludeSeparatorLen]
	var p struct {
		Headers map[string]string `json:"headers"`
		Cookies []string          `json:"cookies"`
	}
	if err := json.Unmarshal(jsonPart, &p); err != nil {
		t.Fatalf("prelude JSON invalid: %v", err)
	}
	if _, ok := p.Headers["Set-Cookie"]; ok {
		t.Errorf("Set-Cookie must not appear in headers map: %v", p.Headers)
	}
	if len(p.Cookies) != 2 {
		t.Errorf("cookies = %v, want two entries", p.Cookies)
	}
}
