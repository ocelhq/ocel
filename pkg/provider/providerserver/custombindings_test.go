package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/environment"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

var fleetGrant = &variablestorev1.Grant{Label: "hosts", Role: "roles/compute.instanceAdmin.v1", Resource: "projects/ocel-cloud-prod"}

func publishFleet(t *testing.T, vendor *fake.Provider) {
	t.Helper()
	published := &variablestorev1.PublishedBinding{
		Binding: &bindingsv1.Binding{
			Name:       "fleet",
			Source:     "pulumi",
			Properties: &bindingsv1.Binding_Custom{Custom: &structpb.Struct{Fields: map[string]*structpb.Value{"zone": structpb.NewStringValue("us-central1-a")}}},
		},
		Grants: []*variablestorev1.Grant{fleetGrant},
	}
	pair, err := variablestoreserver.BindingPair("pulumi", published)
	if err != nil {
		t.Fatalf("BindingPair: %v", err)
	}
	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	if _, err := store.SetBindings(context.Background(), scope, "", "pulumi", []variablestore.NamedBindingWrite{{Name: "fleet", Write: pair}}); err != nil {
		t.Fatalf("SetBindings: %v", err)
	}
}

func boundToFleet() *contractv1.DeployRequest {
	req := twoAppRequest()
	for _, app := range req.GetManifest().GetApps() {
		if app.GetName() == "admin" {
			app.CustomBindings = []string{"fleet"}
		}
	}
	return req
}

func TestAnAppBoundToACustomRecordIsGrantedItButHandedNoneOfItsValues(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	publishFleet(t, vendor)

	if result, _ := deploy(t, client, boundToFleet()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	apps := map[string]provider.StackSpec{}
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.App != nil {
			apps[spec.App.App] = spec
		}
	}
	admin := apps["admin"].App
	at := slices.IndexFunc(admin.Grants, func(binding provider.Binding) bool { return binding.Name == "fleet" })
	if at < 0 {
		t.Fatalf("admin is granted %v, want fleet, the custom record its config binds it to", grantNames(apps["admin"]))
	}
	if grants := admin.Grants[at].Grants; len(grants) != 1 || grants[0].Role != fleetGrant.GetRole() || grants[0].Resource != fleetGrant.GetResource() {
		t.Errorf("admin's fleet grants = %+v, want the role fleet was published with", grants)
	}
	if slices.ContainsFunc(admin.Values.Bindings, func(binding provider.Binding) bool { return binding.Name == "fleet" }) {
		t.Error("admin is handed fleet's values, and a custom record's values are read by transforms alone")
	}
	if slices.Contains(grantNames(apps["web"]), "fleet") {
		t.Errorf("web is granted %v, and its config binds it to no custom record", grantNames(apps["web"]))
	}
}

func TestADeployBindingAnAppToACustomRecordNothingPublishedIsRefused(t *testing.T) {
	message := refusedDeploy(t, boundToFleet(), nil)
	if !strings.Contains(message, `"fleet"`) {
		t.Errorf("refusal = %q, want it to name the custom record nothing published", message)
	}
}

func TestADeployBindingAnAppToARecordThatIsNotCustomAsCustomIsRefused(t *testing.T) {
	message := refusedDeploy(t, boundToFleet(), func(p *fake.Provider) {
		publishRecord(t, p, environment.TierProduction, "terraform", postgresRecord("fleet", "terraform"))
	})
	if !strings.Contains(message, "fleet") || !strings.Contains(message, "postgres") {
		t.Errorf("refusal = %q, want it to name fleet and the type it was published as", message)
	}
}
