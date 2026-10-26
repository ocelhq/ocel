package providerserver_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func assertSpansClose(t *testing.T, events []*progressv1.OperationEvent) {
	t.Helper()

	titles := map[string]string{}
	var order []string
	ended := map[string]int{}
	for _, event := range events {
		key := string(event.GetSpanId())
		if event.GetStarted() != nil {
			if _, seen := titles[key]; seen {
				t.Errorf("span %q is started twice", event.GetMessage())
				continue
			}
			titles[key] = event.GetMessage()
			order = append(order, key)
		}
		if event.GetEnded() != nil {
			if _, started := titles[key]; !started {
				t.Errorf("an ended event names span %x, which no started event opened", event.GetSpanId())
			}
			ended[key]++
		}
	}
	if len(order) == 0 {
		t.Fatal("the run started no span at all")
	}

	for _, key := range order {
		if ended[key] != 1 {
			t.Errorf("span %q is ended %d times, want every span a run opens ended exactly once", titles[key], ended[key])
		}
	}
}

func saidLine(event *progressv1.OperationEvent) string {
	if event.GetBody() != nil || event.GetLevel() != progressv1.Level_LEVEL_INFO {
		return ""
	}
	return event.GetMessage()
}

type startedSpan struct {
	id, parent, title string
	phase             progressv1.Phase
}

func startedSpans(events []*progressv1.OperationEvent) []startedSpan {
	var out []startedSpan
	for _, event := range events {
		if started := event.GetStarted(); started != nil {
			out = append(out, startedSpan{
				id:     string(event.GetSpanId()),
				parent: string(started.GetParentSpanId()),
				title:  event.GetMessage(),
				phase:  event.GetPhase(),
			})
		}
	}
	return out
}

func recorded(stream *connect.ServerStreamForClient[progressv1.OperationEvent]) []*progressv1.OperationEvent {
	defer stream.Close()
	var events []*progressv1.OperationEvent
	for stream.Receive() {
		events = append(events, stream.Msg())
	}
	return events
}

func TestEverySpanADeployOpensIsEndedExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		preview  bool
		request  func(t *testing.T, vendor *fake.Provider) *contractv1.DeployRequest
		succeeds bool
	}{
		{name: "a production deploy", request: func(*testing.T, *fake.Provider) *contractv1.DeployRequest { return deployRequest() }, succeeds: true},
		{name: "a dry run", request: func(*testing.T, *fake.Provider) *contractv1.DeployRequest {
			req := deployRequest()
			req.Dry = true
			return req
		}, succeeds: true},
		{name: "a preview deploy", preview: true, request: func(*testing.T, *fake.Provider) *contractv1.DeployRequest { return previewRequest() }, succeeds: true},
		{name: "a deploy that fails attaching its hostnames", request: func(t *testing.T, vendor *fake.Provider) *contractv1.DeployRequest {
			writer, err := vendor.DNS().Open(fake.KindZone, "shop.example", "")
			if err != nil {
				t.Fatal(err)
			}
			writer.(*fake.DNSRecords).Refuse(errors.New("the zone's api answered 500"))
			req := deployRequest()
			req.Edge = writtenBy("shop.example")
			return req
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			builtProject(t)
			client, vendor := deployServed(t)
			if tc.preview {
				previewBootstrapped(t, client)
			}

			result, events := deploy(t, client, tc.request(t, vendor))
			if result.GetSuccess() != tc.succeeds {
				t.Fatalf("Deploy() success = %v (%q), want %v", result.GetSuccess(), result.GetError(), tc.succeeds)
			}
			assertSpansClose(t, events)
		})
	}
}

func TestASpanStartsWhenItRunsNotWhenTheDeployBegins(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	roots := map[string]string{}
	var sequence []string
	for _, event := range events {
		if started := event.GetStarted(); started != nil && len(started.GetParentSpanId()) == 0 {
			roots[string(event.GetSpanId())] = event.GetMessage()
			sequence = append(sequence, "started "+event.GetMessage())
		}
		if root, ok := roots[string(event.GetSpanId())]; ok && event.GetEnded() != nil {
			sequence = append(sequence, "ended "+root)
		}
	}
	hostnamesEnded := slices.IndexFunc(sequence, func(step string) bool { return strings.HasPrefix(step, "ended Attaching") })
	promotionStarted := slices.IndexFunc(sequence, func(step string) bool { return strings.HasPrefix(step, "started Switching traffic") })
	if hostnamesEnded < 0 || promotionStarted < hostnamesEnded {
		t.Errorf("spans ran as %v, want Promotion started only after Hostnames ended: a started event says the span is running", sequence)
	}
}

func TestEverySpanADeployOpensStartsAndEndsInTheSamePhase(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	started := map[string]*progressv1.OperationEvent{}
	for _, event := range events {
		if event.GetStarted() != nil {
			started[string(event.GetSpanId())] = event
		}
		opened, ok := started[string(event.GetSpanId())]
		if event.GetEnded() == nil || !ok {
			continue
		}
		if opened.GetPhase() != event.GetPhase() {
			t.Errorf("span %q starts in %v and ends in %v, want one phase for the whole span", opened.GetMessage(), opened.GetPhase(), event.GetPhase())
		}
	}
}

func spanPhases(events []*progressv1.OperationEvent, title string) []progressv1.Phase {
	var phases []progressv1.Phase
	for _, span := range startedSpans(events) {
		if span.parent == "" && strings.HasPrefix(span.title, title) {
			phases = append(phases, span.phase)
		}
	}
	return phases
}

func TestADeploysPromotionRunsInThePromotePhase(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	got := spanPhases(events, "Switching traffic")
	if len(got) != 1 || got[0] != progressv1.Phase_PHASE_PROMOTE {
		t.Errorf("the Promotion span runs in %v, want the promote phase", got)
	}
}

func TestAnAppSpansEventsNameTheAppAsSubjectInTheDeployPhase(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if got := spanPhases(events, "Deploying the serverless app"); len(got) != 1 || got[0] != progressv1.Phase_PHASE_DEPLOY {
		t.Errorf("the web span runs in %v, want the deploy phase", got)
	}

	app := map[string]bool{}
	for _, span := range startedSpans(events) {
		if strings.HasPrefix(span.title, "Deploying the serverless app") || app[span.parent] {
			app[span.id] = true
		}
	}
	var scoped int
	for _, event := range events {
		if !app[string(event.GetSpanId())] {
			continue
		}
		scoped++
		if event.GetSubject() != "web" || event.GetPhase() != progressv1.Phase_PHASE_DEPLOY {
			t.Errorf("an event of the web span is scoped %q in %v, want \"web\" in the deploy phase", event.GetSubject(), event.GetPhase())
		}
	}
	if scoped == 0 {
		t.Fatal("no event names the web span's span, want its progress and spans scoped to it")
	}
}

func TestTheEdgeSpansEventsNameTheEdgeKindAsSubject(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	root := map[string]bool{}
	for _, span := range startedSpans(events) {
		if strings.HasPrefix(span.title, "Reconciling the routes") || root[span.parent] {
			root[span.id] = true
		}
	}
	var scoped int
	for _, event := range events {
		if !root[string(event.GetSpanId())] {
			continue
		}
		scoped++
		if event.GetSubject() != string(fake.KindRelay) {
			t.Errorf("an event of the Edge span names %q, want the edge it deploys, %q", event.GetSubject(), fake.KindRelay)
		}
	}
	if scoped == 0 {
		t.Fatal("no event names the Edge span's span, want its progress and spans scoped to it")
	}
}

func TestARemovalRunsInTheDestroyPhase(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	deployed(t, vendor, environment.TierPreview, "shop")

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	got := spanPhases(recorded(stream), "Removing the preview environment")
	if len(got) != 1 || got[0] != progressv1.Phase_PHASE_DESTROY {
		t.Errorf("the removal runs in %v, want the destroy phase", got)
	}
}

func TestBootstrapEndsEverySpanItStarts(t *testing.T) {
	t.Run("when the work succeeds", func(t *testing.T) {
		t.Parallel()
		client, _ := contractServed(t, "1.0.0")

		stream, err := client.Bootstrap(context.Background(), &contractv1.BootstrapRequest{
			Tier: environmentv1.Tier_TIER_PRODUCTION,
		})
		if err != nil {
			t.Fatalf("Bootstrap() error = %v", err)
		}
		assertSpansClose(t, recorded(stream))
	})

	t.Run("when the work fails", func(t *testing.T) {
		t.Parallel()
		client, provider := contractServed(t, "1.0.0")
		provider.FakeBootstrap().RefuseApply(errors.New("the bootstrap fell over"))

		stream, err := client.Bootstrap(context.Background(), &contractv1.BootstrapRequest{
			Tier: environmentv1.Tier_TIER_PRODUCTION,
		})
		if err != nil {
			t.Fatalf("Bootstrap() error = %v", err)
		}
		events := recorded(stream)
		if len(events) == 0 {
			t.Fatal("a failed Bootstrap() streamed nothing at all")
		}
		assertSpansClose(t, events)

		titles := map[string]string{}
		for _, span := range startedSpans(events) {
			titles[span.id] = span.title
		}
		for _, event := range events {
			ended := event.GetEnded()
			if ended == nil {
				continue
			}
			if ended.GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR {
				t.Errorf("the span %q ends %v, want ERROR: the work under it failed", titles[string(event.GetSpanId())], ended.GetStatus())
			}
		}
	})
}

type timedCall struct {
	name            string
	began, returned time.Time
}

type timedCalls struct {
	mu    sync.Mutex
	calls []timedCall
}

func (c *timedCalls) time(name string, began time.Time) {
	returned := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, timedCall{name: name, began: began, returned: returned})
}

func (c *timedCalls) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = nil
}

func (c *timedCalls) list() []timedCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

type timedStore struct {
	records.Store
	calls *timedCalls
}

func (s timedStore) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	defer s.calls.time("read "+key.String(), time.Now())
	return s.Store.Read(ctx, key)
}

func (s timedStore) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	defer s.calls.time("write "+entry.Key.String(), time.Now())
	return s.Store.Write(ctx, entry)
}

func (s timedStore) WritePair(ctx context.Context, first, second records.Entry) error {
	defer s.calls.time("write "+first.Key.String()+" and "+second.Key.String(), time.Now())
	return s.Store.WritePair(ctx, first, second)
}

func (s timedStore) Remove(ctx context.Context, key records.Key, expected records.Revision) error {
	defer s.calls.time("remove "+key.String(), time.Now())
	return s.Store.Remove(ctx, key, expected)
}

func (s timedStore) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	defer s.calls.time("list "+in.String(), time.Now())
	return s.Store.List(ctx, in, under...)
}

type timedProvider struct {
	*fake.Provider
	store timedStore
}

func (p timedProvider) KeyValues() records.Store { return p.store }

func timedServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider, *timedCalls) {
	t.Helper()
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	calls := &timedCalls{}
	client := servedProvider(t, "1.0.0", timedProvider{Provider: vendor, store: timedStore{Store: vendor.KeyValues(), calls: calls}})
	return client, vendor, calls
}

func callsNoSpanNamed(events []*progressv1.OperationEvent, calls []timedCall) []string {
	type window struct{ start, end time.Time }
	started := map[string]time.Time{}
	var windows []window
	for _, event := range events {
		id := string(event.GetSpanId())
		if opened := event.GetStarted(); opened != nil && len(opened.GetParentSpanId()) == 0 {
			started[id] = event.GetTime().AsTime()
		}
		if start, open := started[id]; open && event.GetEnded() != nil {
			windows = append(windows, window{start: start, end: event.GetTime().AsTime()})
		}
	}
	var unnamed []string
	for _, call := range calls {
		if !slices.ContainsFunc(windows, func(w window) bool { return !call.began.Before(w.start) && !call.returned.After(w.end) }) {
			unnamed = append(unnamed, call.name)
		}
	}
	return unnamed
}

func timeRetirementPlans(vendor *fake.Provider, calls *timedCalls) {
	vendor.WithHooks(func(hooks *provider.Hooks) {
		retirement := *hooks.Retirement
		plan := retirement.Plan
		retirement.Plan = func(ctx context.Context, live provider.LiveApps, log progress.Log) (provider.Plan, error) {
			defer calls.time("plan which apps retire", time.Now())
			return plan(ctx, live, log)
		}
		hooks.Retirement = &retirement
	})
}

func refusingForAWantedPart(vendor *fake.Provider) {
	vendor.WithHooks(func(hooks *provider.Hooks) {
		hooks.Preflight = &provider.PreflightHooks{
			Plan: func(context.Context, provider.DeployPreflight) (provider.Plan, error) { return provider.Plan{}, nil },
			Apply: func(context.Context, provider.DeployPreflight) error {
				return provider.WantedPartError{Part: fake.PartCache, Err: refusal.Refuse(refusal.CodeNotReady, "the bootstrap has no cache")}
			},
		}
	})
}

func TestEveryRecordADeployReadsOrWritesIsReadOrWrittenWhileASpanNamesIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		preview bool
		request func(t *testing.T, vendor *fake.Provider) *contractv1.DeployRequest
	}{
		{name: "a production deploy", request: func(*testing.T, *fake.Provider) *contractv1.DeployRequest { return deployRequest() }},
		{name: "a dry run", request: func(*testing.T, *fake.Provider) *contractv1.DeployRequest {
			req := deployRequest()
			req.Dry = true
			return req
		}},
		{name: "a preview deploy", preview: true, request: func(*testing.T, *fake.Provider) *contractv1.DeployRequest { return previewRequest() }},
		{name: "a deploy that fails attaching its hostnames", request: func(t *testing.T, vendor *fake.Provider) *contractv1.DeployRequest {
			writer, err := vendor.DNS().Open(fake.KindZone, "shop.example", "")
			if err != nil {
				t.Fatal(err)
			}
			writer.(*fake.DNSRecords).Refuse(errors.New("the zone's api answered 500"))
			req := deployRequest()
			req.Edge = writtenBy("shop.example")
			return req
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			builtProject(t)
			client, vendor, calls := timedServed(t)
			bootstrappedOverRPC(t, client)
			if tc.preview {
				previewBootstrapped(t, client)
			}
			timeRetirementPlans(vendor, calls)
			req := tc.request(t, vendor)
			calls.reset()

			_, events := deploy(t, client, req)

			if unnamed := callsNoSpanNamed(events, calls.list()); len(unnamed) > 0 {
				t.Errorf("the deploy made these calls while no span named what it waited on: %v", unnamed)
			}
		})
	}
}
