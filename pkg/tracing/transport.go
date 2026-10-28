package tracing

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/tracing/content"
)

const recordedBodyBytes = 8 << 10

var standardTransport = http.DefaultTransport.(*http.Transport).Clone()

var grantType = regexp.MustCompile(`(?i)grant_?type`)

type Transport struct {
	Base http.RoundTripper
	Name func(*http.Request) (string, []attribute.KeyValue, bool)
}

func NewTransport(base http.RoundTripper) http.RoundTripper {
	if traced, ok := base.(*Transport); ok {
		return traced
	}
	return &Transport{Base: base}
}

func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: NewTransport(nil), Timeout: timeout}
}

func NewBaseTransport() *http.Transport {
	return standardTransport.Clone()
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

func (t *Transport) CloseIdleConnections() {
	if closer, ok := t.base().(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	name, attrs := t.spanName(req)
	ctx, span := otel.Tracer(Scope).Start(req.Context(), name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...))
	if !span.IsRecording() {
		span.End()
		return t.base().RoundTrip(req)
	}
	span.SetAttributes(headerAttributes("http.request.header.", req.Header)...)
	sent := req.Clone(httptrace.WithClientTrace(ctx, connectionEvents(span)))
	omitted := content.IsOmitted(req.Context())
	var requestBody *bodyPrefix
	if !omitted && req.Body != nil && req.Body != http.NoBody && isTextual(req.Header.Get("Content-Type")) {
		requestBody = &bodyPrefix{}
		sent.Body = &recordedBody{ReadCloser: req.Body, prefix: requestBody}
	}

	resp, err := t.base().RoundTrip(sent)
	if requestBody != nil && grantType.MatchString(requestBody.String()) {
		omitted, requestBody = true, nil
	}
	failed := err != nil || resp.StatusCode >= http.StatusBadRequest
	recordsBodies := (failed || ReadMode() == ModeFull) && !omitted
	if err != nil {
		if recordsBodies && requestBody != nil {
			span.SetAttributes(requestBody.attributes("http.request.body")...)
		}
		EndSpan(span, err)
		return nil, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	span.SetAttributes(headerAttributes("http.response.header.", resp.Header)...)
	if failed {
		span.SetStatus(codes.Error, resp.Status)
	}
	if _, upgraded := resp.Body.(io.Writer); upgraded || resp.Body == nil || resp.Body == http.NoBody {
		if recordsBodies && requestBody != nil {
			span.SetAttributes(requestBody.attributes("http.request.body")...)
		}
		span.End()
		return resp, nil
	}
	var responseBody *bodyPrefix
	if !omitted && isTextual(resp.Header.Get("Content-Type")) {
		responseBody = &bodyPrefix{}
	}
	resp.Body = &spannedBody{
		ReadCloser:    resp.Body,
		span:          span,
		request:       requestBody,
		response:      responseBody,
		recordsBodies: recordsBodies,
	}
	return resp, nil
}

func (t *Transport) spanName(req *http.Request) (string, []attribute.KeyValue) {
	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", req.Method),
		attribute.String("url.full", req.URL.String()),
		attribute.String("server.address", req.URL.Hostname()),
	}
	if t.Name != nil {
		if name, named, ok := t.Name(req); ok {
			return name, append(attrs, named...)
		}
	}
	return req.Method + " " + req.URL.Host, attrs
}

func headerAttributes(prefix string, header http.Header) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(header))
	for name, values := range header {
		attrs = append(attrs, attribute.StringSlice(prefix+strings.ToLower(name), values))
	}
	return attrs
}

func connectionEvents(span trace.Span) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { span.AddEvent("dns.start") },
		DNSDone: func(info httptrace.DNSDoneInfo) {
			span.AddEvent("dns.done", outcome(info.Err))
		},
		ConnectStart: func(network, addr string) {
			span.AddEvent("connect.start", trace.WithAttributes(attribute.String("network.peer.address", addr)))
		},
		ConnectDone: func(network, addr string, err error) {
			span.AddEvent("connect.done", outcome(err, attribute.String("network.peer.address", addr)))
		},
		TLSHandshakeStart: func() { span.AddEvent("tls.start") },
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			span.AddEvent("tls.done", outcome(err))
		},
		GotConn: func(info httptrace.GotConnInfo) {
			span.AddEvent("connection", trace.WithAttributes(attribute.Bool("http.connection.reused", info.Reused)))
		},
		GotFirstResponseByte: func() { span.AddEvent("first_byte") },
	}
}

func outcome(err error, attrs ...attribute.KeyValue) trace.SpanStartEventOption {
	if err != nil {
		attrs = append(attrs, attribute.String("error.type", fmt.Sprintf("%T", err)))
	}
	return trace.WithAttributes(attrs...)
}

func isTextual(contentType string) bool {
	contentType = strings.ToLower(contentType)
	for _, textual := range []string{"json", "xml", "text/", "x-www-form-urlencoded", "yaml", "graphql", "javascript"} {
		if strings.Contains(contentType, textual) {
			return true
		}
	}
	return false
}

type bodyPrefix struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (p *bodyPrefix) record(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	room := recordedBodyBytes - p.buf.Len()
	if len(b) > room {
		b, p.truncated = b[:room], true
	}
	p.buf.Write(b)
}

func (p *bodyPrefix) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.String()
}

func (p *bodyPrefix) attributes(key string) []attribute.KeyValue {
	p.mu.Lock()
	defer p.mu.Unlock()
	return []attribute.KeyValue{attribute.String(key, p.buf.String()), attribute.Bool(key+".truncated", p.truncated)}
}

type recordedBody struct {
	io.ReadCloser
	prefix *bodyPrefix
}

func (b *recordedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.prefix.record(p[:n])
	return n, err
}

type spannedBody struct {
	io.ReadCloser
	span          trace.Span
	request       *bodyPrefix
	response      *bodyPrefix
	recordsBodies bool
	read          atomic.Int64
	ended         sync.Once
}

func (b *spannedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read.Add(int64(n))
	if b.response != nil {
		b.response.record(p[:n])
	}
	if err != nil {
		b.end(err)
	}
	return n, err
}

func (b *spannedBody) Close() error {
	err := b.ReadCloser.Close()
	b.end(nil)
	return err
}

func (b *spannedBody) end(err error) {
	b.ended.Do(func() {
		b.span.SetAttributes(attribute.Int64("http.response.body.size", b.read.Load()))
		if errors.Is(err, io.EOF) {
			err = nil
		}
		if b.recordsBodies || err != nil {
			if b.request != nil {
				b.span.SetAttributes(b.request.attributes("http.request.body")...)
			}
			if b.response != nil {
				b.span.SetAttributes(b.response.attributes("http.response.body")...)
			}
		}
		EndSpan(b.span, err)
	})
}
