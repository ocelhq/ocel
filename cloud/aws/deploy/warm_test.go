package deploy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// fakeInvoker answers every warm invoke from respond, recording the calls and
// the peak number in flight so the cap can be asserted without an AWS client.
type fakeInvoker struct {
	respond func(ctx context.Context, name string) (*lambda.InvokeOutput, error)

	mu       sync.Mutex
	calls    []string
	inFlight int
	peak     int
}

func (f *fakeInvoker) Invoke(ctx context.Context, in *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	name := aws.ToString(in.FunctionName)
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.inFlight++
	if f.inFlight > f.peak {
		f.peak = f.inFlight
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	return f.respond(ctx, name)
}

func (f *fakeInvoker) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// answering builds an invoker that replies to every function with the same raw
// membrane response body.
func answering(body string) *fakeInvoker {
	return &fakeInvoker{respond: func(context.Context, string) (*lambda.InvokeOutput, error) {
		return &lambda.InvokeOutput{StatusCode: 200, Payload: []byte(body)}, nil
	}}
}

// collectLog is the log func realize threads through, captured for assertions.
func collectLog() (func(string), func() string) {
	var mu sync.Mutex
	var b strings.Builder
	return func(line string) {
			mu.Lock()
			defer mu.Unlock()
			b.WriteString(line)
			b.WriteString("\n")
		}, func() string {
			mu.Lock()
			defer mu.Unlock()
			return b.String()
		}
}

func warmTestTargets(n int) []warmTarget {
	targets := make([]warmTarget, 0, n)
	for i := range n {
		targets = append(targets, warmTarget{
			App:          "web",
			LogicalName:  "web_bundle_" + string(rune('a'+i)),
			FunctionName: "ocel-fn-" + string(rune('a'+i)),
		})
	}
	return targets
}

func runWarm(t *testing.T, invoker FunctionInvoker, targets []warmTarget, budget time.Duration) string {
	t.Helper()
	log, dump := collectLog()
	warmPass{invoker: invoker, targets: targets, budget: budget, log: log}.run(context.Background())
	return dump()
}

// The warm pass exists to fill the bytecode cache, so its targets must be
// exactly the functions the bytecode feature is on for: an app with no ISR
// cache gets no OCEL_BYTECODE_PREFIX and so has nothing to publish, and a
// function whose stack never reported a physical name cannot be addressed.
func TestWarmTargets_OnlyBytecodeGatedFunctions(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		Slug: "proj",
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_index", Framework: "next", App: "web"},
			{LogicalName: "web_api", Framework: "next", App: "web"},
			{LogicalName: "api_handler", App: "api"},
		},
	}
	caches := map[string]*isrConfig{"web": {Prefix: "prod/proj/web/B1"}}
	names := map[string]string{
		"web_index":   "ocel-web-index-abc",
		"api_handler": "ocel-api-handler-def",
	}

	targets := warmTargets(manifest, caches, names)

	if len(targets) != 1 {
		t.Fatalf("warmTargets = %+v, want only web_index", targets)
	}
	if targets[0].FunctionName != "ocel-web-index-abc" || targets[0].App != "web" {
		t.Errorf("warmTargets[0] = %+v, want web_index on app web", targets[0])
	}
}

// The gate is the deploying process's own OCEL_BYTECODE_CACHE=0: with it off no
// function is deployed with a prefix, so warming every one of them would spend
// the deploy's time invoking functions that publish nothing.
func TestWarmTargets_SkippedWhenGateIsOff(t *testing.T) {
	t.Setenv(bytecodeCacheEnv, "0")

	targets := warmTargets(nextManifest(), map[string]*isrConfig{"web": {Prefix: "p"}}, map[string]string{"web_index": "fn"})

	if len(targets) != 0 {
		t.Errorf("warmTargets with the gate off = %+v, want none", targets)
	}
}

func TestWarmPass_ReportsPublished(t *testing.T) {
	out := runWarm(t, answering(`{"state":"published","entries":51,"loaded":47,"bytes":63963136,"uploaded":true}`),
		warmTestTargets(1), time.Minute)

	for _, want := range []string{"warming 1 bundle", "web_bundle_a", "app=web", "47/51 entries", "61.0 MiB published", "warmed 1/1 bundles"} {
		if !strings.Contains(out, want) {
			t.Errorf("warm log missing %q:\n%s", want, out)
		}
	}
}

// already-cached is a success: the deploy cannot pre-check the key (only the
// sandbox knows node's full version), so this is the membrane's answer for a
// cache that was already complete.
func TestWarmPass_ReportsAlreadyCached(t *testing.T) {
	out := runWarm(t, answering(`{"state":"already-cached","entries":51,"loaded":51}`), warmTestTargets(1), time.Minute)

	if !strings.Contains(out, "already cached") || !strings.Contains(out, "warmed 1/1 bundles") {
		t.Errorf("warm log = %s, want an already-cached success", out)
	}
}

func TestWarmPass_ReportsDisabledAndFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"disabled", `{"state":"disabled"}`, "bytecode caching is off"},
		{"failed", `{"state":"failed","error":"put denied","entries":51,"loaded":12}`, "put denied"},
		{"unknown state", `{"state":"pondering"}`, `"pondering"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runWarm(t, answering(tc.body), warmTestTargets(1), time.Minute)

			if !strings.Contains(out, tc.want) || !strings.Contains(out, "not warmed") {
				t.Errorf("warm log = %s, want %q and a not-warmed line", out, tc.want)
			}
			if !strings.Contains(out, "warmed 0/1 bundles") {
				t.Errorf("warm log = %s, want no bundle counted warm", out)
			}
		})
	}
}

// Every failure is a warning and nothing more: a deploy that fails because a
// cache did not warm is worse than a slow cold start.
func TestWarmPass_FailuresDegradeToWarnings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		invoker *fakeInvoker
		want    string
	}{
		{
			"invoke error",
			&fakeInvoker{respond: func(context.Context, string) (*lambda.InvokeOutput, error) {
				return nil, errors.New("dial tcp: no route to host")
			}},
			"no route to host",
		},
		{
			"throttled",
			&fakeInvoker{respond: func(context.Context, string) (*lambda.InvokeOutput, error) {
				return nil, &lambdatypes.TooManyRequestsException{}
			}},
			"throttled",
		},
		{
			"function error",
			&fakeInvoker{respond: func(context.Context, string) (*lambda.InvokeOutput, error) {
				return &lambda.InvokeOutput{StatusCode: 200, FunctionError: aws.String("Unhandled")}, nil
			}},
			"Unhandled",
		},
		{
			"non-2xx",
			&fakeInvoker{respond: func(context.Context, string) (*lambda.InvokeOutput, error) {
				return &lambda.InvokeOutput{StatusCode: 502}, nil
			}},
			"502",
		},
		{
			"garbled body",
			answering(`{"state":`),
			"unreadable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runWarm(t, tc.invoker, warmTestTargets(1), time.Minute)

			if !strings.Contains(out, tc.want) {
				t.Errorf("warm log = %s, want %q", out, tc.want)
			}
			if !strings.Contains(out, "warmed 0/1 bundles") {
				t.Errorf("warm log = %s, want the pass to complete reporting nothing warmed", out)
			}
		})
	}
}

// The rationed resource is Lambda's account concurrency, shared with everything
// else running: an unbounded fan-out throttles most invocations and leaves most
// bundles unwarmed.
func TestWarmPass_CapsConcurrency(t *testing.T) {
	invoker := &fakeInvoker{respond: func(context.Context, string) (*lambda.InvokeOutput, error) {
		time.Sleep(5 * time.Millisecond)
		return &lambda.InvokeOutput{StatusCode: 200, Payload: []byte(`{"state":"published"}`)}, nil
	}}

	runWarm(t, invoker, warmTestTargets(12), time.Minute)

	if invoker.peak > warmConcurrency {
		t.Errorf("peak in-flight invokes = %d, want at most %d", invoker.peak, warmConcurrency)
	}
	if got := len(invoker.called()); got != 12 {
		t.Errorf("invoked %d functions, want all 12", got)
	}
}

// A bundle the deadline cut off must be named, not silently dropped: an
// unwarmed bundle is a cold start the first real request pays for, and the
// deploy output is the only place that ever surfaces.
func TestWarmPass_DeadlineNamesWhatItSkipped(t *testing.T) {
	invoker := &fakeInvoker{respond: func(ctx context.Context, _ string) (*lambda.InvokeOutput, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}

	out := runWarm(t, invoker, warmTestTargets(8), 20*time.Millisecond)

	if !strings.Contains(out, "warmed 0/8 bundles") {
		t.Errorf("warm log = %s, want nothing warmed", out)
	}
	for _, target := range warmTestTargets(8)[warmConcurrency:] {
		if !strings.Contains(out, target.LogicalName) {
			t.Errorf("warm log never names the skipped bundle %s:\n%s", target.LogicalName, out)
		}
	}
	if !strings.Contains(out, "ran out of time") {
		t.Errorf("warm log = %s, want the deadline named as the reason", out)
	}
}

// The payload is the membrane's contract: a deliberately non-HTTP shape the
// edge's own event envelope can never produce.
func TestWarmPass_SendsTheWarmPayload(t *testing.T) {
	var got lambda.InvokeInput
	invoker := &fakeInvoker{respond: func(context.Context, string) (*lambda.InvokeOutput, error) {
		return &lambda.InvokeOutput{StatusCode: 200, Payload: []byte(`{"state":"published"}`)}, nil
	}}
	capture := &capturingInvoker{inner: invoker, into: &got}

	runWarm(t, capture, warmTestTargets(1), time.Minute)

	if string(got.Payload) != warmPayload {
		t.Errorf("payload = %s, want %s", got.Payload, warmPayload)
	}
	if got.InvocationType != lambdatypes.InvocationTypeRequestResponse {
		t.Errorf("invocation type = %s, want RequestResponse", got.InvocationType)
	}
}

type capturingInvoker struct {
	inner FunctionInvoker
	into  *lambda.InvokeInput
}

func (c *capturingInvoker) Invoke(ctx context.Context, in *lambda.InvokeInput, optFns ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	*c.into = *in
	return c.inner.Invoke(ctx, in, optFns...)
}

// No targets means no output at all: a deploy with the feature off, or with no
// Next app, must read exactly as it did before the warm pass existed.
func TestWarmPass_SilentWithNoTargets(t *testing.T) {
	if out := runWarm(t, answering(`{"state":"published"}`), nil, time.Minute); out != "" {
		t.Errorf("warm log = %q, want nothing", out)
	}
}

// A nil invoker is a caller that wired none (a deploy path predating the warm
// pass): it must skip rather than panic mid-deploy.
func TestWarmPass_SkipsWithoutAnInvoker(t *testing.T) {
	if out := runWarm(t, nil, warmTestTargets(1), time.Minute); out != "" {
		t.Errorf("warm log = %q, want nothing", out)
	}
}

// The physical Lambda name is Pulumi-autonamed, so the stack output is the only
// thing that can tell the warm pass what to invoke.
func TestCollectAppFunctionOutputs_ReadsPhysicalNames(t *testing.T) {
	functions := []*deploymentsv1.ManifestFunction{
		{LogicalName: "web_index"},
		{LogicalName: "web_api"},
	}
	outputs := auto.OutputMap{
		"web_index": {Value: map[string]interface{}{
			outputKeyFunctionURL:  "https://index.lambda-url.aws/",
			outputKeyFunctionName: "ocel-web-index-a1b2",
		}},
		// A stack that reported no name at all: the URL still deploys, the
		// bundle simply is not warmed.
		"web_api": {Value: map[string]interface{}{outputKeyFunctionURL: "https://api.lambda-url.aws/"}},
	}

	outs, names, err := collectAppFunctionOutputs(functions, outputs)
	if err != nil {
		t.Fatalf("collectAppFunctionOutputs: %v", err)
	}

	if len(outs) != 2 {
		t.Fatalf("got %d outputs, want 2", len(outs))
	}
	if names["web_index"] != "ocel-web-index-a1b2" {
		t.Errorf("names[web_index] = %q, want the physical Lambda name", names["web_index"])
	}
	if _, ok := names["web_api"]; ok {
		t.Errorf("names[web_api] = %q, want no entry for a function that reported no name", names["web_api"])
	}
}
