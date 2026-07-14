package main

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strconv"

	"github.com/aws/aws-lambda-go/lambdacontext"
)

// runtimeClient is a minimal Lambda Runtime API client. aws-lambda-go has no
// response-streaming support, so we drive the loop ourselves: poll /next, then
// stream the response back via /response with the http-integration-response
// content type.
type runtimeClient struct {
	baseURL string
	http    *http.Client
}

const runtimeAPIVersion = "2018-06-01"

func newRuntimeClient(apiHost string) *runtimeClient {
	return &runtimeClient{
		baseURL: "http://" + apiHost + "/" + runtimeAPIVersion + "/runtime",
		// /next long-polls until an invocation arrives; never time it out.
		http: &http.Client{},
	}
}

// invocation is one pending invoke: the raw event payload plus the parsed
// per-invoke context. deadlineMs is the Runtime API's invocation deadline (unix
// epoch millis, 0 if absent), applied to the request context so a hung upstream
// is cut off.
type invocation struct {
	Payload    []byte
	lc         *lambdacontext.LambdaContext
	deadlineMs int64
}

// next blocks on the Runtime API until an invocation is delivered.
func (c *runtimeClient) next(ctx context.Context) (*invocation, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/invocation/next", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	deadlineMs, _ := strconv.ParseInt(resp.Header.Get("Lambda-Runtime-Deadline-Ms"), 10, 64)
	return &invocation{
		Payload:    payload,
		deadlineMs: deadlineMs,
		lc: &lambdacontext.LambdaContext{
			AwsRequestID:       resp.Header.Get("Lambda-Runtime-Aws-Request-Id"),
			InvokedFunctionArn: resp.Header.Get("Lambda-Runtime-Invoked-Function-Arn"),
		},
	}, nil
}

// responseWriter streams one invocation's response body to the Runtime API. The
// POST /response request body is an io.Pipe so writes flush as chunks; the
// error trailers are pre-declared and only populated if the response fails
// after body bytes have already gone out.
type responseWriter struct {
	pw   *io.PipeWriter
	req  *http.Request
	done chan error
}

// startResponse opens the streaming POST /response for requestID and returns a
// writer to stream the prelude and body into.
func (c *runtimeClient) startResponse(ctx context.Context, requestID string) (*responseWriter, error) {
	pr, pw := io.Pipe()
	url := c.baseURL + "/invocation/" + requestID + "/response"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, pr)
	if err != nil {
		pw.Close()
		return nil, err
	}
	req.Header.Set(headerResponseMode, responseModeStreaming)
	req.Header.Set("Content-Type", contentTypeHTTPIntegration)
	// The pipe body has no known length, so the transport streams it chunked on
	// its own — no Transfer-Encoding header needed.
	// Pre-declare the error trailers; they stay empty (and are omitted) unless a
	// mid-stream failure populates them.
	req.Trailer = http.Header{
		headerErrorType: nil,
		headerErrorBody: nil,
	}

	w := &responseWriter{pw: pw, req: req, done: make(chan error, 1)}
	go func() {
		resp, err := c.http.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		w.done <- err
	}()
	return w, nil
}

func (w *responseWriter) Write(p []byte) (int, error) {
	return w.pw.Write(p)
}

// Close finishes a successful streamed response and waits for the Runtime API
// POST to complete.
func (w *responseWriter) Close() error {
	if err := w.pw.Close(); err != nil {
		return err
	}
	return <-w.done
}

// closeWithError signals a failure that surfaced after body bytes were already
// streamed: the prelude's status can no longer change, so the truncation is
// reported via the response's error trailers instead.
func (w *responseWriter) closeWithError(errType, message string) error {
	w.req.Trailer.Set(headerErrorType, errType)
	w.req.Trailer.Set(headerErrorBody, base64.StdEncoding.EncodeToString([]byte(message)))
	if err := w.pw.Close(); err != nil {
		return err
	}
	return <-w.done
}
