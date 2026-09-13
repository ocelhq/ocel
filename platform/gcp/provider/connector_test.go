package gcp

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"google.golang.org/api/cloudresourcemanager/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestTheConnectorImageRunsTheBinaryItCarries(t *testing.T) {
	t.Parallel()

	built, err := connectorImage(empty.Image, []byte("connector"))
	if err != nil {
		t.Fatalf("connectorImage: %v", err)
	}
	file, err := built.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(file.Config.Entrypoint, []string{connectorImagePath}) {
		t.Errorf("the image starts %v, want %s", file.Config.Entrypoint, connectorImagePath)
	}
	if len(file.Config.Cmd) != 0 {
		t.Errorf("the image carries the base's command %v, and a static base's command is not the connector", file.Config.Cmd)
	}
	layers, err := built.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 {
		t.Errorf("the image holds %d layers on an empty base, want the one that carries the connector", len(layers))
	}

	again, err := connectorImage(empty.Image, []byte("connector"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := built.Digest()
	if err != nil {
		t.Fatal(err)
	}
	second, err := again.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("the same connector bytes built to two image digests, so a re-run would push and release an image nothing changed in")
	}
}

func TestTheConnectorServiceRunsOnOneInstanceAtMostAndIsReachableWithoutIAM(t *testing.T) {
	t.Parallel()

	held, err := serviceOf(serving{
		service: "ocel-connector",
		image:   "example.com/ocel-connector:sha256-abc",
		account: "ocel-connector@project.iam.gserviceaccount.com",
		compute: providerkit.ComputeServerless,
		public:  true,
		memory:  connectorMemory,
		most:    connectorInstances,
		ingress: ingressEverywhere,
	})
	if err != nil {
		t.Fatalf("serviceOf: %v", err)
	}
	if !held.InvokerIamDisabled {
		t.Error("the connector service checks IAM on an invoke, and the console carries its own token rather than a Google credential")
	}
	if held.Template.Scaling.MinInstanceCount != 0 || held.Template.Scaling.MaxInstanceCount != connectorInstances {
		t.Errorf("the connector scales %+v, want nothing standing idle and %d at most", held.Template.Scaling, connectorInstances)
	}
	if held.Template.ServiceAccount != "ocel-connector@project.iam.gserviceaccount.com" {
		t.Errorf("the connector runs as %q, want its own service account", held.Template.ServiceAccount)
	}
	if held.Ingress != ingressEverywhere {
		t.Errorf("the connector takes traffic from %q, and the console dials it from outside the project", held.Ingress)
	}
}

func TestAnAppServiceStillScalesAsItDid(t *testing.T) {
	t.Parallel()

	held, err := serviceOf(serving{service: "app", image: "example.com/app:tag", compute: providerkit.ComputeServerless})
	if err != nil {
		t.Fatalf("serviceOf: %v", err)
	}
	if held.Template.Scaling.MaxInstanceCount != 0 {
		t.Errorf("an app that named no ceiling got one of %d", held.Template.Scaling.MaxInstanceCount)
	}
}

func TestAProjectGrantIsAddedOnceAndTakenAwayOnce(t *testing.T) {
	t.Parallel()

	const member = "serviceAccount:ocel-connector@project.iam.gserviceaccount.com"
	bindings, changed := boundMember(nil, connectorRecordsRole, member, true)
	if !changed || len(bindings) != 1 || !slices.Contains(bindings[0].Members, member) {
		t.Fatalf("granting on an empty policy = %+v, %v", bindings, changed)
	}
	if _, again := boundMember(bindings, connectorRecordsRole, member, true); again {
		t.Error("granting twice rewrote the policy, so every install would churn the project's IAM")
	}

	bindings, changed = boundMember(bindings, connectorRecordsRole, member, false)
	if !changed || slices.Contains(bindings[0].Members, member) {
		t.Errorf("revoking left %+v, want the connector off the binding", bindings)
	}
	if _, again := boundMember(bindings, connectorRecordsRole, member, false); again {
		t.Error("revoking twice rewrote the policy, so rm on a target with no connector would still write IAM")
	}
}

func TestAnotherMembersGrantSurvivesTheConnectorsRemoval(t *testing.T) {
	t.Parallel()

	const member = "serviceAccount:ocel-connector@project.iam.gserviceaccount.com"
	held := []*cloudresourcemanager.Binding{
		{Role: connectorRecordsRole, Members: []string{"user:someone@example.com", member}},
	}
	bindings, changed := boundMember(held, connectorRecordsRole, member, false)
	if !changed {
		t.Fatal("revoking changed nothing")
	}
	if !slices.Equal(bindings[0].Members, []string{"user:someone@example.com"}) {
		t.Errorf("revoking left %v, and taking a connector off an account may not take anybody else's access with it", bindings[0].Members)
	}
}

func TestTheConnectorIsNamedForTheNamespaceAndFitsWhatIAMTakes(t *testing.T) {
	t.Parallel()

	names := Names{namespace: "ocel", project: "example-project"}
	if names.Connector() != "ocel-connector" {
		t.Errorf("Connector() = %q, want ocel-connector", names.Connector())
	}
	if names.ConnectorAccountEmail() != "ocel-connector@example-project.iam.gserviceaccount.com" {
		t.Errorf("ConnectorAccountEmail() = %q", names.ConnectorAccountEmail())
	}
	if err := names.connectorFits(); err != nil {
		t.Errorf("connectorFits() = %v, want the default namespace to fit", err)
	}
	long := Names{namespace: "averylongnamespacethatgoeson", project: "example-project"}
	if err := long.connectorFits(); err == nil {
		t.Errorf("connectorFits() on %q = nil, want it refused before IAM does", long.Connector())
	}
}

func TestAnUnsetComputeTakesTheCloudRunServiceThatScalesToNothing(t *testing.T) {
	t.Parallel()

	held, err := connectorComputeOf("")
	if err != nil {
		t.Fatalf("connectorComputeOf(\"\") = %v, want the provider to pick for itself", err)
	}
	if held != providerkit.ComputeServerless {
		t.Errorf("connectorComputeOf(\"\") = %q, want %q", held, providerkit.ComputeServerless)
	}
}

func TestAComputeThisProjectDoesNotRunTheConnectorOnIsRefusedHere(t *testing.T) {
	t.Parallel()

	_, err := connectorComputeOf(providerkit.ComputeContainer)
	if err == nil {
		t.Fatal("connectorComputeOf(container) = nil, want the compute no gcp connector is built for refused by the provider")
	}
	if !strings.Contains(err.Error(), string(providerkit.ComputeContainer)) {
		t.Errorf("err = %v, want it to name the compute it refused", err)
	}
}
