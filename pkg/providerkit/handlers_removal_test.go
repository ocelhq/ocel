package providerkit_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
	client, provider := deployServed(t)
	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	return client, provider
}

func kinds(plan *planv1.ChangePlan) []string {
	var out []string
	for _, item := range plan.GetGroups() {
		out = append(out, item.GetKind())
	}
	return out
}

func TestPlanRemoveProjectNamesEveryStackTheDeployStoodUp(t *testing.T) {
	client, _ := deployedProject(t)

	plan, err := client.PlanRemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	held := kinds(plan)
	for _, kind := range []string{providerkit.StackGroupKind, providerkit.EdgeGroupKind, "variable values", "stored objects"} {
		if !slices.Contains(held, kind) {
			t.Errorf("the plan names %v, want a %q group among them", held, kind)
		}
	}
	if plan.GetSubject() != "shop" {
		t.Errorf("the plan's subject is %q, want the project it removes", plan.GetSubject())
	}
	for _, group := range plan.GetGroups() {
		if group.GetName() == "" {
			t.Errorf("the plan carries %+v, and the CLI cannot render a nameless group", group)
		}
		if group.GetKind() == providerkit.StackGroupKind && !strings.HasPrefix(group.GetName(), "fake/") {
			t.Errorf("the plan carries %+v, want every stack named under the vendor that holds it", group)
		}
		for _, change := range group.GetChanges() {
			if change.GetKind() == "" || change.GetName() == "" {
				t.Errorf("the plan carries row %+v, and a row renders as a name in a type column", change)
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
	if slices.Contains(kinds(plan), providerkit.StackGroupKind) {
		t.Errorf("the plan names %v for a project that never deployed", kinds(plan))
	}
}

func TestRemoveProjectDestroysEveryStackAndForgetsTheProject(t *testing.T) {
	client, provider := deployedProject(t)

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

	entries, err := providerkit.ReadStacks(context.Background(), provider.Records(), providerkit.ClassProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the project still records %v, want every stack forgotten", entries)
	}
}

func TestRemoveProjectPurgesTheValuesAndObjectsItsReleasesWrote(t *testing.T) {
	client, provider := deployedProject(t)
	ctx := context.Background()

	plans := provider.Releases().(*fake.Releaser).Plans()
	ref := plans[1].App.Functions[0].Artifact

	stream, err := client.RemoveProject(ctx, projectRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	if opened, err := provider.Artifacts().Open(ctx, ref); err == nil {
		opened.Close()
		t.Errorf("the artifact at %s survived the removal, want the project's whole prefix gone", ref.Key)
	}

	store := values.Store{Records: provider.Records(), Sealer: provider.Sealer()}
	names, err := store.PublishedNames(ctx, values.Scope{Project: "shop", Class: providerkit.ClassProduction}, providerkit.ProductionEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Errorf("the removal left links %v published, want the project's values purged", names)
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

func settledProject(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider, *fake.DNSWriter) {
	t.Helper()
	client, provider := contractServed(t, "1.0.0")
	seedStack(t, provider, providerkit.ClassProduction, "shop", providerkit.EdgeStackState{
		Edge: edge.StackState{
			Slug:     "shop",
			Class:    providerkit.ClassProduction,
			Endpoint: "https://shop.fake.invalid",
			Front:    "shop.relay.fake.invalid",
			Bound:    []string{"app.acme.com"},
		},
		Hosts: map[string]providerkit.Settled{
			"app.acme.com": {
				Certificate: providerkit.Certificate{ID: "cert-for-app"},
				Written:     []edge.Record{{Name: "app.acme.com", Type: edge.RecordTypeCNAME, Value: "shop.relay.fake.invalid"}},
				Owed:        []edge.Record{{Name: "owed.acme.com", Type: edge.RecordTypeCNAME, Value: "shop.relay.fake.invalid"}},
			},
		},
	})
	writer, err := provider.DNS().Open(fake.KindZone, "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.EnsureRecords(context.Background(), []edge.Record{
		{Name: "app.acme.com", Type: edge.RecordTypeCNAME, Value: "shop.relay.fake.invalid"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	return client, provider, writer.(*fake.DNSWriter)
}

func settledRequest() *contractv1.ProjectRequest {
	req := projectRequest()
	req.Edge = zoned("acme.com")
	return req
}

func TestPlanRemoveProjectNamesTheRecordsAndCertificatesItsHostnamesHold(t *testing.T) {
	t.Parallel()
	client, _, _ := settledProject(t)

	plan, err := client.PlanRemoveProject(context.Background(), settledRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	held := kinds(plan)
	for _, kind := range []string{"DNS record", "certificate"} {
		if !slices.Contains(held, kind) {
			t.Errorf("the plan names %v, want a %q item among them", held, kind)
		}
	}
	for _, item := range plan.GetGroups() {
		switch item.GetName() {
		case "owed.acme.com CNAME shop.relay.fake.invalid":
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
	client, _, writer := settledProject(t)

	stream, err := client.RemoveProject(context.Background(), settledRequest())
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
	if held := writer.Records(); len(held) != 0 {
		t.Errorf("the zone still holds %v, want every record ocel wrote for this project released", held)
	}
}

func TestRemoveProjectDiscardsTheCertificateOcelRequested(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	validation := edge.Record{Name: "_ocel.app.acme.com", Type: edge.RecordTypeCNAME, Value: "_target.validations.invalid"}
	stale := edge.Record{Name: "_stale.app.acme.com", Type: edge.RecordTypeCNAME, Value: "_stale.validations.invalid"}
	seedStack(t, provider, providerkit.ClassProduction, "shop", providerkit.EdgeStackState{
		Edge: edge.StackState{
			Slug:     "shop",
			Class:    providerkit.ClassProduction,
			Endpoint: "https://shop.fake.invalid",
			Front:    "shop.relay.fake.invalid",
			Bound:    []string{"app.acme.com"},
		},
		Hosts: map[string]providerkit.Settled{
			"app.acme.com": {
				Certificate: providerkit.Certificate{ID: "ocels-cert", Requested: true, Written: []edge.Record{validation}},
				Superseded:  []providerkit.Certificate{{ID: "stalled-cert", Requested: true, Written: []edge.Record{stale}}},
			},
			"old.acme.com": {Certificate: providerkit.Certificate{ID: "pinned-cert"}},
		},
	})
	writer, err := provider.DNS().Open(fake.KindZone, "acme.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.EnsureRecords(context.Background(), []edge.Record{validation, stale}, nil); err != nil {
		t.Fatal(err)
	}

	plan, err := client.PlanRemoveProject(context.Background(), settledRequest())
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

	stream, err := client.RemoveProject(context.Background(), settledRequest())
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
	if discarded := provider.Discarded(); !slices.Contains(discarded, "ocels-cert") {
		t.Errorf("the provider discarded %v, want the certificate ocel requested among them", discarded)
	}
	if discarded := provider.Discarded(); !slices.Contains(discarded, "stalled-cert") {
		t.Errorf("the provider discarded %v, want the certificate a stalled rotation left behind among them", discarded)
	}
	if discarded := provider.Discarded(); slices.Contains(discarded, "pinned-cert") {
		t.Errorf("the provider discarded %v, want a pinned certificate left standing", discarded)
	}
	if held := writer.(*fake.DNSWriter).Records(); len(held) != 0 {
		t.Errorf("the zone still holds %v, want the validation record released with the certificate", held)
	}
}
