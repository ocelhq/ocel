package gcp

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"google.golang.org/api/cloudresourcemanager/v1"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/connectorkit"
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
	only := databaseCondition("example-project", "ocel")
	bindings, changed := boundMember(nil, connectorRecordsRole, member, only, true)
	if !changed || len(bindings) != 1 || !slices.Contains(bindings[0].Members, member) {
		t.Fatalf("granting on an empty policy = %+v, %v", bindings, changed)
	}
	if bindings[0].Condition == nil ||
		bindings[0].Condition.Expression != `resource.name == "projects/example-project/databases/ocel"` {
		t.Fatalf("the grant carries %+v, and an unconditional roles/datastore.user reaches every database in the project",
			bindings[0].Condition)
	}
	if _, again := boundMember(bindings, connectorRecordsRole, member, only, true); again {
		t.Error("granting twice rewrote the policy, so every install would churn the project's IAM")
	}

	bindings, changed = boundMember(bindings, connectorRecordsRole, member, only, false)
	if !changed || slices.Contains(bindings[0].Members, member) {
		t.Errorf("revoking left %+v, want the connector off the binding", bindings)
	}
	if _, again := boundMember(bindings, connectorRecordsRole, member, only, false); again {
		t.Error("revoking twice rewrote the policy, so rm on a target with no connector would still write IAM")
	}
}

func TestAnotherNamespacesConditionalGrantIsADifferentBinding(t *testing.T) {
	t.Parallel()

	const member = "serviceAccount:ocel-connector@project.iam.gserviceaccount.com"
	shop := databaseCondition("example-project", "shop")
	held := []*cloudresourcemanager.Binding{
		{Role: connectorRecordsRole, Members: []string{member}, Condition: shop},
		{Role: connectorRecordsRole, Members: []string{member}},
	}

	bindings, changed := boundMember(held, connectorRecordsRole, member,
		databaseCondition("example-project", "ocel"), true)
	if !changed || len(bindings) != 3 {
		t.Fatalf("granting for another namespace = %+v, %v, want a third binding of its own", bindings, changed)
	}

	bindings, changed = boundMember(bindings, connectorRecordsRole, member,
		databaseCondition("example-project", "ocel"), false)
	if !changed {
		t.Fatal("revoking changed nothing")
	}
	if !slices.Contains(bindings[0].Members, member) || !slices.Contains(bindings[1].Members, member) {
		t.Errorf("revoking left %+v, and removing one namespace's grant may not touch another's or the unconditional one", bindings)
	}
	if slices.Contains(bindings[2].Members, member) {
		t.Errorf("revoking left %+v, want the connector off its own binding", bindings[2])
	}
}

func TestAnotherMembersGrantSurvivesTheConnectorsRemoval(t *testing.T) {
	t.Parallel()

	const member = "serviceAccount:ocel-connector@project.iam.gserviceaccount.com"
	only := databaseCondition("example-project", "ocel")
	held := []*cloudresourcemanager.Binding{
		{Role: connectorRecordsRole, Members: []string{"user:someone@example.com", member}, Condition: only},
	}
	bindings, changed := boundMember(held, connectorRecordsRole, member, only, false)
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

	held, err := providerkit.ConnectorCompute("", connectorCompute)
	if err != nil {
		t.Fatalf("providerkit.ConnectorCompute(\"\", connectorCompute) = %v, want the provider to pick for itself", err)
	}
	if held != providerkit.ComputeServerless {
		t.Errorf("providerkit.ConnectorCompute(\"\", connectorCompute) = %q, want %q", held, providerkit.ComputeServerless)
	}
}

func TestAComputeThisProjectDoesNotRunTheConnectorOnIsRefusedHere(t *testing.T) {
	t.Parallel()

	_, err := providerkit.ConnectorCompute(providerkit.ComputeContainer, connectorCompute)
	if err == nil {
		t.Fatal("ConnectorCompute(container) = nil, want the compute no gcp connector is built for refused by the provider")
	}
	if !strings.Contains(err.Error(), string(providerkit.ComputeContainer)) {
		t.Errorf("err = %v, want it to name the compute it refused", err)
	}
}

func TestRemovingTheConnectorAttemptsEveryStepAndReportsEveryFailure(t *testing.T) {
	t.Parallel()

	var ran []string
	err := everyStep(
		func() error { ran = append(ran, "service"); return errors.New("the service would not go") },
		func() error { ran = append(ran, "grants"); return nil },
		func() error { ran = append(ran, "account"); return errors.New("the account would not go") },
		func() error { ran = append(ran, "images"); return nil },
	)
	if want := []string{"service", "grants", "account", "images"}; !slices.Equal(ran, want) {
		t.Errorf("removal ran %v, want %v: a step that fails leaves the later ones standing if they are skipped", ran, want)
	}
	if err == nil || !strings.Contains(err.Error(), "the service would not go") || !strings.Contains(err.Error(), "the account would not go") {
		t.Errorf("removal reported %v, want every failure named so the operator knows what still stands", err)
	}
	if err := everyStep(func() error { return nil }, func() error { return nil }); err != nil {
		t.Errorf("a removal every step of which passed reported %v", err)
	}
}

func TestTheConnectorServiceMountsItsKeyOutOfSecretManager(t *testing.T) {
	t.Parallel()

	held, err := serviceOf(serving{
		service: "ocel-connector",
		image:   "example.com/ocel-connector:sha256-abc",
		compute: providerkit.ComputeServerless,
		mounts:  []secretMount{connectorKeyMount("ocel-connector-key")},
	})
	if err != nil {
		t.Fatalf("serviceOf: %v", err)
	}
	if len(held.Template.Volumes) != 1 || held.Template.Volumes[0].Secret == nil {
		t.Fatalf("the connector revision carries volumes %+v, want the one Secret Manager fills", held.Template.Volumes)
	}
	volume := held.Template.Volumes[0]
	if volume.Secret.Secret != "ocel-connector-key" {
		t.Errorf("the key volume reads secret %q, want the connector's own", volume.Secret.Secret)
	}
	if len(volume.Secret.Items) != 1 || volume.Secret.Items[0].Path != connectorKeyFile || volume.Secret.Items[0].Version != "latest" {
		t.Errorf("the key volume exposes %+v, want the latest version as the file the config names", volume.Secret.Items)
	}
	mounts := held.Template.Containers[0].VolumeMounts
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

	held := &run.GoogleCloudRunV2Service{Template: &run.GoogleCloudRunV2RevisionTemplate{
		Containers: []*run.GoogleCloudRunV2Container{{Env: []*run.GoogleCloudRunV2EnvVar{
			{Name: connectorVersionEnv, Value: "0.9.9"},
			{Name: connectorPublicKeyEnv, Value: "cHVibGlj"},
		}}},
	}}
	if got := connectorPublicKeyOf(held); got != "cHVibGlj" {
		t.Errorf("connectorPublicKeyOf = %q, want the key the install stamped on the service", got)
	}
	if got := connectorPublicKeyOf(&run.GoogleCloudRunV2Service{}); got != "" {
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
		t.Errorf("the config carried is %v, want keyPath added and the rest kept", read)
	}
	if _, err := keyPathed([]byte(`[]`), connectorKeyPath); err == nil {
		t.Error("a config that is no object was carried")
	}
}

func TestTheConnectorHoldsOnlyTheKeyRolesItsGrantsCallFor(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		grants []string
		roles  []string
	}{
		"read alone":           {grants: []string{connectorkit.CapabilityEnvVarsRead}},
		"read and write":       {grants: []string{connectorkit.CapabilityEnvVarsRead, connectorkit.CapabilityEnvVarsWrite}, roles: []string{connectorSealingRole}},
		"read and reveal":      {grants: []string{connectorkit.CapabilityEnvVarsRead, connectorkit.CapabilityEnvVarsReveal}, roles: []string{connectorOpeningRole}},
		"every grant there is": {grants: []string{connectorkit.CapabilityEnvVarsRead, connectorkit.CapabilityEnvVarsWrite, connectorkit.CapabilityEnvVarsReveal}, roles: []string{connectorSealingRole, connectorOpeningRole}},
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

func TestAReinstallWithFewerGrantsTakesTheKeyRoleItNoLongerCallsForAway(t *testing.T) {
	t.Parallel()

	const member = "serviceAccount:ocel-connector@project.iam.gserviceaccount.com"
	bindings, changed := boundKeyRoles(nil, member, connectorKeyRoles, []string{connectorSealingRole, connectorOpeningRole})
	if !changed || len(bindings) != 2 {
		t.Fatalf("granting both on an empty policy = %+v, %v", bindings, changed)
	}
	bindings, changed = boundKeyRoles(bindings, member, connectorKeyRoles, []string{connectorSealingRole})
	if !changed {
		t.Fatal("dropping the reveal grant changed nothing on the key")
	}
	for _, binding := range bindings {
		held := slices.Contains(binding.GetMembers(), member)
		if binding.GetRole() == connectorOpeningRole && held {
			t.Errorf("the connector still holds %s after the reveal grant went, and a role nothing calls for is one more than the minimum", connectorOpeningRole)
		}
		if binding.GetRole() == connectorSealingRole && !held {
			t.Errorf("the connector lost %s while its write grant stands", connectorSealingRole)
		}
	}
	if _, again := boundKeyRoles(bindings, member, connectorKeyRoles, []string{connectorSealingRole}); again {
		t.Error("holding the same roles again rewrote the policy, so every install would churn the key's IAM")
	}
	bindings, _ = boundKeyRoles(bindings, member, connectorKeyRoles, nil)
	for _, binding := range bindings {
		if slices.Contains(binding.GetMembers(), member) {
			t.Errorf("removal left the connector holding %s", binding.GetRole())
		}
	}
}

func TestASecretGrantIsAddedOnceAndTakenAwayOnce(t *testing.T) {
	t.Parallel()

	const member = "serviceAccount:ocel-connector@project.iam.gserviceaccount.com"
	bindings, changed := boundSecretMember(nil, connectorKeyRole, member, true)
	if !changed || len(bindings) != 1 || !slices.Contains(bindings[0].Members, member) {
		t.Fatalf("granting on an empty policy = %+v, %v", bindings, changed)
	}
	if _, again := boundSecretMember(bindings, connectorKeyRole, member, true); again {
		t.Error("granting twice rewrote the policy")
	}
	bindings, changed = boundSecretMember(bindings, connectorKeyRole, member, false)
	if !changed || slices.Contains(bindings[0].Members, member) {
		t.Errorf("revoking left %+v", bindings)
	}
	if _, again := boundSecretMember(bindings, connectorKeyRole, member, false); again {
		t.Error("revoking twice rewrote the policy")
	}
}
