package providerserver_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func projectRequest() *contractv1.ProjectRequest {
	return &contractv1.ProjectRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	}
}

func deployedProject(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	builtProject(t)
	client, vendor := deployServed(t)
	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	return client, vendor
}

func kinds(plan *planv1.ChangePlan) []string {
	var out []string
	for _, item := range plan.GetGroups() {
		out = append(out, item.GetKind())
	}
	return out
}

func TestPlanRemoveProjectNamesEveryStackTheDeployProvisioned(t *testing.T) {
	client, _ := deployedProject(t)

	plan, err := client.PlanRemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	planned := kinds(plan)
	for _, kind := range []string{provider.StackGroupKind, edge.EdgeGroupKind, "variable values", "stored objects"} {
		if !slices.Contains(planned, kind) {
			t.Errorf("the plan names %v, want a %q group among them", planned, kind)
		}
	}
	if plan.GetSubject() != "shop" {
		t.Errorf("the plan's subject is %q, want the project it removes", plan.GetSubject())
	}
	for _, group := range plan.GetGroups() {
		if group.GetName() == "" {
			t.Errorf("the plan contains %+v, and the CLI cannot render a nameless group", group)
		}
		if group.GetKind() == provider.StackGroupKind && !strings.HasPrefix(group.GetName(), "fake/") {
			t.Errorf("the plan contains %+v, want every stack named under the vendor that hosts it", group)
		}
		for _, change := range group.GetChanges() {
			if change.GetKind() == "" || change.GetName() == "" {
				t.Errorf("the plan contains row %+v, and a row renders as a name in a type column", change)
			}
		}
	}
}

func TestPlanRemoveProjectOfAProjectNothingDeployedNamesNoStack(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	plan, err := client.PlanRemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	if slices.Contains(kinds(plan), provider.StackGroupKind) {
		t.Errorf("the plan names %v for a project that never deployed", kinds(plan))
	}
}

func TestRemoveProjectDestroysEveryStackAndForgetsTheProject(t *testing.T) {
	client, vendor := deployedProject(t)

	stream, err := client.RemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the project removed", result.GetError())
	}

	entries, err := stackrecords.List(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the project still records %v, want every stack forgotten", entries)
	}
}

func removeProject(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.ProjectRequest) *progressv1.OperationResult {
	t.Helper()
	stream, err := client.RemoveProject(context.Background(), req)
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRemoveProjectIsRefusedWhileADeployHoldsProduction(t *testing.T) {
	client, vendor := deployedProject(t)
	takeLeaseOver(t, vendor.KeyValues(), otherEnvironmentLease)

	result := removeProject(t, client, projectRequest())

	if result.GetSuccess() || !strings.Contains(result.GetError(), "a deploy to prod is running: remove it again once it ends") {
		t.Fatalf("RemoveProject() = %q, want it refused while a deploy holds production", result.GetError())
	}
	if entries, err := stackrecords.List(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop"); err != nil || len(entries) == 0 {
		t.Errorf("the refused removal left %d stacks recorded (%v), want every stack kept", len(entries), err)
	}
}

func TestRemovingEveryPreviewIsRefusedWhileAFirstDeployToOneHoldsItsLease(t *testing.T) {
	client, vendor := deployedProject(t)
	recordLease(t, vendor.KeyValues(), environment.TierPreview, "pr-9", otherEnvironmentLease)

	result := removeProject(t, client, &contractv1.ProjectRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
	})

	if result.GetSuccess() || !strings.Contains(result.GetError(), "a deploy to pr-9 is running") {
		t.Fatalf("RemoveProject() of every preview = %q, want it refused while a deploy holds pr-9, though pr-9 records no stack yet", result.GetError())
	}
}

func TestRemoveProjectHoldsProductionWhileItRemovesItAndLeavesNoLeaseBehind(t *testing.T) {
	client, vendor := deployedProject(t)
	var during error
	relayPlane(vendor).BeforeNextPointerRemoval(func() {
		during = takeLeaseAsDeploy(vendor.KeyValues(), environment.TierProduction, stackrecords.ProductionEnv)
	})

	if result := removeProject(t, client, projectRequest()); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the project removed", result.GetError())
	}

	if during == nil || !strings.Contains(during.Error(), "a removal of shop in production is running") {
		t.Errorf("a deploy taking production while it was removed = %v, want it refused because the removal holds production", during)
	}
	if isLeaseHeld(t, vendor.KeyValues(), environment.TierProduction, stackrecords.ProductionEnv) {
		t.Error("production still records a lease after the project was removed, want nothing left behind")
	}
}

func TestRemoveProjectErasesItsLedger(t *testing.T) {
	client, vendor := deployedProject(t)
	partition := ledger.Partition(environment.TierProduction, "shop")
	if kept, err := vendor.KeyValues().List(context.Background(), partition); err != nil || len(kept) == 0 {
		t.Fatalf("the deploy left %d ledger entries (%v), and a removal that erases none proves nothing", len(kept), err)
	}

	stream, err := client.RemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v, want the project removed", result.GetError(), err)
	}

	kept, err := vendor.KeyValues().List(context.Background(), partition)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 0 {
		t.Errorf("the ledger keeps %d entries after the project was removed, want none: a destroyed project leaves no bytes behind", len(kept))
	}
}

func TestRemoveProjectPurgesTheValuesAndObjectsItsReleasesWrote(t *testing.T) {
	client, vendor := deployedProject(t)
	ctx := context.Background()

	specs := vendor.FakeStacks().Provisioned()
	ref := specs[1].App.Functions[0].Artifact

	stream, err := client.RemoveProject(ctx, projectRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	if opened, err := vendor.Artifacts().Open(ctx, ref); err == nil {
		opened.Close()
		t.Errorf("the artifact at %s survived the removal, want the project's whole prefix gone", ref.Key)
	}

	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	names, err := store.PublishedNames(ctx, variablestore.Scope{Project: "shop", Tier: environment.TierProduction}, stackrecords.ProductionEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Errorf("the removal left bindings %v published, want the project's values purged", names)
	}
}

func TestRemoveProjectLeavesNothingInTheProjectsValuesOrReferencesPartitions(t *testing.T) {
	client, vendor := deployedProject(t)
	ctx := context.Background()

	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	shop := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	region := variablestore.Coordinate{Cell: variablestore.Cell{Folder: "/", Key: "REGION"}}
	if _, err := store.Set(ctx, shop, region, "eu", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, variablestore.Scope{Project: "blog", Tier: environment.TierProduction}, region,
		variablestore.Target{Project: "shop", Cell: region.Cell}); err != nil {
		t.Fatal(err)
	}
	partitions := map[string]keyvalue.Partition{
		"values":     variablestore.ValuesPartition(shop),
		"references": variablestore.ReferencesPartition(shop),
	}
	for what, partition := range partitions {
		if kept, err := vendor.KeyValues().List(ctx, partition); err != nil || len(kept) == 0 {
			t.Fatalf("the project's %s partition holds %d entries (%v) before the removal, and a removal that purges none proves nothing", what, len(kept), err)
		}
	}

	stream, err := client.RemoveProject(ctx, projectRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	for what, partition := range partitions {
		kept, err := vendor.KeyValues().List(ctx, partition)
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) != 0 {
			t.Errorf("the project's %s partition keeps %d entries after the removal, want none: a destroyed project leaves no bytes behind", what, len(kept))
		}
	}
}

func TestRemoveProjectOnThePreviewTierForgetsEveryPreviewsEnvironmentRecord(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	if result, _ := deploy(t, client, previewRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	ctx := context.Background()
	partition := stackrecords.EnvironmentsPartition(environment.TierPreview, "shop")
	if kept, err := vendor.KeyValues().List(ctx, partition); err != nil || len(kept) == 0 {
		t.Fatalf("the preview deploy recorded %d environments (%v), and a removal that forgets none proves nothing", len(kept), err)
	}

	stream, err := client.RemoveProject(ctx, &contractv1.ProjectRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	kept, err := vendor.KeyValues().List(ctx, partition)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range kept {
		t.Errorf("%s is still recorded after the project's previews were removed: a destroyed project leaves no bytes behind, and a later preview of that name would read its lifecycle and alias", entry.Key)
	}
}

func TestRemoveProjectRetiresTheISRPrefixOfEveryReleaseBeforeSweepingTheProject(t *testing.T) {
	client, vendor := deployedProject(t)

	stream, err := client.RemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	var want []string
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.App == nil {
			continue
		}
		coordinate := naming.Coordinate{Project: "shop", Env: spec.Ref.Name.Env, App: spec.Ref.Name.App, Release: spec.Ref.Name.Release}
		want = append(want, "remove-prefix "+coordinate.ISRPrefix())
	}
	if len(want) == 0 {
		t.Fatal("the deploy provisioned no app stack to remove")
	}
	inOrder(t, vendor.Journal(), append(want, "remove-prefix "+naming.Coordinate{Project: "shop", Env: stackrecords.ProductionEnv}.StoragePrefix())...)
}

func TestRemoveProjectForgetsItsEnvSourceAndHowItsSyncsWent(t *testing.T) {
	client, vendor := deployedProject(t)
	ctx := context.Background()
	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	registration := envsource.Registration{Project: "shop", Descriptor: execDescriptor(t), Folders: []string{""}}
	if _, err := envsource.Register(ctx, store, environment.TierProduction, registration); err != nil {
		t.Fatal(err)
	}
	sync := &envsource.Sync{Store: store, Tier: environment.TierProduction}
	if _, err := sync.CopyProjectFrom(ctx, registration, envsource.NewFixed("exec", nil)); err != nil {
		t.Fatal(err)
	}

	stream, err := client.RemoveProject(ctx, projectRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	if _, registered, err := envsource.Registered(ctx, store.KeyValues, environment.TierProduction, "shop"); err != nil || registered {
		t.Errorf("Registered() after the removal = %v, %v, want a scheduled sync to stop reading for a project that is gone", registered, err)
	}
	if status, err := envsource.StatusOf(ctx, store, environment.TierProduction, registration); err != nil || !status.LastAttemptAt.IsZero() {
		t.Errorf("StatusOf() after the removal = %+v, %v, want the status record removed with the project", status, err)
	}
}

func TestRemoveProjectRefusesACallNamingNoProject(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	if _, err := client.PlanRemoveProject(context.Background(), &contractv1.ProjectRequest{
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	}); err == nil {
		t.Fatal("PlanRemoveProject() with no slug succeeded, want it refused")
	}
}

func cutOverProject(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider, *fake.DNSRecords) {
	t.Helper()
	client, p := contractServed(t, "1.0.0")
	seedStack(t, p, environment.TierProduction, "shop", stackrecords.EdgeState{
		Kind: fake.KindRelay,
		Edge: edge.StackState{
			Slug:     "shop",
			Tier:     environment.TierProduction,
			Endpoint: "https://shop.fake.invalid",
			Address:  "shop.relay.fake.invalid",
			Bound:    []string{"app.acme.com"},
		},
		Hosts: map[string]stackrecords.HostnameState{
			"app.acme.com": {
				Router:      fake.RouterRelay,
				Certificate: provider.Certificate{ID: "cert-for-app"},
				Written:     []edge.Record{{Name: "app.acme.com", Type: edge.RecordTypeCNAME, Value: "shop.relay.fake.invalid"}},
				Manual:      []edge.Record{{Name: "manual.acme.com", Type: edge.RecordTypeCNAME, Value: "shop.relay.fake.invalid"}},
			},
		},
	})
	writer, err := p.DNS().Open(fake.KindZone, "acme.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Ensure(context.Background(), []edge.Record{
		{Name: "app.acme.com", Type: edge.RecordTypeCNAME, Value: "shop.relay.fake.invalid"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	return client, p, writer.(*fake.DNSRecords)
}

func cutOverRequest() *contractv1.ProjectRequest {
	req := projectRequest()
	req.Edge = zoned("acme.com")
	return req
}

func TestPlanRemoveProjectNamesTheRecordsAndCertificatesItsHostnamesUse(t *testing.T) {
	t.Parallel()
	client, _, _ := cutOverProject(t)

	plan, err := client.PlanRemoveProject(context.Background(), cutOverRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	planned := kinds(plan)
	for _, kind := range []string{"DNS record", "certificate"} {
		if !slices.Contains(planned, kind) {
			t.Errorf("the plan names %v, want a %q item among them", planned, kind)
		}
	}
	for _, item := range plan.GetGroups() {
		switch item.GetName() {
		case "manual.acme.com CNAME shop.relay.fake.invalid":
			if item.GetAction() != planv1.Change_ACTION_KEEP {
				t.Errorf("the plan deletes %q, want a record ocel never wrote kept", item.GetName())
			}
		case "cert-for-app":
			if item.GetAction() != planv1.Change_ACTION_KEEP {
				t.Errorf("the plan deletes %q, want a pinned certificate kept", item.GetName())
			}
		}
	}
}

func TestRemoveProjectReleasesTheRecordsItWrote(t *testing.T) {
	t.Parallel()
	client, _, writer := cutOverProject(t)

	stream, err := client.RemoveProject(context.Background(), cutOverRequest())
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the project removed", result.GetError())
	}
	if written := writer.Records(); len(written) != 0 {
		t.Errorf("the zone still contains %v, want every record ocel wrote for this project released", written)
	}
}

func TestRemoveProjectDiscardsTheCertificateOcelRequested(t *testing.T) {
	t.Parallel()
	client, p := contractServed(t, "1.0.0")
	validation := edge.Record{Name: "_ocel.app.acme.com", Type: edge.RecordTypeCNAME, Value: "_target.validations.invalid"}
	stale := edge.Record{Name: "_stale.app.acme.com", Type: edge.RecordTypeCNAME, Value: "_stale.validations.invalid"}
	seedStack(t, p, environment.TierProduction, "shop", stackrecords.EdgeState{
		Kind: fake.KindRelay,
		Edge: edge.StackState{
			Slug:     "shop",
			Tier:     environment.TierProduction,
			Endpoint: "https://shop.fake.invalid",
			Address:  "shop.relay.fake.invalid",
			Bound:    []string{"app.acme.com"},
		},
		Hosts: map[string]stackrecords.HostnameState{
			"app.acme.com": {
				Router:      fake.RouterRelay,
				Certificate: provider.Certificate{ID: "ocels-cert", Requested: true, Written: []edge.Record{validation}},
				Superseded:  []provider.Certificate{{ID: "stalled-cert", Requested: true, Written: []edge.Record{stale}}},
			},
			"old.acme.com": {Router: fake.RouterRelay, Certificate: provider.Certificate{ID: "pinned-cert"}},
		},
	})
	writer, err := p.DNS().Open(fake.KindZone, "acme.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Ensure(context.Background(), []edge.Record{validation, stale}, nil); err != nil {
		t.Fatal(err)
	}

	plan, err := client.PlanRemoveProject(context.Background(), cutOverRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	for _, item := range plan.GetGroups() {
		switch item.GetName() {
		case "ocels-cert":
			if item.GetAction() != planv1.Change_ACTION_DELETE {
				t.Errorf("the plan keeps %q, want a certificate ocel requested deleted", item.GetName())
			}
		case "pinned-cert":
			if item.GetAction() != planv1.Change_ACTION_KEEP {
				t.Errorf("the plan deletes %q, want a pinned certificate kept", item.GetName())
			}
		}
	}

	stream, err := client.RemoveProject(context.Background(), cutOverRequest())
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the project removed", result.GetError())
	}
	if discarded := p.Discarded(); !slices.Contains(discarded, "ocels-cert") {
		t.Errorf("the provider discarded %v, want the certificate ocel requested among them", discarded)
	}
	if discarded := p.Discarded(); !slices.Contains(discarded, "stalled-cert") {
		t.Errorf("the provider discarded %v, want the certificate a stalled rotation left behind among them", discarded)
	}
	if discarded := p.Discarded(); slices.Contains(discarded, "pinned-cert") {
		t.Errorf("the provider discarded %v, want a pinned certificate left in place", discarded)
	}
	if written := writer.(*fake.DNSRecords).Records(); len(written) != 0 {
		t.Errorf("the zone still contains %v, want the validation record released with the certificate", written)
	}
}

func TestARemovalRefusesWorkTheConsentedProjectPlanNeverShowed(t *testing.T) {
	ctx := context.Background()
	client, vendor := deployedProject(t)

	consented, err := client.PlanRemoveProject(ctx, projectRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}

	admin := naming.AppStack(stackrecords.ProductionEnv, "admin", naming.NewReleaseToken(adminBuildID, "1"))
	if err := stackrecords.Write(ctx, vendor.KeyValues(), environment.TierProduction, "shop", admin, stackrecords.Stack{App: "admin"}); err != nil {
		t.Fatalf("stackrecords.Write() error = %v", err)
	}

	req := projectRequest()
	req.Consented = consented
	stream, err := client.RemoveProject(ctx, req)
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	if _, err := drain(stream); err == nil {
		t.Fatal("RemoveProject() = nil, want a removal that outgrew its consented plan refused")
	} else if !strings.Contains(err.Error(), "admin") {
		t.Errorf("the refusal reads %q, want it to name what was provisioned under the plan", err)
	}
}

func TestARemovalRunsTheConsentedProjectPlanItWasHanded(t *testing.T) {
	ctx := context.Background()
	client, _ := deployedProject(t)

	consented, err := client.PlanRemoveProject(ctx, projectRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}

	req := projectRequest()
	req.Consented = consented
	stream, err := client.RemoveProject(ctx, req)
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the plan the human saw to run", result.GetError())
	}
}

func TestARemovalOpensTheEdgeTheProjectRunsOnRatherThanTheDefault(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	plan, err := client.PlanRemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	if plan.GetEdgeKind() != string(fake.KindDirect) {
		t.Errorf("the removal plans against the %q edge, want the %q edge the project runs on",
			plan.GetEdgeKind(), fake.KindDirect)
	}
}

func TestARemovalOpensItsDNSForTheEdgeTheProjectRunsOnWhenTheConfigNamesNone(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	req.Edge.Kind = string(fake.KindDirect)
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	opened := len(vendor.DNS().(*fake.DNS).Fronts())

	removal := projectRequest()
	removal.Edge = writtenBy("shop.example")
	if _, err := client.PlanRemoveProject(context.Background(), removal); err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	fronts := vendor.DNS().(*fake.DNS).Fronts()[opened:]
	if len(fronts) == 0 || slices.ContainsFunc(fronts, func(front edge.Kind) bool { return front != fake.KindDirect }) {
		t.Errorf("the removal opened its DNS under %v, want the %s edge the stack runs on", fronts, fake.KindDirect)
	}
}

func TestADestroySaysWhichHostnamesAndEdgeItRemoves(t *testing.T) {
	client, _ := deployedProject(t)

	stream, err := client.RemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	var said []string
	for _, event := range recorded(stream) {
		if line := saidLine(event); line != "" {
			said = append(said, line)
		}
	}
	for _, want := range []string{
		"Unbinding shop.example from the relay edge",
		"Removing the @production routing pointer",
		"Destroying the stack that serves shop through the relay edge",
		"Removing the stored variable values of shop in production",
		"Forgetting shop in production: nothing of it is left",
	} {
		if !slices.Contains(said, want) {
			t.Errorf("the destroy said %q, want %q among it", said, want)
		}
	}
}

func TestRemoveProjectRemovesAServiceWhoseHolderLostItsStackRecord(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	var mu sync.Mutex
	var removed []string
	vendor.ResourceStacks(resources.Hooks{Functions: &resources.FunctionHooks{
		Provision: func(context.Context, provider.StackSpec, progress.Log) ([]provider.Function, error) {
			return []provider.Function{{Name: "server", Physical: "shop-web-server", Revision: "shop-web-server-00001"}}, nil
		},
		Remove: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ provider.ImageStore, _ progress.Log) error {
			mu.Lock()
			defer mu.Unlock()
			for _, function := range functions {
				removed = append(removed, function.Physical)
			}
			return nil
		},
		Shared: &resources.SharedHooks[provider.Function]{
			Name: func(context.Context, provider.StackSpec) ([]provider.Function, error) {
				return []provider.Function{{Name: "server", Physical: "shop-web-server"}}, nil
			},
			RemoveRevisions: func(context.Context, provider.StackRef, []provider.Function, provider.ImageStore, progress.Log) ([]provider.Function, error) {
				return nil, nil
			},
		},
	}})
	req := deployRequest()
	req.Manifest.Resources, req.Manifest.Usages = nil, nil
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	ctx := context.Background()
	entries, err := stackrecords.List(ctx, vendor.KeyValues(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.Name.IsInfra() {
			if err := stackrecords.Forget(ctx, vendor.KeyValues(), environment.TierProduction, "shop", entry.Name); err != nil {
				t.Fatal(err)
			}
		}
	}

	stream, err := client.RemoveProject(ctx, projectRequest())
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v, want the project removed", result.GetError(), err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(removed, []string{"shop-web-server"}) {
		t.Errorf("the removal took down %v, want the service a release still held though its stack record was gone", removed)
	}
	shared := keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootSharedPhysicals, Path: []string{"shop"}}
	if left, err := vendor.KeyValues().List(ctx, shared); err != nil || len(left) != 0 {
		t.Errorf("the removal left %v, %v behind, want nothing: a removed project leaves no bytes", left, err)
	}
}

func execDescriptor(t *testing.T) envsource.Descriptor {
	t.Helper()
	descriptor, err := envsource.NewDescriptor("exec", []byte(`{"command":["op"],"format":"json"}`))
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func TestRemoveProjectDestroysAStackRecordedBeforeItTookTheProject(t *testing.T) {
	client, vendor := deployedProject(t)
	late := naming.AppStack(stackrecords.ProductionEnv, "docs", releaseOf(t, releaseFor(9)))
	var recorded error
	vendor.KeyValues().(*fake.KeyValues).BeforeNextWrite(stackrecords.ProjectLeaseKey(environment.TierProduction, "shop"), func() {
		recorded = stackrecords.Write(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop", late, stackrecords.Stack{Kind: provider.StackApp})
	})

	if result := removeProject(t, client, projectRequest()); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the project removed", result.GetError())
	}

	if recorded != nil {
		t.Fatal(recorded)
	}
	if entries, err := stackrecords.List(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop"); err != nil || len(entries) != 0 {
		t.Errorf("after the removal %d stacks are still recorded (%v), want %s, recorded before the removal took the project, destroyed with the rest", len(entries), err, late)
	}
}

func TestRemovingEveryPreviewRefusesAFirstDeployToANewPreviewUntilItEnds(t *testing.T) {
	client, vendor := deployedProject(t)
	recordLabelledEnvironment(t, vendor, "pr-7", "pr-123", stackrecords.LifecyclePersistent)
	var during error
	vendor.KeyValues().(*fake.KeyValues).BeforeNextWrite(stackrecords.EnvironmentLeaseKey(environment.TierPreview, "shop", "pr-7"), func() {
		during = takeLeaseAsDeploy(vendor.KeyValues(), environment.TierPreview, "pr-9")
	})

	removeProject(t, client, &contractv1.ProjectRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
	})

	if during == nil || !strings.Contains(during.Error(), "a removal of shop in preview is running") {
		t.Errorf("a first deploy to pr-9 while every preview was removed = %v, want it refused because the removal holds every preview of shop", during)
	}
	if isLeaseHeld(t, vendor.KeyValues(), environment.TierPreview, "pr-9") {
		t.Error("the refused deploy left a lease on pr-9")
	}
	if _, err := vendor.KeyValues().Read(context.Background(), stackrecords.ProjectLeaseKey(environment.TierPreview, "shop")); !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("reading the removal's lease on shop after it ended = %v, want it freed", err)
	}
}

func recordAppStacks(t *testing.T, vendor *fake.Provider, n int) []naming.StackName {
	t.Helper()
	stacks := make([]naming.StackName, 0, n)
	for i := range n {
		stack := naming.AppStack(stackrecords.ProductionEnv, fmt.Sprintf("app%d", i), releaseOf(t, releaseFor(100+i)))
		if err := stackrecords.Write(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop", stack, stackrecords.Stack{Kind: provider.StackApp}); err != nil {
			t.Fatal(err)
		}
		stacks = append(stacks, stack)
	}
	return stacks
}

func recordedStacks(t *testing.T, vendor *fake.Provider) []naming.StackName {
	t.Helper()
	entries, err := stackrecords.List(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	stacks := make([]naming.StackName, 0, len(entries))
	for _, entry := range entries {
		stacks = append(stacks, entry.Name)
	}
	return stacks
}

func countConcurrentAppDestroys(stacks *fake.Stacks) func() int {
	var mu sync.Mutex
	inFlight, most := 0, 0
	full := make(chan struct{})
	var filled sync.Once
	stacks.Destroying(func(ref provider.StackRef) error {
		if ref.Name.IsInfra() {
			return nil
		}
		mu.Lock()
		inFlight++
		most = max(most, inFlight)
		if inFlight == providerserver.StackDestroyConcurrency {
			filled.Do(func() { close(full) })
		}
		mu.Unlock()
		select {
		case <-full:
		case <-time.After(time.Second):
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		return nil
	})
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return most
	}
}

func TestRemoveProjectDestroysItsAppStacksConcurrentlyButNeverMoreThanTheBoundAtOnce(t *testing.T) {
	client, vendor := deployedProject(t)
	recordAppStacks(t, vendor, 2*providerserver.StackDestroyConcurrency)
	most := countConcurrentAppDestroys(vendor.FakeStacks())

	if result := removeProject(t, client, projectRequest()); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the project removed", result.GetError())
	}

	if most() != providerserver.StackDestroyConcurrency {
		t.Errorf("at most %d app stacks were destroyed at once, want %d", most(), providerserver.StackDestroyConcurrency)
	}
}

func TestRemoveProjectDestroysNoInfraStackUntilEveryAppStackIsDestroyed(t *testing.T) {
	client, vendor := deployedProject(t)
	recordAppStacks(t, vendor, 2*providerserver.StackDestroyConcurrency)
	apps := 0
	for _, stack := range recordedStacks(t, vendor) {
		if !stack.IsInfra() {
			apps++
		}
	}
	var mu sync.Mutex
	destroyed := 0
	var early []string
	vendor.FakeStacks().Destroying(func(ref provider.StackRef) error {
		if ref.Name.IsInfra() {
			mu.Lock()
			defer mu.Unlock()
			if destroyed < apps {
				early = append(early, fmt.Sprintf("%s after %d of %d app stacks", ref.Name, destroyed, apps))
			}
			return nil
		}
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		destroyed++
		return nil
	})

	if result := removeProject(t, client, projectRequest()); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the project removed", result.GetError())
	}

	if len(early) > 0 {
		t.Errorf("the removal started destroying %v, want every app stack destroyed before any infra stack", early)
	}
}

func TestRemoveProjectDestroysEveryOtherStackWhenOneAppStackFailsAndReportsTheFailure(t *testing.T) {
	client, vendor := deployedProject(t)
	failing := recordAppStacks(t, vendor, 2*providerserver.StackDestroyConcurrency)[3]
	vendor.FakeStacks().Destroying(func(ref provider.StackRef) error {
		if ref.Name == failing {
			return errors.New("the stack's state is locked")
		}
		return nil
	})

	result := removeProject(t, client, projectRequest())

	if result.GetSuccess() || !strings.Contains(result.GetError(), failing.String()) {
		t.Errorf("RemoveProject() = success %v, %q, want it to fail naming %s", result.GetSuccess(), result.GetError(), failing)
	}
	if left := recordedStacks(t, vendor); !slices.Equal(left, []naming.StackName{failing}) {
		t.Errorf("after the removal %v are still recorded, want only %s, whose destroy failed", left, failing)
	}
}

func TestRemoveProjectOpensASpanForEveryStackItDestroys(t *testing.T) {
	client, vendor := deployedProject(t)
	stacks := recordedStacks(t, vendor)

	stream, err := client.RemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	opened := openedSpans(recorded(stream))

	for _, stack := range stacks {
		want := openedSpan{stack.String(), "Destroying everything this release of " + stack.App + " provisioned"}
		if stack.IsInfra() {
			want.message = "Destroying the resources every app in " + stack.Env + " binds to"
		}
		if !slices.Contains(opened, want) {
			t.Errorf("the removal opened spans %v, want %v among them", opened, want)
		}
	}
}
