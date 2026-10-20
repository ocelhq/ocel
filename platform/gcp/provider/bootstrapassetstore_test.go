package gcp

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
)

type assetStoreHarness struct {
	iam     *googletest.IAM
	secrets *googletest.Secrets
	hmac    *hmacServer
	clients *gcloud.Clients
}

func newAssetStoreHarness(t *testing.T) *assetStoreHarness {
	t.Helper()
	h := &assetStoreHarness{iam: appAccountsOnly(), secrets: googletest.NewSecrets(), hmac: &hmacServer{}}
	rest := h.iam.Rest(t)
	h.clients = h.iam.Serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/secrets"):
			h.secrets.Serve(t, w, r)
		case strings.Contains(r.URL.Path, "/hmacKeys"):
			h.hmac.serve(t, w, r)
		default:
			rest(w, r)
		}
	})
	return h
}

func (h *assetStoreHarness) raise(t *testing.T) {
	t.Helper()
	if err := cloudflareorigin.RaiseAssetStore(t.Context(), h.clients, environment.TierProduction, cloudflareKind); err != nil {
		t.Fatalf("raiseAssetStore() = %v", err)
	}
}

func (h *assetStoreHarness) stored(t *testing.T) cloudflareorigin.EdgeCredentials {
	t.Helper()
	credentials, err := cloudflareorigin.ReadEdgeCredentials(t.Context(), h.clients, environment.TierProduction, cloudflareKind)
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

func (h *assetStoreHarness) assetBindings() []string {
	var bound []string
	for _, binding := range projectBindingsOf(h.iam) {
		if strings.HasPrefix(binding, grants.AssetsRole+" ") {
			bound = append(bound, binding)
		}
	}
	return bound
}

func TestBootstrappingAnEdgeThatRunsCodeMintsAnAccountThatReadsTheEdgeBucketAndNothingElse(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)

	h.raise(t)

	account := h.clients.AssetReaderAccount(environment.TierProduction)
	if len(h.iam.Created) != 1 || h.iam.Created[0].AccountId != account {
		t.Fatalf("the bootstrap created %v, want the one account %s", h.iam.Created, account)
	}
	want := grants.AssetsRole + " serviceAccount:" + h.clients.AssetReaderAccountEmail(environment.TierProduction) +
		` resource.name == "projects/_/buckets/ocel-prod-edge-k3x9q2"` +
		` || resource.name.startsWith("projects/_/buckets/ocel-prod-edge-k3x9q2/objects/")`
	if got := h.assetBindings(); !slices.Equal(got, []string{want}) {
		t.Errorf("the project's asset bindings = %q, want exactly %q", got, want)
	}
}

func TestBootstrappingAnEdgeThatRunsCodeKeepsAnHMACKeyOfThatAccountInSecretManager(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)

	h.raise(t)

	ids := h.hmac.active()
	if len(ids) != 1 {
		t.Fatalf("the active HMAC keys = %v, want one", ids)
	}
	if key := h.hmac.keys[ids[0]]; key.ServiceAccountEmail != h.clients.AssetReaderAccountEmail(environment.TierProduction) {
		t.Errorf("the key authenticates %s, want the asset reader", key.ServiceAccountEmail)
	}
	credentials := h.stored(t)
	if credentials.AssetStoreAccessKeyID != ids[0] || credentials.AssetStoreSecretAccessKey != "secret-"+ids[0] {
		t.Errorf("the recorded credential = %q/%q, want the key's access id and secret", credentials.AssetStoreAccessKeyID, credentials.AssetStoreSecretAccessKey)
	}
}

func TestBootstrappingAgainKeepsTheKeyItRecorded(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	h.raise(t)

	h.raise(t)

	if h.hmac.created != 1 {
		t.Errorf("two bootstraps minted %d keys, want the one the first recorded", h.hmac.created)
	}
}

func TestBootstrappingAfterTheSecretWasLostRetiresTheKeyNobodyCanReadAndMintsAnother(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	h.raise(t)
	lost := h.hmac.active()[0]
	credentials := h.stored(t)
	credentials.AssetStoreAccessKeyID, credentials.AssetStoreSecretAccessKey = "", ""
	if err := cloudflareorigin.WriteEdgeCredentials(t.Context(), h.clients, environment.TierProduction, cloudflareKind, credentials); err != nil {
		t.Fatal(err)
	}

	h.raise(t)

	active := h.hmac.active()
	if len(active) != 1 || active[0] == lost {
		t.Errorf("the active keys = %v, want one key and not the lost %s", active, lost)
	}
	if !slices.Contains(h.hmac.deleted, lost) {
		t.Errorf("the deleted keys = %v, want the lost %s among them", h.hmac.deleted, lost)
	}
}

func TestBootstrappingMintsTheNewKeyBeforeItDeletesTheOneTheWorkerStillSignsWith(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	h.raise(t)
	lost := h.hmac.active()[0]
	credentials := h.stored(t)
	credentials.AssetStoreSecretAccessKey = ""
	if err := cloudflareorigin.WriteEdgeCredentials(t.Context(), h.clients, environment.TierProduction, cloudflareKind, credentials); err != nil {
		t.Fatal(err)
	}

	h.raise(t)

	minted := h.stored(t).AssetStoreAccessKeyID
	if want := []string{"create " + lost, "create " + minted, "delete " + lost}; !slices.Equal(h.hmac.events, want) {
		t.Errorf("the HMAC key calls = %v, want %v", h.hmac.events, want)
	}
}

func TestABootstrapThatCannotMintAKeyKeepsTheOneTheWorkerSignsWith(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	h.raise(t)
	signing := h.hmac.active()[0]
	credentials := h.stored(t)
	credentials.AssetStoreSecretAccessKey = ""
	if err := cloudflareorigin.WriteEdgeCredentials(t.Context(), h.clients, environment.TierProduction, cloudflareKind, credentials); err != nil {
		t.Fatal(err)
	}
	h.hmac.refusesCreate = true

	if err := cloudflareorigin.RaiseAssetStore(t.Context(), h.clients, environment.TierProduction, cloudflareKind); err == nil {
		t.Fatal("raiseAssetStore() = nil, want the refused key creation")
	}

	if got := h.hmac.active(); !slices.Equal(got, []string{signing}) {
		t.Errorf("the active keys = %v, want the worker's %s kept", got, signing)
	}
}

func TestABootstrapThatCannotDeleteTheStaleKeyHasRecordedTheNewOne(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	h.raise(t)
	credentials := h.stored(t)
	credentials.AssetStoreSecretAccessKey = ""
	if err := cloudflareorigin.WriteEdgeCredentials(t.Context(), h.clients, environment.TierProduction, cloudflareKind, credentials); err != nil {
		t.Fatal(err)
	}
	h.hmac.refusesDelete = true

	if err := cloudflareorigin.RaiseAssetStore(t.Context(), h.clients, environment.TierProduction, cloudflareKind); err == nil {
		t.Fatal("raiseAssetStore() = nil, want the refused deletion")
	}

	recorded := h.stored(t)
	if recorded.AssetStoreAccessKeyID != "GOOG1E2" || recorded.AssetStoreSecretAccessKey != "secret-GOOG1E2" {
		t.Errorf("the recorded credential = %q/%q, want the new key GOOG1E2", recorded.AssetStoreAccessKeyID, recorded.AssetStoreSecretAccessKey)
	}
}

func TestBootstrappingReplacesAKeyThatWasDeactivated(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	h.raise(t)
	deactivated := h.hmac.active()[0]
	h.hmac.keys[deactivated].State = "INACTIVE"

	h.raise(t)

	active := h.hmac.active()
	if len(active) != 1 || active[0] == deactivated {
		t.Errorf("the active keys = %v, want one key and not the deactivated %s", active, deactivated)
	}
	if got := h.stored(t).AssetStoreAccessKeyID; got != active[0] {
		t.Errorf("the recorded key = %s, want the new %s", got, active[0])
	}
}

func TestBootstrappingKeepsTheCredentialOfTheWorkerItAdopted(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	if err := cloudflareorigin.WriteEdgeCredentials(t.Context(), h.clients, environment.TierProduction, cloudflareKind,
		cloudflareorigin.EdgeCredentials{ReleasesStore: "c1"}); err != nil {
		t.Fatal(err)
	}

	h.raise(t)

	if got := h.stored(t); got.ReleasesStore != "c1" {
		t.Errorf("the recorded credentials = %+v, want the worker's credential kept beside the asset key", got)
	}
}

func TestTakingTheAssetStoreDownDeletesItsKeyItsGrantAndItsAccount(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	h.raise(t)
	minted := h.hmac.active()[0]

	if err := cloudflareorigin.TakeAssetStore(t.Context(), h.clients, environment.TierProduction); err != nil {
		t.Fatalf("takeAssetStore() = %v", err)
	}

	if !slices.Equal(h.hmac.deleted, []string{minted}) || len(h.hmac.active()) != 0 {
		t.Errorf("the deleted keys = %v, want %s", h.hmac.deleted, minted)
	}
	if got := h.assetBindings(); len(got) != 0 {
		t.Errorf("the project's asset bindings = %q, want none", got)
	}
	if len(h.iam.DeletedAccounts) != 1 || !strings.HasSuffix(h.iam.DeletedAccounts[0], h.clients.AssetReaderAccountEmail(environment.TierProduction)) {
		t.Errorf("the deleted accounts = %v, want the asset reader", h.iam.DeletedAccounts)
	}
}

func TestTakingTheAssetStoreDownWhereNothingWasRaisedIsNotAnError(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)

	if err := cloudflareorigin.TakeAssetStore(t.Context(), h.clients, environment.TierProduction); err != nil {
		t.Errorf("takeAssetStore() = %v, want nothing to take down", err)
	}
}

func TestAnEdgeThatRunsCodePlansTheAccountAndKeyItsBootstrapRaises(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)

	changes, err := cloudflareorigin.PlannedAssetStore(t.Context(), h.clients, environment.TierProduction, cloudflareKind)
	if err != nil {
		t.Fatalf("plannedAssetStore() = %v", err)
	}
	for _, change := range changes {
		if change.Action != provider.ActionCreate {
			t.Errorf("%s %s is planned to %s, want create where nothing exists", change.Kind, change.Name, change.Action)
		}
	}
	if len(changes) != 2 {
		t.Fatalf("the plan = %+v, want the account and the key", changes)
	}

	h.raise(t)
	changes, err = cloudflareorigin.PlannedAssetStore(t.Context(), h.clients, environment.TierProduction, cloudflareKind)
	if err != nil {
		t.Fatalf("plannedAssetStore() = %v", err)
	}
	for _, change := range changes {
		if change.Action != provider.ActionKeep {
			t.Errorf("%s %s is planned to %s, want keep once raised", change.Kind, change.Name, change.Action)
		}
	}
}

func TestACloudflareBootstrapChecksTheHMACKeyPermissionsAndAPlainOneDoesNot(t *testing.T) {
	t.Parallel()
	for _, permission := range bootstrap.AssetStorePermissions {
		if !slices.Contains(bootstrap.PermissionsFor([]string{bootstrap.CloudflareOriginPart}), permission) {
			t.Errorf("a %q bootstrap does not check %s, and the apply would fail when it mints the asset reader's key", bootstrap.CloudflareOriginPart, permission)
		}
		if slices.Contains(bootstrap.PermissionsFor(nil), permission) {
			t.Errorf("a bootstrap that raises no Cloudflare front checks %s, and would refuse a credential for a key it never mints", permission)
		}
	}
}

func frontWithAssetReader(h *assetStoreHarness) (bootstrap.Engine, *frontRegistry) {
	registry := &frontRegistry{front: &countingFront{runsCode: true}}
	return composed(bootstrap.Engine{Edges: registry, Clients: h.clients, Records: fake.NewKeyValues()}), registry
}

func TestRaisingAFrontThatRunsCodeRaisesItsAssetReaderAndDroppingItTakesTheReaderDown(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	b, registry := frontWithAssetReader(h)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.CloudflareOriginPart}}
	recorded := func() cloudflareorigin.EdgeCredentials {
		credentials, err := cloudflareorigin.ReadEdgeCredentials(t.Context(), h.clients, environment.TierProduction, registry.front.Kind())
		if err != nil {
			t.Fatal(err)
		}
		return credentials
	}

	if err := b.RaiseFronts(t.Context(), req, nil); err != nil {
		t.Fatalf("raiseFronts() = %v", err)
	}
	if len(h.hmac.active()) != 1 || recorded().AssetStoreAccessKeyID == "" {
		t.Fatalf("raising the front left the active keys %v and credential %+v, want one key recorded", h.hmac.active(), recorded())
	}

	if err := b.TearFronts(t.Context(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart}); err != nil {
		t.Fatalf("tearFronts() = %v", err)
	}
	if len(h.hmac.keys) != 0 || len(h.iam.DeletedAccounts) != 1 {
		t.Errorf("tearing the front down left the keys %v and deleted the accounts %v, want no key and the asset reader deleted", h.hmac.keys, h.iam.DeletedAccounts)
	}
	if got := recorded(); got != (cloudflareorigin.EdgeCredentials{}) {
		t.Errorf("tearing the front down left the credentials %+v, want none", got)
	}
}

func TestRaisingAFrontThatRunsNoCodeRaisesNoAssetReader(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	b, registry := frontWithAssetReader(h)
	registry.front.runsCode = false
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.CloudflareOriginPart}}

	if err := b.RaiseFronts(t.Context(), req, nil); err != nil {
		t.Fatalf("raiseFronts() = %v", err)
	}

	if h.hmac.created != 0 || len(h.iam.Created) != 0 {
		t.Errorf("a front that runs no code minted %d keys and created %v", h.hmac.created, h.iam.Created)
	}
}

func TestAPlanOfAFrontThatRunsCodeNamesItsAssetReader(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	b, _ := frontWithAssetReader(h)

	groups, err := b.PlannedFronts(t.Context(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart})
	if err != nil {
		t.Fatalf("plannedFronts() = %v", err)
	}
	if len(groups) != 1 || len(groups[0].Changes) != 2 || groups[0].Action != provider.ActionCreate {
		t.Fatalf("the plan = %+v, want one edge group that creates the reader's account and key", groups)
	}

	removed, err := b.RemovedFronts(t.Context(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart})
	if err != nil {
		t.Fatalf("removedFronts() = %v", err)
	}
	if len(removed) != 1 || removed[0].Action != provider.ActionDelete {
		t.Errorf("the removal plan = %+v, want one edge group that deletes", removed)
	}
}
