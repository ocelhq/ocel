package main

import (
	"context"
	"encoding/base64"
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

	pw      *io.PipeWriter
	trailer http.Header
	done    chan error
}

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
	req, err := http.NewRequestWithContext(w.ctx, http.MethodPost, w.url, pr)
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

	w.pw, w.trailer, w.done = pw, req.Trailer, make(chan error, 1)
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
	w.trailer.Set(headerErrorType, errType)
	w.trailer.Set(headerErrorBody, base64.StdEncoding.EncodeToString([]byte(message)))
	if err := w.pw.Close(); err != nil {
		return err
	}
	return <-w.done
}
