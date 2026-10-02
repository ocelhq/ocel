package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func withStamp(t *testing.T, written stamp) *Provider {
	t.Helper()
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/ocel-acme-prod-production/"+StampObject) {
			t.Errorf("preflight called %s %s, want only the tier's bootstrap stamp read", r.Method, r.URL.Path)
		}
		writeBody(w, written)
	}))
	t.Cleanup(served.Close)
	return pushing(t, served.URL)
}

func declaringAStore(memory int64) provider.DeployPreflight {
	return provider.DeployPreflight{
		Deploy:    provider.DeploySpec{Tier: environment.TierProduction},
		Resources: []provider.Resource{{Name: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{MemoryBytes: memory}}},
	}
}

func TestADeployDeclaringAStoreIsAdmittedOnceTheTierHasItsNetwork(t *testing.T) {
	if err := withStamp(t, stamp{State: stateComplete, Features: []string{kvFeature}}).PreflightDeploy(context.Background(), declaringAStore(256<<20)); err != nil {
		t.Errorf("PreflightDeploy() = %v, want a store admitted on a tier whose network is installed", err)
	}
}

func TestADeployDeclaringAStoreIsRefusedUntilTheTierHasItsNetwork(t *testing.T) {
	for _, written := range []stamp{
		{State: stateComplete},
		{State: stateApplying, Features: []string{kvFeature}},
	} {
		err := withStamp(t, written).PreflightDeploy(context.Background(), declaringAStore(256<<20))
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady ||
			!strings.Contains(err.Error(), "ocel bootstrap production --features "+kvFeature) {
			t.Errorf("PreflightDeploy() under stamp %+v = %v, want it refused naming the bootstrap that installs the network", written, err)
		}
	}
}

func TestADeployDeclaringAStoreAbove32GBIsRefusedBeforeAnythingIsRead(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s before refusing a store above 32gb", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)

	err := pushing(t, served.URL).PreflightDeploy(context.Background(), declaringAStore(64<<30))
	if err == nil || !strings.Contains(err.Error(), "32gb") {
		t.Errorf("PreflightDeploy() = %v, want the store refused naming the 32gb limit", err)
	}
}

func TestADeployDeclaringNoStoreReadsNothing(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s for a deploy declaring no store", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)

	if err := pushing(t, served.URL).PreflightDeploy(context.Background(), provider.DeployPreflight{}); err != nil {
		t.Errorf("PreflightDeploy() = %v", err)
	}
}

func declaringATask(infra naming.StackName) provider.DeployPreflight {
	return provider.DeployPreflight{
		Deploy: provider.DeploySpec{Tier: environment.TierProduction, Infra: infra},
		Resources: []provider.Resource{{Name: "task-resize", Declared: "resize", Type: provider.BindingTask, Topic: &provider.TopicSpec{
			Consumers: []provider.ConsumerSpec{{Name: "resize", Worker: "worker", Exclusive: true}},
		}}},
	}
}

func TestADeployDeclaringATaskIsAdmittedOnceTheTierHasTasks(t *testing.T) {
	p := withStamp(t, stamp{State: stateComplete, Features: []string{tasksFeature}})
	if err := p.PreflightDeploy(context.Background(), declaringATask(naming.InfraStack("prod"))); err != nil {
		t.Errorf("PreflightDeploy() = %v, want a task admitted on a tier whose tasks feature is installed", err)
	}
}

func TestADeployDeclaringATaskIsRefusedUntilTheTierHasTasks(t *testing.T) {
	for _, written := range []stamp{{State: stateComplete}, {State: stateApplying, Features: []string{tasksFeature}}} {
		err := withStamp(t, written).PreflightDeploy(context.Background(), declaringATask(naming.InfraStack("prod")))
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady ||
			!strings.Contains(err.Error(), "ocel bootstrap production --features "+tasksFeature) {
			t.Errorf("PreflightDeploy() under stamp %+v = %v, want it refused naming the bootstrap that installs topics and tasks", written, err)
		}
	}
}

func TestAnEphemeralPreviewDeclaringATaskIsRefusedBeforeAnythingIsRead(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s before refusing an ephemeral preview's task", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)

	err := pushing(t, served.URL).PreflightDeploy(context.Background(), declaringATask(naming.StackName{}))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeUnsupported || !strings.Contains(err.Error(), "resize") {
		t.Errorf("PreflightDeploy() of an ephemeral preview declaring a task = %v, want an %s refusal naming it: such a preview provisions no infra stack", err, refusal.CodeUnsupported)
	}
}

func TestWorkersRunOnCloudRunForAtMostThe600sAPushMayTake(t *testing.T) {
	want := []provider.WorkerCeiling{
		{Compute: provider.ComputeServerless, MaxDuration: 600 * time.Second},
		{Compute: provider.ComputeContainer, MaxDuration: 600 * time.Second},
	}
	if got := pushing(t, "").Facts().WorkerCeilings; !slices.Equal(got, want) {
		t.Errorf("Facts().WorkerCeilings = %+v, want %+v: a push subscription waits at most 600s for its endpoint", got, want)
	}
	if bindings := pushing(t, "").Facts().Bindings; !slices.Contains(bindings, provider.BindingTopic) || !slices.Contains(bindings, provider.BindingTask) {
		t.Errorf("Facts().Bindings = %v, want topics and tasks served", bindings)
	}
}
