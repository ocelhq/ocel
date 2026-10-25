package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
)

func TestAnInfraRunsPreflightAddsTheNetworkADatabaseNeedsAndKeepsThePartsTheProjectRecords(t *testing.T) {
	p, server := deployingNetworks(t)
	ctx := context.Background()
	tier := environment.TierProduction
	recordedCurrent(t, p, tier, bootstrap.Keys)
	if _, err := p.KeyValues().Write(ctx, records.Entry{Key: stackrecords.ProjectKey(tier, "shop"), Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.RecordPartsUsed(ctx, p.KeyValues(), tier, "shop", []string{bootstrap.EdgeBucketPart, bootstrap.CacheBucketPart}); err != nil {
		t.Fatal(err)
	}
	log := &fake.Log{}
	pre := declaringADatabase("17")
	pre.Deploy.Slug = "shop"
	pre.Edge = cloudflare.Kind
	pre.Progress = log

	if err := p.ApplyInfraPreflight(ctx, pre); err != nil {
		t.Fatalf("ApplyInfraPreflight() = %v", err)
	}

	want := []string{"create network ocel-prod-network", "create subnetwork ocel-prod-network", "grant on ocel-prod-network", "create policy ocel-prod-network-memorystore", "create policy ocel-prod-network-cloudsql"}
	if got := server.wrote(); !slices.Equal(got, want) {
		t.Errorf("the infra run's preflight wrote %v, want only the network its database needs: %v", got, want)
	}
	users, err := stackrecords.ListPartUsers(ctx, p.KeyValues(), tier)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{bootstrap.EdgeBucketPart, bootstrap.CacheBucketPart, bootstrap.KeysPart, network.PartName} {
		if !slices.Equal(users[part], []string{"shop"}) {
			t.Errorf("users of %s after the infra run's preflight = %v, want shop: it adds what its resources need to what the project records, and knows no app to drop a part for", part, users[part])
		}
	}
	if lines := log.Lines(); slices.ContainsFunc(lines, func(line string) bool { return strings.HasPrefix(line, "WARN ") }) {
		t.Errorf("the infra run's preflight said %q, want no warning: the deploy's own preflight warns once", lines)
	}
}

func TestAnInfraRunsPreflightPlansThePartsItsResourcesNeedAndNoneTheEdgeServesFrom(t *testing.T) {
	p, server := deployingNetworks(t)
	pre := declaringADatabase("17")
	pre.Deploy.Slug = "shop"
	pre.Edge = alb.Kind

	plan, err := p.PlanInfraPreflight(context.Background(), pre)
	if err != nil {
		t.Fatalf("PlanInfraPreflight() = %v", err)
	}

	if len(server.wrote()) != 0 {
		t.Errorf("the infra run's plan wrote %v, want nothing", server.wrote())
	}
	planned := plan.Groups
	if len(planned) != 2 || planned[0].Part != bootstrap.KeysPart || planned[1].Part != network.PartName {
		t.Errorf("the infra run's plan = %+v, want the keys and the %s part its database needs, and nothing the alb serves from", planned, network.PartName)
	}
}

func TestAnInfraRunsPreflightDeclaringNothingCallsNothingAndRecordsNothing(t *testing.T) {
	p := failingOnAnyCall(t)
	ctx := context.Background()
	pre := provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierProduction, Slug: "shop"}, Edge: alb.Kind}

	if plan, err := p.PlanInfraPreflight(ctx, pre); err != nil || len(plan.Groups) != 0 {
		t.Errorf("PlanInfraPreflight() of no resource = %+v, %v, want nothing planned", plan.Groups, err)
	}
	if err := p.ApplyInfraPreflight(ctx, pre); err != nil {
		t.Fatalf("ApplyInfraPreflight() of no resource = %v", err)
	}
	if entry, err := p.KeyValues().Read(ctx, stackrecords.PartsUsedKey(environment.TierProduction, "shop")); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("the infra run's preflight of no resource recorded parts %s (%v), want no record", entry.Value, err)
	}
}

func TestAnInfraRunsPreflightThatCannotAddAPartRecordsNoPartUsed(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"denied"}}`))
	}))
	t.Cleanup(served.Close)
	p := pushing(t, served.URL)
	ctx := context.Background()
	pre := declaringADatabase("17")
	pre.Deploy.Slug = "shop"
	pre.Edge = alb.Kind

	if err := p.ApplyInfraPreflight(ctx, pre); err == nil {
		t.Fatal("ApplyInfraPreflight() on a project that refuses every call = nil, want the error adding its parts")
	}

	if entry, err := p.KeyValues().Read(ctx, stackrecords.PartsUsedKey(environment.TierProduction, "shop")); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("the infra run's preflight recorded parts %s (%v) it never made, want no record: a destroy would remove parts that do not exist", entry.Value, err)
	}
}

func TestAnInfraRunOnThisProviderRunsItsOwnPreflight(t *testing.T) {
	t.Parallel()

	if pushing(t, "").Hooks().InfraPreflight == nil {
		t.Error("the gcp provider's hooks leave InfraPreflight nil, so an infra run provisions a database before the network it needs is added")
	}
}

var infraPartAPIs = []string{"cloudkms.googleapis.com", "compute.googleapis.com", "memorystore.googleapis.com", "sqladmin.googleapis.com",
	"networkconnectivity.googleapis.com", "serviceconsumermanagement.googleapis.com", "servicenetworking.googleapis.com"}

func TestAnInfraRunsPlanShowsTheServicesOfThePartsItAddsSwitchedOnInOneReadAndSwitchesNothingOn(t *testing.T) {
	t.Parallel()
	f := &freshProject{usage: &googletest.ServiceUsage{ServicesOff: slices.Clone(infraPartAPIs)}}
	p := pushing(t, f.serve(t))
	pre := declaringADatabase("17")
	pre.Deploy.Slug = "shop"

	plan, err := p.PlanInfraPreflight(t.Context(), pre)
	if err != nil {
		t.Fatalf("PlanInfraPreflight() = %v", err)
	}

	if got := switchedOnInPlan(plan); !slices.Equal(got, infraPartAPIs) {
		t.Errorf("the infra run's plan switches on %v, want %v: the keys and private-network parts its database needs", got, infraPartAPIs)
	}
	if f.usage.BatchGets != 1 || f.usage.Gets != 0 {
		t.Errorf("read the services in %d batches and %d single reads, want one batch", f.usage.BatchGets, f.usage.Gets)
	}
	if len(f.usage.Enabled) > 0 {
		t.Errorf("PlanInfraPreflight() switched on %v, want nothing", f.usage.Enabled)
	}
}

func TestAnInfraRunSwitchesOnTheServicesOfThePartsItAddsBeforeItReadsAnyOfThem(t *testing.T) {
	t.Parallel()
	f := &freshProject{usage: &googletest.ServiceUsage{ServicesOff: slices.Clone(infraPartAPIs)}}
	p := pushing(t, f.serve(t))
	recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys)
	pre := declaringADatabase("17")
	pre.Deploy.Slug = "shop"

	if err := p.ApplyInfraPreflight(t.Context(), pre); err == nil {
		t.Fatal("ApplyInfraPreflight() = nil, want the fresh project's first read of the network refused")
	}

	if got := f.usage.EnabledIDs(); !slices.Equal(got, infraPartAPIs) {
		t.Errorf("ApplyInfraPreflight() switched on %v, want %v", got, infraPartAPIs)
	}
	calls := f.called()
	enabled := slices.IndexFunc(calls, func(call string) bool { return strings.HasSuffix(call, ":batchEnable") })
	reads := f.readsBeyondServiceUsage()
	if enabled < 0 || len(reads) == 0 || slices.Index(calls, reads[0]) < enabled {
		t.Errorf("ApplyInfraPreflight() called %v, want every service its parts use switched on before its first read", calls)
	}
}

func TestAnInfraRunWhoseCredentialMayNotSwitchServicesOnIsRefusedBeforeItAddsAnything(t *testing.T) {
	t.Parallel()
	f := &freshProject{usage: &googletest.ServiceUsage{ServicesOff: []string{"cloudkms.googleapis.com"}}, withheld: []string{"serviceusage.services.enable"}}
	p := pushing(t, f.serve(t))
	pre := declaringADatabase("17")
	pre.Deploy.Slug = "shop"

	for name, run := range map[string]func() error{
		"plan":  func() error { _, err := p.PlanInfraPreflight(t.Context(), pre); return err },
		"apply": func() error { return p.ApplyInfraPreflight(t.Context(), pre) },
	} {
		err := run()

		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied || !strings.Contains(refused.Message, "gcloud services enable cloudkms.googleapis.com --project acme-prod") {
			t.Errorf("%s = %v, want a %s refusal naming the command that switches cloudkms.googleapis.com on", name, err, refusal.CodeDenied)
		}
	}
	if len(f.usage.Enabled) > 0 {
		t.Errorf("switched on %v, want nothing", f.usage.Enabled)
	}
}
