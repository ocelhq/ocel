package providerserver_test

import (
	"context"
	"encoding/json"
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
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

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

func TestARemovalRerunDiscardsTheCertificateAnEarlierRunFailedToDiscard(t *testing.T) {
	t.Parallel()
	client, p := contractServed(t, "1.0.0")
	validation := edge.Record{Name: "_ocel.app.acme.com", Type: edge.RecordTypeCNAME, Value: "_target.validations.invalid"}
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
			},
		},
	})
	writer, err := p.DNS().Open(fake.KindZone, "acme.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Ensure(context.Background(), []edge.Record{validation}, nil); err != nil {
		t.Fatal(err)
	}

	p.QueueDiscardFailures(errors.New("the provider throttled the delete"))
	if result := removeProject(t, client, cutOverRequest()); result.GetSuccess() {
		t.Fatal("RemoveProject() removed the project, want it failed on a certificate it could not discard")
	}
	if result := removeProject(t, client, cutOverRequest()); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the rerun to remove the project", result.GetError())
	}

	if discarded := p.Discarded(); !slices.Contains(discarded, "ocels-cert") {
		t.Errorf("the provider discarded %v, want the rerun to discard the certificate the first run could not", discarded)
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
	shared := records.Partition{Tier: environment.TierProduction, Root: resources.RootSharedPhysicals, Path: []string{"shop"}}
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

func TestRemovingEveryPreviewSaysUntilWhenItWaitsOutTheLeasesInterruptedRunsLeft(t *testing.T) {
	client, vendor := deployedProject(t)
	for _, env := range []string{"pr-1", "pr-2"} {
		value, err := json.Marshal(stackrecords.EnvironmentLease{Token: otherEnvironmentLease, Holder: stackrecords.LeaseDeploy, ExpiresAt: time.Now().Add(-time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := vendor.KeyValues().Write(context.Background(), records.Entry{Key: stackrecords.EnvironmentLeaseKey(environment.TierPreview, "shop", env), Value: value}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	began := time.Now().UTC()

	stream, err := client.RemoveProject(ctx, &contractv1.ProjectRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
	})
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	defer stream.Close()
	var said []string
	for stream.Receive() {
		line := saidLine(stream.Msg())
		if line == "" {
			continue
		}
		said = append(said, line)
		if strings.HasPrefix(line, "Waiting") {
			break
		}
	}

	var wants []string
	for _, at := range []time.Time{began, time.Now().UTC()} {
		wants = append(wants, "Waiting until "+at.Add(5*time.Minute).Format("15:04 MST")+" to take over the leases interrupted runs left on 2 environments, in case one still renews its lease")
	}
	if !slices.ContainsFunc(said, func(line string) bool { return slices.Contains(wants, line) }) {
		t.Errorf("the removal said %q, want %q among it", said, wants[0])
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
	if _, err := vendor.KeyValues().Read(context.Background(), stackrecords.ProjectLeaseKey(environment.TierPreview, "shop")); !errors.Is(err, records.ErrNotFound) {
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
