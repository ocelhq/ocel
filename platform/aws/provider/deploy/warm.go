package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"golang.org/x/sync/errgroup"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

const warmPayload = `{"ocel":{"warm":1}}`

const warmConcurrency = appConcurrency

const warmPassDeadline = 3 * time.Minute

type FunctionInvoker interface {
	Invoke(ctx context.Context, in *lambda.InvokeInput, optFns ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}

func warmDeployedFunctions(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, appFunctionNames []map[string]string, log func(string)) []warmResult {
	if cfg.Invoker == nil {
		return nil
	}
	if log == nil {
		log = func(string) {}
	}
	caches, err := appCaches(cfg, manifest)
	if err != nil {
		log(fmt.Sprintf("ocel: could not work out which bundles to warm: %v", err))
		return nil
	}
	names := map[string]string{}
	for _, app := range appFunctionNames {
		for logical, physical := range app {
			names[logical] = physical
		}
	}
	return warmPass{
		invoker: cfg.Invoker,
		targets: warmTargets(manifest, caches, names),
		budget:  warmPassDeadline,
		log:     log,
	}.run(ctx)
}

type warmTarget struct {
	App          string
	LogicalName  string
	FunctionName string
}

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

type warmPass struct {
	invoker FunctionInvoker
	targets []warmTarget
	budget  time.Duration
	log     func(string)
}

func (p warmPass) run(ctx context.Context) []warmResult {
	if len(p.targets) == 0 || p.invoker == nil {
		return nil
	}
	p.log(fmt.Sprintf("ocel: warming %s (%d at a time)", plural(len(p.targets), "bundle", "bundles"), warmConcurrency))

	ctx, cancel := context.WithTimeout(ctx, p.budget)
	defer cancel()

	start := time.Now()
	var (
		mu      sync.Mutex
		warmed  int
		skipped = make([]bool, len(p.targets))
		replies = make([]warmReply, len(p.targets))
	)
	var g errgroup.Group
	g.SetLimit(warmConcurrency)
	for i, target := range p.targets {
		g.Go(func() error {
			if ctx.Err() != nil {
				skipped[i] = true
				return nil
			}
			at := time.Now()
			outcome, reply, ok := p.warmOne(ctx, target)
			replies[i] = reply
			mu.Lock()
			defer mu.Unlock()
			if ok {
				warmed++
			}
			p.log(fmt.Sprintf("  %s app=%s  %s  %.1fs", target.LogicalName, target.App, outcome, time.Since(at).Seconds()))
			return nil
		})
	}
	_ = g.Wait()

	var results []warmResult
	for i, target := range p.targets {
		if skipped[i] {
			p.log(fmt.Sprintf("  %s app=%s  the warm pass ran out of time; not warmed", target.LogicalName, target.App))
			continue
		}
		results = append(results, warmResult{Target: target, Reply: replies[i]})
	}
	p.log(fmt.Sprintf("ocel: warmed %d/%d bundles in %.0fs", warmed, len(p.targets), time.Since(start).Seconds()))
	return results
}

type warmResult struct {
	Target warmTarget
	Reply  warmReply
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (p warmPass) warmOne(ctx context.Context, target warmTarget) (string, warmReply, bool) {
	reply, failure := invokeWarm(ctx, p.invoker, target.FunctionName)
	if failure != "" {
		return failure, warmReply{}, false
	}
	outcome, ok := reply.report()
	return outcome, reply, ok
}

func invokeWarm(ctx context.Context, invoker FunctionInvoker, functionName string) (warmReply, string) {
	out, err := invoker.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   aws.String(functionName),
		InvocationType: lambdatypes.InvocationTypeRequestResponse,
		Payload:        []byte(warmPayload),
	})
	if err != nil {
		var throttled *lambdatypes.TooManyRequestsException
		if errors.As(err, &throttled) {
			return warmReply{}, "throttled; not warmed"
		}
		return warmReply{}, fmt.Sprintf("invoke failed: %v; not warmed", err)
	}
	if fnErr := aws.ToString(out.FunctionError); fnErr != "" {
		return warmReply{}, fmt.Sprintf("the function errored (%s); not warmed", fnErr)
	}
	if out.StatusCode < 200 || out.StatusCode >= 300 {
		return warmReply{}, fmt.Sprintf("lambda answered %d; not warmed", out.StatusCode)
	}
	reply, err := parseWarmReply(out.Payload)
	if err != nil {
		return warmReply{}, fmt.Sprintf("unreadable warm response: %v; not warmed", err)
	}
	return reply, ""
}

const (
	warmStatePublished     = "published"
	warmStateAlreadyCached = "already-cached"
	warmStateDisabled      = "disabled"
	warmStateFailed        = "failed"
)

type warmSource string

const warmSourceEmbedded warmSource = "embedded"

func parseWarmReply(payload []byte) (warmReply, error) {
	var reply warmReply
	if err := json.Unmarshal(payload, &reply); err == nil {
		return reply, nil
	}
	if at := bytes.LastIndexByte(payload, 0); at >= 0 {
		var framed warmReply
		if err := json.Unmarshal(bytes.TrimLeft(payload[at:], "\x00"), &framed); err == nil {
			return framed, nil
		}
	}
	return warmReply{}, fmt.Errorf("no warm summary in %q", tailOf(payload, 200))
}

func tailOf(payload []byte, n int) []byte {
	if len(payload) <= n {
		return payload
	}
	return payload[len(payload)-n:]
}

type warmReply struct {
	State        string            `json:"state"`
	Entries      int               `json:"entries"`
	Loaded       int               `json:"loaded"`
	Failures     []json.RawMessage `json:"failures"`
	StoppedBy    string            `json:"stoppedBy"`
	Skipped      []string          `json:"skipped"`
	SkippedCount int               `json:"skippedCount"`
	Uncounted    string            `json:"uncounted"`
	Bytes        int64             `json:"bytes"`
	Key          string            `json:"key"`
	Source       warmSource        `json:"source"`
	Uploaded     bool              `json:"uploaded"`
	Error        string            `json:"error"`
}

func (r warmReply) report() (string, bool) {
	switch r.State {
	case warmStatePublished:
		if !r.Uploaded {
			return fmt.Sprintf("%s, reported published without uploading; not warmed", r.walk()), false
		}
		published := fmt.Sprintf("%s, %.1f MiB published", r.walk(), float64(r.Bytes)/(1<<20))
		if r.Key != "" {
			published += " to " + r.Key
		}
		return published, true
	case warmStateAlreadyCached:
		return fmt.Sprintf("%s, already cached", r.walk()), true
	case warmStateDisabled:
		return fmt.Sprintf("nothing to warm: %s; not warmed", r.reason()), false
	case warmStateFailed:
		return fmt.Sprintf("%s, warm failed: %s; not warmed", r.walk(), r.reason()), false
	default:
		return fmt.Sprintf("unrecognized warm state %q; not warmed", r.State), false
	}
}

func (r warmReply) walk() string {
	if r.Uncounted != "" {
		return fmt.Sprintf("entry counts unknown (%s)", r.Uncounted)
	}
	walk := fmt.Sprintf("%d/%d entries", r.Loaded, r.Entries)
	if r.SkippedCount == 0 {
		return walk
	}
	walk += fmt.Sprintf(", %s skipped", plural(r.SkippedCount, "entry", "entries"))
	if r.StoppedBy != "" {
		walk += " by " + r.StoppedBy
	}
	if len(r.Skipped) == 0 {
		return walk
	}
	walk += ": " + strings.Join(r.Skipped, ", ")
	if unlisted := r.SkippedCount - len(r.Skipped); unlisted > 0 {
		walk += fmt.Sprintf(" (+%d not listed)", unlisted)
	}
	return walk
}

func (r warmReply) reason() string {
	switch {
	case r.Error != "":
		return r.Error
	case r.Uncounted != "":
		return r.Uncounted
	case r.StoppedBy != "":
		return r.StoppedBy
	case len(r.Failures) > 0:
		return fmt.Sprintf("%s failed to load", plural(len(r.Failures), "entry", "entries"))
	default:
		return "no reason given"
	}
}
