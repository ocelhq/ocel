package deploy

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/blang/semver"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/naming"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type teardownCalls struct {
	mu   sync.Mutex
	seen []string
}

func (c *teardownCalls) record(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, call)
}

func (c *teardownCalls) ordered() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.seen)
}

type fakePulumi struct {
	record func(string)
}

var _ auto.PulumiCommand = (*fakePulumi)(nil)

func (f *fakePulumi) Version() semver.Version { return semver.MustParse("3.146.0") }

func (f *fakePulumi) Run(_ context.Context, _ string, _ io.Reader, _ []io.Writer, _ []io.Writer, _ []string, args ...string) (string, string, int, error) {
	if args[0] == "destroy" {
		if i := slices.Index(args, "--stack"); i >= 0 && i+1 < len(args) {
			f.record("destroy-stack " + args[i+1])
		}
	}
	if len(args) >= 2 && args[0] == "stack" && args[1] == "history" {
		return "[]", "", 0, nil
	}
	return "", "", 0, nil
}

func spanOrder(t *testing.T, spans []spanCall, stage Stage) int {
	t.Helper()
	for i, span := range spans {
		if span.id == stage.ID {
			return i
		}
	}
	t.Fatalf("stage %q never span; got %d spans", stage.Title, len(spans))
	return -1
}

func servingStack(t *testing.T, fake *recordingEdge, hostname string) edge.EdgeStack {
	t.Helper()
	stack := fake.reconciled(t, edge.StackSpec{
		Version: "v1",
		Class:   edge.ClassProduction,
		Slug:    "shop",
		Program: &edge.ProgramSpec{Name: "root", StoreEndpoint: fakeStoreEndpoint},
	})
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	return stack
}

func servedProject(t *testing.T, calls *teardownCalls, stacks ...naming.StackName) Config {
	t.Helper()
	return Config{
		Stacks:        &fakeStackIndex{projects: []string{"shop"}, stacks: map[string][]naming.StackName{"shop": stacks}},
		Pulumi:        &fakePulumi{record: calls.record},
		PulumiProject: "ocel",
		Passphrase:    "teardown",
		BackendURL:    "file://" + t.TempDir(),
	}
}

func TestDestroyProjectStopsRoutingBeforeItDeletesTheOrigin(t *testing.T) {
	t.Parallel()

	web := naming.AppStack(ProductionEnv, "web", testRelease(t, "b1"))
	infra := naming.InfraStack(ProductionEnv)

	calls := &teardownCalls{}
	fake := &recordingEdge{kind: cloudflare.Kind, record: calls.record}
	stack := servingStack(t, fake, "shop.example.com")
	tracer := &fakeTracer{}
	stages := newProjectTeardownStages()
	cfg := servedProject(t, calls, web, infra)
	cfg.Tracer = tracer

	result, err := DestroyProject(context.Background(), stack, cfg, "shop", stages, nil)
	if err != nil {
		t.Fatalf("DestroyProject: %v", err)
	}
	if !result.EdgeTornDown {
		t.Fatal("EdgeTornDown = false, want the caller told it may forget the stack state")
	}

	want := []string{
		"unbind shop.example.com",
		"remove-pointer " + edge.DefaultPointer,
		"destroy-stack " + web.String(),
		"destroy-stack " + infra.String(),
		"destroy",
	}
	if got := calls.ordered(); !slices.Equal(got, want) {
		t.Fatalf("teardown calls = %v, want %v — routing must stop before what it points at is deleted", got, want)
	}

	unbind := spanOrder(t, tracer.spans, stages.Unbind)
	apps := spanOrder(t, tracer.spans, stages.AppStacks)
	infraSpan := spanOrder(t, tracer.spans, stages.InfraStacks)
	edgeStage := spanOrder(t, tracer.spans, stages.Edge)
	if unbind > apps || unbind > infraSpan {
		t.Errorf("unbind span at %d, stacks at %d/%d — nothing may be destroyed while the edge still routes to it", unbind, apps, infraSpan)
	}
	if edgeStage < apps || edgeStage < infraSpan {
		t.Errorf("edge span at %d, stacks at %d/%d — the edge stack outlives the origin it fronts", edgeStage, apps, infraSpan)
	}
}

func TestDestroyProjectResumesAfterAFailedEdgeDestroy(t *testing.T) {
	t.Parallel()

	web := naming.AppStack(ProductionEnv, "web", testRelease(t, "b1"))
	calls := &teardownCalls{}
	fake := &recordingEdge{kind: cloudflare.Kind, destroyErr: errors.New("the front is on fire"), record: calls.record}
	stack := servingStack(t, fake, "shop.example.com")
	cfg := servedProject(t, calls, web)

	result, err := DestroyProject(context.Background(), stack, cfg, "shop", ProjectTeardownStages{}, nil)
	if err == nil {
		t.Fatal("DestroyProject err = nil, want the failed edge destroy reported")
	}
	if result.EdgeTornDown {
		t.Fatal("EdgeTornDown = true after a failed destroy: the stack state is what the rerun reads its progress from")
	}
	if got := calls.ordered(); !slices.Contains(got, "destroy-stack "+web.String()) {
		t.Fatalf("teardown calls = %v, want the app stack destroyed before the edge was tried", got)
	}

	fake.destroyErr = nil
	result, err = DestroyProject(context.Background(), stack, cfg, "shop", ProjectTeardownStages{}, nil)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !result.EdgeTornDown {
		t.Fatal("EdgeTornDown = false on the rerun, want the interrupted destroy finished")
	}
	if fake.destroyed != 2 {
		t.Fatalf("destroy calls = %d, want the rerun to have tried again", fake.destroyed)
	}
	if got := strings.Count(strings.Join(calls.ordered(), "\n"), "destroy-stack "+web.String()); got != 1 {
		t.Fatalf("app stack destroyed %d times, want the rerun to skip what the index no longer carries", got)
	}
}

func TestUnbindRoutingDropsEveryHostnameAndPointer(t *testing.T) {
	t.Parallel()

	fake := &recordingEdge{kind: cloudflare.Kind}
	stack := servingStack(t, fake, "shop.example.com")

	if err := unbindRouting(context.Background(), stack, Config{}, Stage{}, []string{"pr-1", "pr-2"}); err != nil {
		t.Fatalf("unbindRouting: %v", err)
	}

	want := []string{"unbind shop.example.com", "remove-pointer pr-1", "remove-pointer pr-2"}
	if !slices.Equal(fake.calls, want) {
		t.Fatalf("edge calls = %v, want %v — every preview pointer stops routing before its stacks go", fake.calls, want)
	}
}
