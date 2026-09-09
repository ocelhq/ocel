package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseEvent(t *testing.T) {
	t.Run("payload V2 method path query", func(t *testing.T) {
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
	})

	t.Run("base64 body decoded", func(t *testing.T) {
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
	})
	t.Run("payload V1 method path query", func(t *testing.T) {
		payload := []byte(`{
			"version": "1.0",
			"path": "/hello",
			"httpMethod": "POST",
			"queryStringParameters": {"a": "2"},
			"multiValueQueryStringParameters": {"a": ["1", "2"]},
			"headers": {"content-type": "application/json", "accept": "text/html"},
			"multiValueHeaders": {"accept": ["text/html", "application/json"]},
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
		if ev.path() != "/hello" {
			t.Errorf("path = %q, want /hello", ev.path())
		}
		if ev.query() != "a=1&a=2" {
			t.Errorf("query = %q, want a=1&a=2; a repeated parameter must survive", ev.query())
		}
		if got := ev.header().Values("Accept"); len(got) != 2 || got[0] != "text/html" || got[1] != "application/json" {
			t.Errorf("Accept = %v, want both values the REST API sent", got)
		}
		if got := ev.header().Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want the single-valued header carried through", got)
		}
		body, err := ev.decodedBody()
		if err != nil {
			t.Fatalf("decodedBody: %v", err)
		}
		if string(body) != "hi there" {
			t.Errorf("body = %q, want %q", body, "hi there")
		}
	})

	t.Run("payload V1 without repeated parameters", func(t *testing.T) {
		ev, err := parseEvent([]byte(`{"version":"1.0","path":"/","httpMethod":"GET","queryStringParameters":{"a":"1"}}`))
		if err != nil {
			t.Fatalf("parseEvent: %v", err)
		}
		if ev.query() != "a=1" {
			t.Errorf("query = %q, want a=1", ev.query())
		}
	})

	t.Run("payload V1 carries cookies in a header", func(t *testing.T) {
		ev, err := parseEvent([]byte(`{"version":"1.0","path":"/","httpMethod":"GET","headers":{"Cookie":"s=1; t=2"}}`))
		if err != nil {
			t.Fatalf("parseEvent: %v", err)
		}
		req, err := buildLoopbackRequest(t.Context(), 4321, ev)
		if err != nil {
			t.Fatalf("buildLoopbackRequest: %v", err)
		}
		if got := req.Header.Get("Cookie"); got != "s=1; t=2" {
			t.Errorf("Cookie = %q, want the header a REST API sends cookies in", got)
		}
	})
}

func TestBuildLoopbackRequestFromARestProxyEvent(t *testing.T) {
	ev, err := parseEvent([]byte(`{
		"version": "1.0",
		"path": "/blog/post",
		"httpMethod": "POST",
		"multiValueQueryStringParameters": {"tag": ["a", "b"]},
		"multiValueHeaders": {"x-try": ["1", "2"], "host": ["shop.example.com"]},
		"body": "aGk=",
		"isBase64Encoded": true
	}`))
	if err != nil {
		t.Fatalf("parseEvent: %v", err)
	}

	req, err := buildLoopbackRequest(t.Context(), 4321, ev)
	if err != nil {
		t.Fatalf("buildLoopbackRequest: %v", err)
	}
	if req.Method != "POST" {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if req.URL.Path != "/blog/post" {
		t.Errorf("path = %q, want /blog/post", req.URL.Path)
	}
	if req.URL.RawQuery != "tag=a&tag=b" {
		t.Errorf("query = %q, want tag=a&tag=b", req.URL.RawQuery)
	}
	if got := req.Header.Values("X-Try"); len(got) != 2 {
		t.Errorf("X-Try = %v, want both values", got)
	}
	if req.Host != "shop.example.com" {
		t.Errorf("req.Host = %q, want the public authority the REST API forwarded", req.Host)
	}
	body, _ := io.ReadAll(req.Body)
	if string(body) != "hi" {
		t.Errorf("body = %q, want the base64 body decoded", body)
	}
}

func TestBuildLoopbackRequest(t *testing.T) {
	t.Run("path query headers cookies", func(t *testing.T) {
		ev := &httpEvent{
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
	})

	t.Run("host is the public authority", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			headers map[string]string
			want    string
		}{
			{
				name:    "forwarded host wins",
				headers: map[string]string{"x-forwarded-host": "app.ocel.site", "host": "abc.lambda-url.us-east-1.on.aws"},
				want:    "app.ocel.site",
			},
			{
				name:    "first forwarded host of a list",
				headers: map[string]string{"x-forwarded-host": "app.ocel.site, proxy.internal"},
				want:    "app.ocel.site",
			},
			{
				name:    "falls back to host",
				headers: map[string]string{"host": "abc.lambda-url.us-east-1.on.aws"},
				want:    "abc.lambda-url.us-east-1.on.aws",
			},
			{
				name:    "empty forwarded host falls back",
				headers: map[string]string{"x-forwarded-host": "", "host": "abc.lambda-url.us-east-1.on.aws"},
				want:    "abc.lambda-url.us-east-1.on.aws",
			},
			{
				name:    "no authority header leaves the loopback host",
				headers: map[string]string{},
				want:    "127.0.0.1:4321",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ev := &httpEvent{RawPath: "/", Headers: tc.headers}
				ev.RequestContext.HTTP.Method = "GET"

				req, err := buildLoopbackRequest(t.Context(), 4321, ev)
				if err != nil {
					t.Fatalf("buildLoopbackRequest: %v", err)
				}
				if req.Host != tc.want {
					t.Errorf("req.Host = %q, want %q", req.Host, tc.want)
				}
			})
		}
	})
}

func TestEncodePrelude(t *testing.T) {
	t.Run("JSON then eight null bytes", func(t *testing.T) {
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
	})

	t.Run("set cookie headers become cookies not headers", func(t *testing.T) {
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
	})

	t.Run("the edge header the router set reaches the prelude", func(t *testing.T) {
		h := http.Header{}
		h.Set("Content-Type", "text/html")
		h.Set("X-Ocel-Edge", "cloudfront")

		out, err := encodePrelude(200, h)
		if err != nil {
			t.Fatalf("encodePrelude: %v", err)
		}
		var p struct {
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(out[:len(out)-preludeSeparatorLen], &p); err != nil {
			t.Fatalf("prelude JSON invalid: %v", err)
		}
		if p.Headers["X-Ocel-Edge"] != "cloudfront" {
			t.Errorf("headers = %v, want the edge header the router set carried through; it is the only thing that marks a streamed response", p.Headers)
		}
	})

	t.Run("reserved x-amzn headers are dropped", func(t *testing.T) {
		h := http.Header{}
		h.Set("Content-Type", "text/html")
		h.Set("X-Powered-By", "Next.js")
		h.Set("X-Amzn-Requestid", "3f1c0b6e-0000-0000-0000-000000000000")
		h.Set("X-Amzn-Trace-Id", "Root=1-00000000-000000000000000000000000")
		h.Set("X-Amzn-Remapped-Date", "Fri, 15 Aug 2026 00:00:00 GMT")
		h.Add("Set-Cookie", "a=1")

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
		for k := range p.Headers {
			if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Amzn-") {
				t.Errorf("reserved header %q must not appear in prelude: %v", k, p.Headers)
			}
		}
		if p.Headers["Content-Type"] != "text/html" {
			t.Errorf("Content-Type = %q, want text/html", p.Headers["Content-Type"])
		}
		if p.Headers["X-Powered-By"] != "Next.js" {
			t.Errorf("X-Powered-By = %q, want Next.js", p.Headers["X-Powered-By"])
		}
		if len(p.Cookies) != 1 || p.Cookies[0] != "a=1" {
			t.Errorf("cookies = %v, want [a=1]", p.Cookies)
		}
	})
}

func unreadBodyServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				if _, err := http.ReadRequest(r); err != nil {
					return
				}
				const body = "ok"
				io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\nConnection: keep-alive\r\n\r\n"+body)
				io.Copy(io.Discard, r)
			}(conn)
		}
	}()

	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestLoopbackClientDoesNotHangOnAConnectionPoisonedByAnUnreadBody(t *testing.T) {
	port := unreadBodyServer(t)
	client := newLoopbackClient()

	unread := &httpEvent{RawPath: "/_middleware", Body: string(bytes.Repeat([]byte("a"), 300_000))}
	unread.RequestContext.HTTP.Method = "POST"
	req1, err := buildLoopbackRequest(t.Context(), port, unread)
	if err != nil {
		t.Fatalf("buildLoopbackRequest: %v", err)
	}
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	io.ReadAll(resp1.Body)
	resp1.Body.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	next := &httpEvent{RawPath: "/file"}
	next.RequestContext.HTTP.Method = "GET"
	req2, err := buildLoopbackRequest(ctx, port, next)
	if err != nil {
		t.Fatalf("buildLoopbackRequest: %v", err)
	}

	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("second request hung instead of completing: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp2.StatusCode)
	}
}
