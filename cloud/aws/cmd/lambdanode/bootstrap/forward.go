package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambdacontext"
)

// handleInvocation runs one iteration of the Runtime API loop: pull the next
// invocation, open the streaming response, and proxy it through to the user's
// app on loopback. It returns an error only when the Runtime API itself is
// unreachable (a failed /next) — that is fatal to the loop. Everything after
// that, including a failed response delivery, is logged and swallowed so one
// bad invocation doesn't recycle the sandbox; if the API really is down, the
// next /next fails and the loop exits then.
func handleInvocation(ctx context.Context, rt *runtimeClient, m *Membrane) error {
	inv, err := rt.next(ctx)
	if err != nil {
		return err
	}

	ctx = lambdacontext.NewContext(ctx, inv.lc)
	if inv.deadlineMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.UnixMilli(inv.deadlineMs))
		defer cancel()
	}

	rw, err := rt.startResponse(ctx, inv.lc.AwsRequestID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: start response for %s: %v\n", inv.lc.AwsRequestID, err)
		return nil
	}
	if err := m.forward(ctx, inv, rw); err != nil {
		fmt.Fprintf(os.Stderr, "ocel: deliver response for %s: %v\n", inv.lc.AwsRequestID, err)
	}
	return nil
}

// forward turns the Function URL event into a real HTTP request against the
// user's app on loopback and streams the response back through rw: a prelude
// (status + headers) followed by the body as it arrives.
func (m *Membrane) forward(ctx context.Context, inv *invocation, rw *responseWriter) error {
	ev, err := parseEvent(inv.Payload)
	if err != nil {
		return m.fail(rw, fmt.Sprintf("bad event payload: %v", err))
	}

	req, err := buildLoopbackRequest(ctx, m.nodePort, ev)
	if err != nil {
		return m.fail(rw, fmt.Sprintf("build loopback request: %v", err))
	}

	// Inject internal context the JS wrapper reads per-request (and strips
	// before the user's app sees it).
	if lc, ok := lambdacontext.FromContext(ctx); ok {
		req.Header.Set("x-ocel-request-id", lc.AwsRequestID)
		req.Header.Set("x-ocel-function-arn", lc.InvokedFunctionArn)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return m.fail(rw, fmt.Sprintf("upstream request failed: %v", err))
	}
	defer resp.Body.Close()

	prelude, err := encodePrelude(resp.StatusCode, resp.Header)
	if err != nil {
		return m.fail(rw, fmt.Sprintf("encode prelude: %v", err))
	}
	if _, err := rw.Write(prelude); err != nil {
		return err
	}

	if _, err := io.Copy(rw, resp.Body); err != nil {
		// Body is already streaming; the status/prelude can't change, so signal
		// the truncation via the response's error trailers.
		return rw.closeWithError(errTypeUpstream, err.Error())
	}
	return rw.Close()
}

// fail reports an upstream failure that occurred before any body byte was
// written: the response hasn't started, so we still own the status and send a
// clean 502 prelude.
func (m *Membrane) fail(rw *responseWriter, message string) error {
	header := http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}}
	prelude, err := encodePrelude(http.StatusBadGateway, header)
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
