package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-lambda-go/lambdacontext"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vars "github.com/ocelhq/ocel/platform/aws/provider/vars/live"
	"github.com/ocelhq/ocel/platform/aws/runtime/bytecode"
	"github.com/ocelhq/ocel/platform/aws/runtime/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func main() {
	ctx := context.Background()
	start := time.Now()

	served := readArtifact()

	var prime primer
	if !executable(served) {
		prime = primeBytecode(bytecode.Start(ctx, nodeVersionFromBinary))
	}

	values, err := live.Resolve(ctx, taskRoot())
	if err != nil {
		fatalInit(fmt.Sprintf("failed to read this deployment's live variables: %v", err))
	}
	var resolved liveValues = values
	prefetch := resolved.Prefetch(ctx)

	bakedEnv, err := resolveBakedVarsEnv()
	if err != nil {
		fatalInit(fmt.Sprintf("failed to open this deployment's encrypted variables: %v", err))
	}

	proxyEnv, proxyServed, err := serveProxy(ctx, resolved.Links(), os.Getenv(stateTableEnvVar), os.Getenv(sessionPrefixEnvVar))
	if err != nil {
		fatalInit(fmt.Sprintf("failed to serve this deployment's proxied links: %v", err))
	}
	go superviseProxy(proxyServed)

	child, err := bringUp(ctx, served, resolved, prefetch, childEnv(bakedEnv, resolved, proxyEnv), start, prime)
	if err != nil {
		fatalInit(err.Error())
	}

	rt := newRuntimeClient(os.Getenv("AWS_LAMBDA_RUNTIME_API"))
	for {
		if err := handleInvocation(ctx, rt, child); err != nil {
			fmt.Fprintf(os.Stderr, "ocel: runtime loop error: %v\n", err)
			os.Exit(1)
		}
	}
}

type liveValues interface {
	Prefetch(ctx context.Context) <-chan error
	Join(done <-chan error) error
	Failure() error
	Abandoned() <-chan struct{}
	Attach(sink io.Writer)
	Refresh(ctx context.Context)
	Env() []string
	Links() []vars.Link
}

type compileCache interface {
	Key() string
	Source() bytecode.Source
	Cached() bool
	Upload(ctx context.Context, by time.Time, flush bytecode.Flush) bytecode.Outcome
}

type primer func(ctx context.Context) compileCache

func primeBytecode(p *bytecode.Priming) primer {
	return func(ctx context.Context) compileCache {
		if cache := p.Await(ctx); cache != nil {
			return cache
		}
		return nil
	}
}

type spawner func(extraEnv []string, budget time.Duration, onControl func(io.Writer), abandon <-chan struct{}) (*nodeChild, error)

func bringUp(ctx context.Context, served providerkit.FunctionConfig, values liveValues, prefetch <-chan error, env []string, start time.Time, prime primer) (child, error) {
	if executable(served) {
		return bringUpChild(func() (*execChild, error) {
			return startExecutable(served.Command, providerkit.InjectedPort, env, spawnBudget(start))
		}, values, prefetch)
	}
	return bringUpNodeWithBytecode(ctx, startNode(entrypointPath(served)), values, prefetch, env, start, prime)
}

func bringUpNodeWithBytecode(ctx context.Context, spawn spawner, values liveValues, prefetch <-chan error, env []string, start time.Time, prime primer) (*nodeChild, error) {
	var cache compileCache
	if prime != nil {
		cache = prime(ctx)
	}
	child, err := bringUpNode(spawn, values, prefetch, env, spawnBudget(start))
	if err != nil {
		return nil, err
	}
	child.cache = cache
	return child, nil
}

func spawnBudget(start time.Time) time.Duration {
	budget := startupBudget - time.Since(start)
	if budget < minSpawnBudget {
		return minSpawnBudget
	}
	return budget
}

func bringUpChild[C child](start func() (C, error), values liveValues, prefetch <-chan error) (C, error) {
	var none C
	child, err := start()
	if err != nil {
		if prefetchErr := values.Failure(); prefetchErr != nil {
			return none, fmt.Errorf("failed to resolve this deployment's live variables: %w", prefetchErr)
		}
		return none, err
	}
	if err := values.Join(prefetch); err != nil {
		return none, fmt.Errorf("failed to resolve this deployment's live variables: %w", err)
	}
	return child, nil
}

func bringUpNode(spawn spawner, values liveValues, prefetch <-chan error, env []string, budget time.Duration) (*nodeChild, error) {
	return bringUpChild(func() (*nodeChild, error) {
		child, err := spawn(env, budget, values.Attach, values.Abandoned())
		if err != nil {
			return nil, fmt.Errorf("failed to start node runtime: %w", err)
		}
		child.live = values
		return child, nil
	}, values, prefetch)
}

func childEnv(bakedEnv []string, values liveValues, proxyEnv []string) []string {
	env := make([]string, 0, len(bakedEnv)+len(proxyEnv)+1)
	env = append(env, bakedEnv...)
	if values != nil {
		env = append(env, values.Env()...)
	}
	return append(env, proxyEnv...)
}

func fatalInit(msg string) {
	api := os.Getenv("AWS_LAMBDA_RUNTIME_API")
	if api != "" {
		url := "http://" + api + "/2018-06-01/runtime/init/error"
		payload, _ := json.Marshal(map[string]string{
			"errorMessage": msg,
			"errorType":    "Ocel.InitError",
		})
		req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
		req.Header.Set("Lambda-Runtime-Function-Error-Type", "Ocel.InitError")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	os.Exit(1)
}

func handleInvocation(ctx context.Context, rt *runtimeClient, c child) error {
	inv, err := rt.next(ctx)
	if err != nil {
		return err
	}

	controlled, _ := c.(controlledChild)
	if controlled != nil {
		controlled.refreshLiveValues(ctx)
	}

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
		if err := answerWarm(ctx, controlled, rw); err != nil {
			fmt.Fprintf(os.Stderr, "ocel: deliver warm response for %s: %v\n", inv.lc.AwsRequestID, err)
		}
		return nil
	}

	var waiter <-chan struct{}
	if controlled != nil {
		waiter = controlled.beginInvocation(inv.lc.AwsRequestID)
	}
	appCtx, cancelApp := answerBefore(ctx)
	reached, err := c.endpoint().forward(appCtx, inv, rw)
	cancelApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: deliver response for %s: %v\n", inv.lc.AwsRequestID, err)
	}
	if controlled != nil {
		controlled.endInvocation(ctx, inv.lc.AwsRequestID, waiter, reached)
	}
	return nil
}

func answerBefore(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline.Add(-completionMargin))
}

type child interface {
	endpoint() upstream
}

type controlledChild interface {
	refreshLiveValues(ctx context.Context)
	beginInvocation(requestID string) <-chan struct{}
	endInvocation(ctx context.Context, requestID string, waiter <-chan struct{}, reached bool)
	answerWarmInvocation(ctx context.Context, rw *responseWriter) error
}

type upstream struct {
	port   int
	client *http.Client
}

func (u upstream) endpoint() upstream { return u }

func newLoopbackClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			MaxIdleConns:        16,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     4 * time.Second,
		},
	}
}

func (u upstream) forward(ctx context.Context, inv *invocation, rw *responseWriter) (reached bool, err error) {
	ev, err := parseEvent(inv.Payload)
	if err != nil {
		return false, u.fail(rw, http.StatusBadGateway, fmt.Sprintf("bad event payload: %v", err))
	}

	resp, err := u.forwardToApp(ctx, ev)
	if err != nil {
		return false, u.failBeforeFirstByte(ctx, rw, fmt.Sprintf("upstream request failed: %v", err))
	}
	defer resp.Body.Close()

	var first [1]byte
	n, err := io.ReadFull(resp.Body, first[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return true, u.failBeforeFirstByte(ctx, rw, fmt.Sprintf("read upstream body: %v", err))
	}
	sentinel := n == 0 && !selfTerminating(resp.StatusCode)
	if sentinel {
		resp.Header.Set(emptyBodyHeader, "1")
		resp.Header.Del("Content-Length")
	}

	prelude, err := encodePrelude(resp.StatusCode, resp.Header)
	if err != nil {
		return true, u.fail(rw, http.StatusBadGateway, fmt.Sprintf("encode prelude: %v", err))
	}
	if _, err := rw.Write(prelude); err != nil {
		return true, err
	}

	if n == 0 {
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

func (u upstream) forwardToApp(ctx context.Context, ev *httpEvent) (*http.Response, error) {
	req, err := buildForwardRequest(ctx, u.port, ev)
	if err != nil {
		return nil, err
	}
	resp, retryable, err := u.roundTrip(req)
	if err == nil || !retryable {
		return resp, err
	}

	req, buildErr := buildForwardRequest(ctx, u.port, ev)
	if buildErr != nil {
		return nil, err
	}
	resp, _, err = u.roundTrip(req)
	return resp, err
}

func buildForwardRequest(ctx context.Context, port int, ev *httpEvent) (*http.Request, error) {
	req, err := buildLoopbackRequest(ctx, port, ev)
	if err != nil {
		return nil, err
	}
	if lc, ok := lambdacontext.FromContext(ctx); ok {
		req.Header.Set("x-ocel-request-id", lc.AwsRequestID)
		req.Header.Set("x-ocel-function-arn", lc.InvokedFunctionArn)
	}
	return req, nil
}

func (u upstream) roundTrip(req *http.Request) (resp *http.Response, retryable bool, err error) {
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

	resp, err = u.client.Do(req)
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

func (u upstream) failBeforeFirstByte(ctx context.Context, rw *responseWriter, message string) error {
	if ctx.Err() == context.DeadlineExceeded {
		return u.fail(rw, http.StatusGatewayTimeout, "app did not answer before the invocation deadline")
	}
	return u.fail(rw, http.StatusBadGateway, message)
}

func (u upstream) fail(rw *responseWriter, status int, message string) error {
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

const (
	contentTypeHTTPIntegration = "application/vnd.awslambda.http-integration-response"
	headerResponseMode         = "Lambda-Runtime-Function-Response-Mode"
	responseModeStreaming      = "streaming"
	headerErrorType            = "Lambda-Runtime-Function-Error-Type"
	headerErrorBody            = "Lambda-Runtime-Function-Error-Body"
	preludeSeparatorLen        = 8

	errTypeUpstream = "Ocel.UpstreamError"

	emptyBodySentinel = "\n"
)

var emptyBodyHeader = http.CanonicalHeaderKey(edge.HeaderEmptyBody)

type prelude struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers"`
	Cookies    []string          `json:"cookies"`
}

func encodePrelude(status int, header http.Header) ([]byte, error) {
	p := prelude{
		StatusCode: status,
		Headers:    flattenHeaders(header),
		Cookies:    header.Values("Set-Cookie"),
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return append(b, make([]byte, preludeSeparatorLen)...), nil
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k := range h {
		ck := http.CanonicalHeaderKey(k)
		if ck == "Set-Cookie" || ck == "Transfer-Encoding" || strings.HasPrefix(ck, "X-Amzn-") {
			continue
		}
		out[k] = h.Get(k)
	}
	return out
}

func supervise(what string, exited <-chan error) {
	err := <-exited
	fmt.Fprintf(os.Stderr, "ocel: %s exited after startup: %v\n", what, err)
	os.Exit(1)
}

func executable(a providerkit.FunctionConfig) bool { return len(a.Command) > 0 }

func readArtifact() providerkit.FunctionConfig {
	var a providerkit.FunctionConfig
	data, err := os.ReadFile(filepath.Join(taskRoot(), providerkit.FunctionConfigFile))
	if err != nil {
		return a
	}
	if json.Unmarshal(data, &a) != nil {
		return providerkit.FunctionConfig{}
	}
	return a
}
