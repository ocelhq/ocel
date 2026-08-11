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

func answering(body string) *fakeInvoker {
	return &fakeInvoker{respond: func(context.Context, string) (*lambda.InvokeOutput, error) {
		return &lambda.InvokeOutput{StatusCode: 200, Payload: []byte(body)}, nil
	}}
}

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

type capturingInvoker struct {
	inner FunctionInvoker
	into  *lambda.InvokeInput
}

func (c *capturingInvoker) Invoke(ctx context.Context, in *lambda.InvokeInput, optFns ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	*c.into = *in
	return c.inner.Invoke(ctx, in, optFns...)
}

func TestWarmTargets(t *testing.T) {
	t.Run("only bytecode gated functions", func(t *testing.T) {
		t.Setenv(bytecodeCacheEnv, "1")
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
	})

	t.Run("skipped when the gate is off", func(t *testing.T) {
		t.Setenv(bytecodeCacheEnv, "")

		targets := warmTargets(nextManifest(), map[string]*isrConfig{"web": {Prefix: "p"}}, map[string]string{"web_index": "fn"})

		if len(targets) != 0 {
			t.Errorf("warmTargets with the gate off = %+v, want none", targets)
		}
	})
}

func TestWarmPass(t *testing.T) {
	t.Run("reports published", func(t *testing.T) {
		out := runWarm(t, answering(`{"state":"published","entries":51,"loaded":47,"bytes":63963136,"uploaded":true,"key":"prod/p/web/B1/bytecode/fn/node24.3.1-arm64.tar.gz"}`),
			warmTestTargets(1), time.Minute)

		for _, want := range []string{
			"warming 1 bundle (", "web_bundle_a", "app=web", "47/51 entries", "61.0 MiB published",
			"node24.3.1-arm64.tar.gz", "warmed 1/1 bundles",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("warm log missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("counts bundles in words", func(t *testing.T) {
		out := runWarm(t, answering(`{"state":"published","uploaded":true}`), warmTestTargets(1), time.Minute)

		if strings.Contains(out, "warming 1 bundles") {
			t.Errorf("warm log = %s, want a singular bundle", out)
		}
	})

	t.Run("names the entries that stayed cold", func(t *testing.T) {
		out := runWarm(t, answering(`{"state":"published","entries":51,"loaded":38,"uploaded":true,`+
			`"stoppedBy":"ceiling","skippedCount":13,"skipped":["app/a/page","app/b/page"]}`),
			warmTestTargets(1), time.Minute)

		for _, want := range []string{"13 entries skipped by ceiling", "app/a/page", "app/b/page", "(+11 not listed)"} {
			if !strings.Contains(out, want) {
				t.Errorf("warm log missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("reports uncounted coverage as unknown", func(t *testing.T) {
		out := runWarm(t, answering(`{"state":"published","uploaded":true,"bytes":1048576,`+
			`"uncounted":"node did not report back on the compile-cache warm"}`),
			warmTestTargets(1), time.Minute)

		if !strings.Contains(out, "entry counts unknown (node did not report back") {
			t.Errorf("warm log = %s, want the counts reported as unknown with the reason", out)
		}
		if !strings.Contains(out, "warmed 1/1 bundles") {
			t.Errorf("warm log = %s, want a published cache counted as warmed", out)
		}
		if strings.Contains(out, "0/0 entries") {
			t.Errorf("warm log = %s, want no counts it never measured", out)
		}
	})

	t.Run("published without uploading is not warmed", func(t *testing.T) {
		out := runWarm(t, answering(`{"state":"published","entries":51,"loaded":51,"uploaded":false}`),
			warmTestTargets(1), time.Minute)

		if !strings.Contains(out, "without uploading") || !strings.Contains(out, "warmed 0/1 bundles") {
			t.Errorf("warm log = %s, want the contradiction reported as not warmed", out)
		}
	})

	t.Run("reports already cached", func(t *testing.T) {
		out := runWarm(t, answering(`{"state":"already-cached","entries":51,"loaded":51}`), warmTestTargets(1), time.Minute)

		if !strings.Contains(out, "already cached") || !strings.Contains(out, "warmed 1/1 bundles") {
			t.Errorf("warm log = %s, want an already-cached success", out)
		}
	})

	t.Run("reports disabled and failed", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			body string
			want string
		}{
			{"disabled", `{"state":"disabled","error":"this deployment resolved no bytecode cache identity"}`, "resolved no bytecode cache identity"},
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
	})

	t.Run("failures degrade to warnings", func(t *testing.T) {
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
	})

	t.Run("caps concurrency", func(t *testing.T) {
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
	})

	t.Run("deadline names what it skipped", func(t *testing.T) {
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
	})

	t.Run("sends the warm payload", func(t *testing.T) {
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
	})

	t.Run("silent with no targets", func(t *testing.T) {
		if out := runWarm(t, answering(`{"state":"published"}`), nil, time.Minute); out != "" {
			t.Errorf("warm log = %q, want nothing", out)
		}
	})

	t.Run("skips without an invoker", func(t *testing.T) {
		if out := runWarm(t, nil, warmTestTargets(1), time.Minute); out != "" {
			t.Errorf("warm log = %q, want nothing", out)
		}
	})

	t.Run("reads a prelude framed answer", func(t *testing.T) {
		framed := `{"statusCode":200,"headers":{},"cookies":[]}` + string(make([]byte, 8)) +
			`{"state":"published","entries":9,"loaded":9,"uploaded":true}`

		out := runWarm(t, answering(framed), warmTestTargets(1), time.Minute)

		if !strings.Contains(out, "9/9 entries") || !strings.Contains(out, "warmed 1/1 bundles") {
			t.Errorf("warm log = %s, want a prelude-framed answer read as a published cache", out)
		}
	})
}

func TestParseWarmReply(t *testing.T) {
	t.Parallel()

	t.Run("reads bare and prelude framed payloads", func(t *testing.T) {
		t.Parallel()

		summary := `{"state":"published","entries":51,"loaded":51,"uploaded":true}`
		prelude := `{"statusCode":200,"headers":{"content-type":"application/json"},"cookies":[]}`

		for _, tc := range []struct {
			name    string
			payload []byte
		}{
			{"bare json", []byte(summary)},
			{"prelude framed", append(append([]byte(prelude), make([]byte, 8)...), summary...)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				reply, err := parseWarmReply(tc.payload)
				if err != nil {
					t.Fatalf("parseWarmReply: %v", err)
				}
				if reply.State != warmStatePublished || reply.Loaded != 51 || !reply.Uploaded {
					t.Errorf("reply = %+v, want the membrane's own summary", reply)
				}
			})
		}
	})

	t.Run("rejects a payload with no summary", func(t *testing.T) {
		t.Parallel()

		if _, err := parseWarmReply([]byte("Internal Server Error")); err == nil {
			t.Error("parseWarmReply() err = nil, want an unreadable payload reported")
		}
	})
}

func TestCollectAppFunctionOutputs(t *testing.T) {
	t.Parallel()

	t.Run("reads physical names", func(t *testing.T) {
		t.Parallel()

		functions := []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_index"},
			{LogicalName: "web_api"},
		}
		outputs := auto.OutputMap{
			"web_index": {Value: map[string]any{
				outputKeyFunctionURL:  "https://index.lambda-url.aws/",
				outputKeyFunctionName: "ocel-web-index-a1b2",
			}},
			"web_api": {Value: map[string]any{outputKeyFunctionURL: "https://api.lambda-url.aws/"}},
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
	})
}
