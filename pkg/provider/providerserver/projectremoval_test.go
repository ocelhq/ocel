package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
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

	admin := naming.AppStack(stackrecords.ProductionEnv, "admin", naming.NewReleaseToken(adminDeploymentID, "1"))
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

func TestADestroySaysWhichStacksHostnamesAndEdgeItRemovesAndHowFarAlongItIs(t *testing.T) {
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
		"Destroying stack prod--infra (2 of 2)",
		"Destroying the stack that serves shop through the relay edge",
		"Removing the stored variable values of shop in production",
		"Forgetting shop in production: nothing of it is left",
	} {
		if !slices.Contains(said, want) {
			t.Errorf("the destroy said %q, want %q among it", said, want)
		}
	}
	if !slices.ContainsFunc(said, func(line string) bool {
		return strings.HasPrefix(line, "Destroying stack prod--web--") && strings.HasSuffix(line, " (1 of 2)")
	}) {
		t.Errorf("the destroy said %q, want the web stack named as the first of 2", said)
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
		Remove: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ progress.Log) error {
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
			RemoveRevisions: func(context.Context, provider.StackRef, []provider.Function, progress.Log) ([]provider.Function, error) {
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
