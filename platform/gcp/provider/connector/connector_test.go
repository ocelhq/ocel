package connector

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/connectorserver"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func TestTheConnectorServiceRunsOnOneInstanceAtMostAndIsReachableWithoutIAM(t *testing.T) {
	t.Parallel()

	service, err := run.ServiceOf(run.Serving{
		Service:   "ocel-connector",
		Image:     "example.com/ocel-connector:sha256-abc",
		Account:   "ocel-connector@project.iam.gserviceaccount.com",
		Compute:   provider.ComputeServerless,
		Public:    true,
		Memory:    connectorMemory,
		Instances: provider.Instances{Max: connectorInstances},
		Ingress:   run.IngressEverywhere,
	})
	if err != nil {
		t.Fatalf("serviceOf: %v", err)
	}
	if !service.InvokerIamDisabled {
		t.Error("the connector service checks IAM on an invoke, and the console sends its own token rather than a Google credential")
	}
	if service.Template.Scaling.MinInstanceCount != 0 || service.Template.Scaling.MaxInstanceCount != connectorInstances {
		t.Errorf("the connector scales %+v, want nothing running idle and %d at most", service.Template.Scaling, connectorInstances)
	}
	if service.Template.ServiceAccount != "ocel-connector@project.iam.gserviceaccount.com" {
		t.Errorf("the connector runs as %q, want its own service account", service.Template.ServiceAccount)
	}
	if service.Ingress != run.IngressEverywhere {
		t.Errorf("the connector takes traffic from %q, and the console dials it from outside the project", service.Ingress)
	}
}

func TestAnAppServiceStillScalesAsItDid(t *testing.T) {
	t.Parallel()

	service, err := run.ServiceOf(run.Serving{Service: "app", Image: "example.com/app:tag", Compute: provider.ComputeServerless})
	if err != nil {
		t.Fatalf("serviceOf: %v", err)
	}
	if service.Template.Scaling.MaxInstanceCount != 0 {
		t.Errorf("an app that named no ceiling got one of %d", service.Template.Scaling.MaxInstanceCount)
	}
}

func TestTheConnectorIsNamedForTheNamespaceAndFitsWhatIAMTakes(t *testing.T) {
	t.Parallel()

	named := names.New("ocel", "example-project")
	if named.Connector() != "ocel-connector" {
		t.Errorf("Connector() = %q, want ocel-connector", named.Connector())
	}
	if named.ConnectorAccountEmail() != "ocel-connector@example-project.iam.gserviceaccount.com" {
		t.Errorf("ConnectorAccountEmail() = %q", named.ConnectorAccountEmail())
	}
	if err := named.ConnectorFits(); err != nil {
		t.Errorf("connectorFits() = %v, want the default namespace to fit", err)
	}
	long := names.New("averylongnamespacethatgoeson", "example-project")
	if err := long.ConnectorFits(); err == nil {
		t.Errorf("connectorFits() on %q = nil, want it refused before IAM does", long.Connector())
	}
}

func TestAnUnsetComputeTakesTheCloudRunServiceThatScalesToNothing(t *testing.T) {
	t.Parallel()

	compute, err := provider.ConnectorCompute("", connectorCompute)
	if err != nil {
		t.Fatalf("provider.ConnectorCompute(\"\", connectorCompute) = %v, want the provider to pick for itself", err)
	}
	if compute != provider.ComputeServerless {
		t.Errorf("provider.ConnectorCompute(\"\", connectorCompute) = %q, want %q", compute, provider.ComputeServerless)
	}
}

func TestAComputeThisProjectDoesNotRunTheConnectorOnIsRefusedHere(t *testing.T) {
	t.Parallel()

	_, err := provider.ConnectorCompute(provider.ComputeContainer, connectorCompute)
	if err == nil {
		t.Fatal("ConnectorCompute(container) = nil, want the compute no gcp connector is built for refused by the provider")
	}
	if !strings.Contains(err.Error(), string(provider.ComputeContainer)) {
		t.Errorf("err = %v, want it to name the compute it refused", err)
	}
}

func TestRemovingTheConnectorAttemptsEveryStepAndReportsEveryFailure(t *testing.T) {
	t.Parallel()

	var ran []string
	err := gcloud.EveryStep(
		func() error { ran = append(ran, "service"); return errors.New("the service would not go") },
		func() error { ran = append(ran, "grants"); return nil },
		func() error { ran = append(ran, "account"); return errors.New("the account would not go") },
		func() error { ran = append(ran, "images"); return nil },
	)
	if want := []string{"service", "grants", "account", "images"}; !slices.Equal(ran, want) {
		t.Errorf("removal ran %v, want %v: a step that fails leaves the later ones in place if they are skipped", ran, want)
	}
	if err == nil || !strings.Contains(err.Error(), "the service would not go") || !strings.Contains(err.Error(), "the account would not go") {
		t.Errorf("removal reported %v, want every failure named so the operator knows what still exists", err)
	}
	if err := gcloud.EveryStep(func() error { return nil }, func() error { return nil }); err != nil {
		t.Errorf("a removal every step of which passed reported %v", err)
	}
}

func TestTheConnectorServiceMountsItsKeyOutOfSecretManager(t *testing.T) {
	t.Parallel()

	service, err := run.ServiceOf(run.Serving{
		Service: "ocel-connector",
		Image:   "example.com/ocel-connector:sha256-abc",
		Compute: provider.ComputeServerless,
		Mounts:  []run.SecretMount{connectorKeyMount("ocel-connector-key")},
	})
	if err != nil {
		t.Fatalf("serviceOf: %v", err)
	}
	if len(service.Template.Volumes) != 1 || service.Template.Volumes[0].Secret == nil {
		t.Fatalf("the connector revision has volumes %+v, want the one Secret Manager fills", service.Template.Volumes)
	}
	volume := service.Template.Volumes[0]
	if volume.Secret.Secret != "ocel-connector-key" {
		t.Errorf("the key volume reads secret %q, want the connector's own", volume.Secret.Secret)
	}
	if len(volume.Secret.Items) != 1 || volume.Secret.Items[0].Path != connectorKeyFile || volume.Secret.Items[0].Version != "latest" {
		t.Errorf("the key volume exposes %+v, want the latest version as the file the config names", volume.Secret.Items)
	}
	mounts := service.Template.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].Name != volume.Name || mounts[0].MountPath != connectorKeyDir {
		t.Errorf("the container mounts %+v, want the key volume at %s", mounts, connectorKeyDir)
	}
	if !strings.HasPrefix(connectorKeyPath, connectorKeyDir+"/") {
		t.Errorf("the config names %s while the volume is mounted at %s", connectorKeyPath, connectorKeyDir)
	}
}

func TestTheKeyMintedIntoSecretManagerNamesThePublicKeyTheConsoleVerifiesWith(t *testing.T) {
	t.Parallel()

	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	public, err := publicKeyOf(keyPayload(seed))
	if err != nil {
		t.Fatalf("publicKeyOf: %v", err)
	}
	want := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	if public != want {
		t.Errorf("the install registers %s, and the connector reading the same file signs as %s", public, want)
	}
	if _, err := publicKeyOf([]byte("not a key\n")); err == nil {
		t.Error("a payload that is no key named a public key")
	}
	if _, err := publicKeyOf(keyPayload(seed[:16])); err == nil {
		t.Error("a short seed named a public key")
	}
}

func TestTheConnectorsPublicKeyIsReadOffTheServiceItRunsAs(t *testing.T) {
	t.Parallel()

	service := &runv2.GoogleCloudRunV2Service{Template: &runv2.GoogleCloudRunV2RevisionTemplate{
		Containers: []*runv2.GoogleCloudRunV2Container{{Env: []*runv2.GoogleCloudRunV2EnvVar{
			{Name: connectorVersionEnv, Value: "0.9.9"},
			{Name: connectorPublicKeyEnv, Value: "cHVibGlj"},
		}}},
	}}
	if got := connectorPublicKeyOf(service); got != "cHVibGlj" {
		t.Errorf("connectorPublicKeyOf = %q, want the key the install stamped on the service", got)
	}
	if got := connectorPublicKeyOf(&runv2.GoogleCloudRunV2Service{}); got != "" {
		t.Errorf("connectorPublicKeyOf on a bare service = %q", got)
	}
}

func TestTheConnectorConfigNamesWhereItsKeyIsMounted(t *testing.T) {
	t.Parallel()

	written, err := keyPathed([]byte(`{"console":"https://console.example.com","connectorId":"conn-1"}`), connectorKeyPath)
	if err != nil {
		t.Fatalf("keyPathed: %v", err)
	}
	var read map[string]any
	if err := json.Unmarshal(written, &read); err != nil {
		t.Fatal(err)
	}
	if read["keyPath"] != connectorKeyPath || read["console"] != "https://console.example.com" || read["connectorId"] != "conn-1" {
		t.Errorf("the config written is %v, want keyPath added and the rest kept", read)
	}
	if _, err := keyPathed([]byte(`[]`), connectorKeyPath); err == nil {
		t.Error("a config that is no object was written")
	}
}

func TestTheConnectorHasOnlyTheKeyRolesItsGrantsCallFor(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		grants []string
		roles  []string
	}{
		"read alone":           {grants: []string{connectorserver.CapabilityVariablesRead}},
		"read and write":       {grants: []string{connectorserver.CapabilityVariablesRead, connectorserver.CapabilityVariablesWrite}, roles: []string{connectorSealingRole}},
		"read and reveal":      {grants: []string{connectorserver.CapabilityVariablesRead, connectorserver.CapabilityVariablesReveal}, roles: []string{connectorOpeningRole}},
		"every grant there is": {grants: []string{connectorserver.CapabilityVariablesRead, connectorserver.CapabilityVariablesWrite, connectorserver.CapabilityVariablesReveal}, roles: []string{connectorSealingRole, connectorOpeningRole}},
		"no grant at all":      {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := keyRolesFor(tc.grants); !slices.Equal(got, tc.roles) {
				t.Errorf("keyRolesFor(%v) = %v, want %v: a read never opens a sealed value, a write seals one, a reveal opens one",
					tc.grants, got, tc.roles)
			}
		})
	}
}

func TestTheConnectorServiceNamesTheAccountItAdmitsHeartbeatsFrom(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	served := buildConnectorServing(names, "europe-west1", "example.com/ocel-connector:sha256-abc", "0.9.9", "cHVibGlj", []byte("{}\n"))
	if got := served.Env[ports.ConnectorAccountEnvVar]; got != names.ConnectorAccountEmail() {
		t.Errorf("the connector reads %s = %q, want %s, the account its schedule's ID token is minted for", ports.ConnectorAccountEnvVar, got, names.ConnectorAccountEmail())
	}
	if served.Account != names.ConnectorAccountEmail() {
		t.Errorf("the connector runs as %q, want its own account", served.Account)
	}
}
