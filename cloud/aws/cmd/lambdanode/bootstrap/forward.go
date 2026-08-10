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

func handleInvocation(ctx context.Context, rt *runtimeClient, m *Membrane) error {
	inv, err := rt.next(ctx)
	if err != nil {
		return err
	}

	m.live.refreshIfStale(ctx)

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

	if isWarmInvocation(inv.Payload) {
		if err := m.answerWarmInvocation(ctx, rw); err != nil {
			fmt.Fprintf(os.Stderr, "ocel: deliver warm response for %s: %v\n", inv.lc.AwsRequestID, err)
		}
		return nil
	}

	waiter := m.registerWaiter(inv.lc.AwsRequestID)
	reached, err := m.forward(ctx, inv, rw)
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

func (m *Membrane) forward(ctx context.Context, inv *invocation, rw *responseWriter) (reached bool, err error) {
	ev, err := parseEvent(inv.Payload)
	if err != nil {
		return false, m.fail(rw, fmt.Sprintf("bad event payload: %v", err))
	}

	req, err := buildLoopbackRequest(ctx, m.nodePort, ev)
	if err != nil {
		return false, m.fail(rw, fmt.Sprintf("build loopback request: %v", err))
	}

	if lc, ok := lambdacontext.FromContext(ctx); ok {
		req.Header.Set("x-ocel-request-id", lc.AwsRequestID)
		req.Header.Set("x-ocel-function-arn", lc.InvokedFunctionArn)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return false, m.fail(rw, fmt.Sprintf("upstream request failed: %v", err))
	}
	defer resp.Body.Close()

	var first [1]byte
	n, err := io.ReadFull(resp.Body, first[:])
	if err != nil && err != io.EOF {
		return true, m.fail(rw, fmt.Sprintf("read upstream body: %v", err))
	}
	empty := n == 0
	sentinel := empty && !selfTerminating(resp.StatusCode)
	if sentinel {
		resp.Header.Set(emptyBodyHeader, "1")
	}

	prelude, err := encodePrelude(resp.StatusCode, resp.Header)
	if err != nil {
		return true, m.fail(rw, fmt.Sprintf("encode prelude: %v", err))
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

func selfTerminating(status int) bool {
	return status == http.StatusNoContent || status == http.StatusNotModified
}

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
