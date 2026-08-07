package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"golang.org/x/sync/errgroup"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// warmPayload is the invoke event the membrane answers with a warm pass instead
// of a request. Its shape is the authorization: every Function URL here is
// AWS_IAM and the edge composes its own event envelope around a real request,
// so a top-level "ocel" object is unreachable from public traffic. What guards
// it is the IAM grant on lambda:InvokeFunction — there is no signed header, by
// design, since a secret in the payload would have to be deployed with the
// function and would then be one more thing a build could drift on.
const warmPayload = `{"ocel":{"warm":1}}`

// warmConcurrency bounds how many bundles are warmed at once. The rationed
// resource is the account's Lambda concurrency, which is far lower than S3's
// and shared with everything else running in the account — an unbounded
// fan-out against it throttles most of the invocations and leaves most bundles
// unwarmed, which is exactly the outcome this pass exists to prevent. It is
// appConcurrency for the same reason that constant is what it is: the throttle
// budget belongs to a remote service, not to the machine running the deploy.
const warmConcurrency = appConcurrency

// warmPassDeadline caps what warming can add to a deploy. A bundle's own
// invocation cannot outlast defaultFunctionTimeoutSeconds, so this admits
// roughly two dozen worst-case-slow bundles before it bites; past it the deploy
// proceeds with whatever warmed and names the rest. The pass runs before the
// promote, so every second here is a second the previous Deployment keeps
// serving — worth paying for a full cache, but not without a ceiling.
const warmPassDeadline = 3 * time.Minute

// FunctionInvoker is the subset of the AWS Lambda client the warm pass needs:
// one synchronous invoke. The aws-sdk-go-v2 client satisfies it, so nothing
// adapts it at the call site; tests substitute a fake and drive every branch
// with no AWS client, config or credentials in reach.
type FunctionInvoker interface {
	Invoke(ctx context.Context, in *lambda.InvokeInput, optFns ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}

// warmDeployedFunctions warms every bundle this deploy realized that the
// bytecode feature is on for, taking each app-deploy stack's realized function
// names as they came back from it — an app whose stack failed contributes none,
// so a doomed deploy spends nothing here.
//
// It returns nothing and swallows what it cannot do, including a cache
// derivation that fails: warming is an optimization, and the caller is one step
// from the promote.
func warmDeployedFunctions(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, appFunctionNames []map[string]string, log func(string)) {
	if cfg.Invoker == nil {
		return
	}
	if log == nil {
		log = func(string) {}
	}
	caches, err := appCaches(cfg, manifest)
	if err != nil {
		log(fmt.Sprintf("ocel: could not work out which bundles to warm: %v", err))
		return
	}
	names := map[string]string{}
	for _, app := range appFunctionNames {
		for logical, physical := range app {
			names[logical] = physical
		}
	}
	warmPass{
		invoker: cfg.Invoker,
		targets: warmTargets(manifest, caches, names),
		budget:  warmPassDeadline,
		log:     log,
	}.run(ctx)
}

// warmTarget is one bundle to warm: the physical Lambda name to invoke, plus
// the logical name and app the deploy output reports it under.
type warmTarget struct {
	App          string
	LogicalName  string
	FunctionName string
}

// warmTargets are the functions this deploy should warm: exactly those the
// bytecode feature is on for. That is the deploy-wide gate (bytecodeCacheEnabled)
// and an app that keeps an ISR cache, which together are what put
// OCEL_BYTECODE_PREFIX on a function's environment — deriving the set from the
// same two facts rather than restating them is what keeps a function that
// publishes nothing from being invoked for it.
//
// names maps a logical name to the physical Lambda name its stack realized, so
// a function whose app-deploy stack failed is simply absent and is not warmed.
func warmTargets(manifest *deploymentsv1.Manifest, caches map[string]*isrConfig, names map[string]string) []warmTarget {
	if !bytecodeCacheEnabled() {
		return nil
	}
	var targets []warmTarget
	for _, fn := range manifest.GetFunctions() {
		app := fn.GetApp()
		physical := names[fn.GetLogicalName()]
		if caches[app] == nil || physical == "" {
			continue
		}
		targets = append(targets, warmTarget{App: app, LogicalName: fn.GetLogicalName(), FunctionName: physical})
	}
	return targets
}

// warmPass invokes each target once so the membrane loads every entry in its
// bundle and publishes a compile cache covering the whole app. Everything it
// depends on is a field, so the whole pass is exercisable without AWS.
type warmPass struct {
	invoker FunctionInvoker
	targets []warmTarget
	budget  time.Duration
	log     func(string)
}

// run warms every target, or says why it did not. It returns nothing on
// purpose: every leg that can go wrong — a throttle, a timeout, a PUT error, a
// response that does not parse — ends as a warning line, because a deploy that
// fails because a cache did not warm is worse than a slow cold start.
//
// Bundles the deadline cuts off are named rather than dropped: an unwarmed
// bundle is a cold start the first real request pays for, and this output is
// the only place that ever surfaces.
func (p warmPass) run(ctx context.Context) {
	if len(p.targets) == 0 || p.invoker == nil {
		return
	}
	p.log(fmt.Sprintf("ocel: warming %d bundles (%d at a time)", len(p.targets), warmConcurrency))

	ctx, cancel := context.WithTimeout(ctx, p.budget)
	defer cancel()

	start := time.Now()
	var (
		mu      sync.Mutex
		warmed  int
		skipped = make([]bool, len(p.targets))
	)
	var g errgroup.Group
	g.SetLimit(warmConcurrency)
	for i, target := range p.targets {
		g.Go(func() error {
			// Checked before the invoke rather than left to it: a target the
			// deadline never admitted has not failed, and reporting it as a
			// cancelled invoke would hide that the pass simply ran out of time.
			if ctx.Err() != nil {
				mu.Lock()
				skipped[i] = true
				mu.Unlock()
				return nil
			}
			at := time.Now()
			outcome, ok := p.warmOne(ctx, target)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				warmed++
			}
			p.log(fmt.Sprintf("  %s app=%s  %s  %.1fs", target.LogicalName, target.App, outcome, time.Since(at).Seconds()))
			return nil
		})
	}
	_ = g.Wait() // warmOne returns no error, so Wait cannot.

	for i, target := range p.targets {
		if skipped[i] {
			p.log(fmt.Sprintf("  %s app=%s  the warm pass ran out of time; not warmed", target.LogicalName, target.App))
		}
	}
	p.log(fmt.Sprintf("ocel: warmed %d/%d bundles in %.0fs", warmed, len(p.targets), time.Since(start).Seconds()))
}

// warmOne invokes one bundle and reduces the answer to the line the deploy
// reports it under, plus whether the bundle now has a full cache.
func (p warmPass) warmOne(ctx context.Context, target warmTarget) (string, bool) {
	out, err := p.invoker.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   aws.String(target.FunctionName),
		InvocationType: lambdatypes.InvocationTypeRequestResponse,
		Payload:        []byte(warmPayload),
	})
	if err != nil {
		var throttled *lambdatypes.TooManyRequestsException
		if errors.As(err, &throttled) {
			return "throttled; not warmed", false
		}
		return fmt.Sprintf("invoke failed: %v; not warmed", err), false
	}
	if fnErr := aws.ToString(out.FunctionError); fnErr != "" {
		return fmt.Sprintf("the function errored (%s); not warmed", fnErr), false
	}
	if out.StatusCode < 200 || out.StatusCode >= 300 {
		return fmt.Sprintf("lambda answered %d; not warmed", out.StatusCode), false
	}

	var reply warmReply
	if err := json.Unmarshal(out.Payload, &reply); err != nil {
		return fmt.Sprintf("unreadable warm response: %v; not warmed", err), false
	}
	return reply.report()
}

// warmReply is the membrane's raw JSON answer to a warm invoke. Fields that do
// not apply to a state are omitted from the response, so every one of these
// reads as its zero value when it was not sent.
type warmReply struct {
	State     string            `json:"state"`
	Entries   int               `json:"entries"`
	Loaded    int               `json:"loaded"`
	Failures  []json.RawMessage `json:"failures"`
	StoppedBy string            `json:"stoppedBy"`
	Bytes     int64             `json:"bytes"`
	Key       string            `json:"key"`
	Uploaded  bool              `json:"uploaded"`
	Error     string            `json:"error"`
}

// report renders one bundle's outcome and whether it counts as warmed.
//
// already-cached counts. The deploy cannot pre-check whether a cache exists —
// the key embeds node's full version and only the sandbox learns that — so this
// is the membrane's answer for a build whose cache was already complete, not a
// failure to publish one.
func (r warmReply) report() (string, bool) {
	switch r.State {
	case "published":
		return fmt.Sprintf("%d/%d entries, %.1f MiB published", r.Loaded, r.Entries, float64(r.Bytes)/(1<<20)), true
	case "already-cached":
		return fmt.Sprintf("%d/%d entries, already cached", r.Loaded, r.Entries), true
	case "disabled":
		return "bytecode caching is off in this function; not warmed", false
	case "failed":
		return fmt.Sprintf("%d/%d entries, warm failed: %s; not warmed", r.Loaded, r.Entries, r.reason()), false
	default:
		return fmt.Sprintf("unrecognized warm state %q; not warmed", r.State), false
	}
}

// reason is the best account a failed reply carries of why. The membrane sets
// whichever of the three it knows, so this falls through them rather than
// picking one and reporting an empty string when that one is absent.
func (r warmReply) reason() string {
	switch {
	case r.Error != "":
		return r.Error
	case r.StoppedBy != "":
		return r.StoppedBy
	case len(r.Failures) > 0:
		return fmt.Sprintf("%d entries failed to load", len(r.Failures))
	default:
		return "no reason given"
	}
}
