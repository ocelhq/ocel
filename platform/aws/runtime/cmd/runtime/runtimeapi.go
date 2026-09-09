package main

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/aws/aws-lambda-go/lambdacontext"
)

type runtimeClient struct {
	baseURL string
	http    *http.Client
}

const runtimeAPIVersion = "2018-06-01"

func newRuntimeClient(apiHost string) *runtimeClient {
	return &runtimeClient{
		baseURL: "http://" + apiHost + "/" + runtimeAPIVersion + "/runtime",
		http:    &http.Client{},
	}
}

type invocation struct {
	Payload    []byte
	lc         *lambdacontext.LambdaContext
	deadlineMs int64
}

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

type responseWriter struct {
	ctx    context.Context
	client *http.Client
	url    string

	pw   *io.PipeWriter
	fail chan invocationFailure
	done chan error
}

type invocationFailure struct {
	errType string
	message string
}

type failingBody struct {
	pr      *io.PipeReader
	trailer http.Header
	fail    <-chan invocationFailure
}

func (b *failingBody) Read(p []byte) (int, error) {
	n, err := b.pr.Read(p)
	if errors.Is(err, io.EOF) {
		select {
		case f := <-b.fail:
			b.trailer.Set(headerErrorType, f.errType)
			b.trailer.Set(headerErrorBody, base64.StdEncoding.EncodeToString([]byte(f.message)))
		default:
		}
	}
	return n, err
}

func (b *failingBody) Close() error { return b.pr.Close() }

func (c *runtimeClient) startResponse(ctx context.Context, requestID string) (*responseWriter, error) {
	return &responseWriter{
		ctx:    ctx,
		client: c.http,
		url:    c.baseURL + "/invocation/" + requestID + "/response",
	}, nil
}

func (w *responseWriter) stream() error {
	if w.pw != nil {
		return nil
	}
	pr, pw := io.Pipe()
	fail := make(chan invocationFailure, 1)
	body := &failingBody{pr: pr, fail: fail}
	req, err := http.NewRequestWithContext(w.ctx, http.MethodPost, w.url, body)
	if err != nil {
		pw.Close()
		return err
	}
	req.Header.Set(headerResponseMode, responseModeStreaming)
	req.Header.Set("Content-Type", contentTypeHTTPIntegration)
	req.Trailer = http.Header{
		headerErrorType: nil,
		headerErrorBody: nil,
	}

	body.trailer = req.Trailer
	w.pw, w.fail, w.done = pw, fail, make(chan error, 1)
	go func() {
		resp, err := w.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		w.done <- err
	}()
	return nil
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if err := w.stream(); err != nil {
		return 0, err
	}
	return w.pw.Write(p)
}

func (w *responseWriter) Close() error {
	if err := w.stream(); err != nil {
		return err
	}
	if err := w.pw.Close(); err != nil {
		return err
	}
	return <-w.done
}

func (w *responseWriter) closeWithError(errType, message string) error {
	if err := w.stream(); err != nil {
		return err
	}
	select {
	case w.fail <- invocationFailure{errType: errType, message: message}:
	default:
	}
	if err := w.pw.Close(); err != nil {
		return err
	}
	return <-w.done
}
