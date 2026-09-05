package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"syscall"
	"time"

	"github.com/aws/aws-lambda-go/lambdacontext"
)

func handleInvocation(ctx context.Context, rt *runtimeClient, m *nodeChild) error {
	inv, err := rt.next(ctx)
	if err != nil {
		return err
	}

	m.live.refreshIfStale(ctx)

	ctx = lambdacontext.NewContext(ctx, inv.lc)
	rw, err := rt.startResponse(ctx, inv.lc.AwsRequestID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: start response for %s: %v\n", inv.lc.AwsRequestID, err)
		return nil
	}
	if inv.deadlineMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.UnixMilli(inv.deadlineMs))
		defer cancel()
	}

	if isWarmInvocation(inv.Payload) {
		if err := m.answerWarmInvocation(ctx, rw); err != nil {
			fmt.Fprintf(os.Stderr, "ocel: deliver warm response for %s: %v\n", inv.lc.AwsRequestID, err)
		}
		return nil
	}

	waiter := m.registerWaiter(inv.lc.AwsRequestID)
	appCtx, cancelApp := answerBefore(ctx)
	reached, err := m.forward(appCtx, inv, rw)
	cancelApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: deliver response for %s: %v\n", inv.lc.AwsRequestID, err)
	}
	if !reached {
		m.signalComplete(inv.lc.AwsRequestID)
	}
	m.awaitCompletion(ctx, inv.lc.AwsRequestID, waiter)

	m.uploadBytecodeCacheOnce(ctx)
	return nil
}

func answerBefore(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline.Add(-completionMargin))
}

func (m *nodeChild) forward(ctx context.Context, inv *invocation, rw *responseWriter) (reached bool, err error) {
	ev, err := parseEvent(inv.Payload)
	if err != nil {
		return false, m.fail(rw, http.StatusBadGateway, fmt.Sprintf("bad event payload: %v", err))
	}

	resp, err := m.forwardToNode(ctx, ev)
	if err != nil {
		return false, m.failBeforeFirstByte(ctx, rw, fmt.Sprintf("upstream request failed: %v", err))
	}
	defer resp.Body.Close()

	var first [1]byte
	n, err := io.ReadFull(resp.Body, first[:])
	if err != nil && err != io.EOF {
		return true, m.failBeforeFirstByte(ctx, rw, fmt.Sprintf("read upstream body: %v", err))
	}
	empty := n == 0
	sentinel := empty && !selfTerminating(resp.StatusCode)
	if sentinel {
		resp.Header.Set(emptyBodyHeader, "1")
	}

	prelude, err := encodePrelude(resp.StatusCode, resp.Header)
	if err != nil {
		return true, m.fail(rw, http.StatusBadGateway, fmt.Sprintf("encode prelude: %v", err))
	}
	if _, err := rw.Write(prelude); err != nil {
		return true, err
	}

	if empty {
		if sentinel {
			if _, err := rw.Write([]byte(emptyBodySentinel)); err != nil {
				return true, err
			}
		}
		return true, rw.Close()
	}
	if _, err := rw.Write(first[:n]); err != nil {
		return true, err
	}

	if _, err := io.Copy(rw, resp.Body); err != nil {
		return true, rw.closeWithError(errTypeUpstream, err.Error())
	}
	return true, rw.Close()
}

func (m *nodeChild) forwardToNode(ctx context.Context, ev *httpEvent) (*http.Response, error) {
	req, err := buildForwardRequest(ctx, m.nodePort, ev)
	if err != nil {
		return nil, err
	}
	resp, retryable, err := m.roundTrip(req)
	if err == nil || !retryable {
		return resp, err
	}

	req, buildErr := buildForwardRequest(ctx, m.nodePort, ev)
	if buildErr != nil {
		return nil, err
	}
	resp, _, err = m.roundTrip(req)
	return resp, err
}

func buildForwardRequest(ctx context.Context, nodePort int, ev *httpEvent) (*http.Request, error) {
	req, err := buildLoopbackRequest(ctx, nodePort, ev)
	if err != nil {
		return nil, err
	}
	if lc, ok := lambdacontext.FromContext(ctx); ok {
		req.Header.Set("x-ocel-request-id", lc.AwsRequestID)
		req.Header.Set("x-ocel-function-arn", lc.InvokedFunctionArn)
	}
	return req, nil
}

func (m *nodeChild) roundTrip(req *http.Request) (resp *http.Response, retryable bool, err error) {
	var reused, wrote bool
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			reused = info.Reused
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			wrote = info.Err == nil
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	resp, err = m.client.Do(req)
	if err == nil {
		return resp, false, nil
	}
	return nil, reused && !wrote && isStaleConnError(err), err
}

func isStaleConnError(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET)
}

func selfTerminating(status int) bool {
	return status == http.StatusNoContent || status == http.StatusNotModified
}

func (m *nodeChild) failBeforeFirstByte(ctx context.Context, rw *responseWriter, message string) error {
	if ctx.Err() == context.DeadlineExceeded {
		return m.fail(rw, http.StatusGatewayTimeout, "app did not answer before the invocation deadline")
	}
	return m.fail(rw, http.StatusBadGateway, message)
}

func (m *nodeChild) fail(rw *responseWriter, status int, message string) error {
	header := http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}}
	prelude, err := encodePrelude(status, header)
	if err != nil {
		return rw.closeWithError(errTypeUpstream, message)
	}
	if _, err := rw.Write(prelude); err != nil {
		return err
	}
	if _, err := rw.Write([]byte(message)); err != nil {
		return err
	}
	return rw.Close()
}
