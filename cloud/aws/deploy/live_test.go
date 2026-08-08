package deploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/aws/vars"
	"github.com/ocelhq/ocel/cloud/aws/vars/baked"
	"github.com/ocelhq/ocel/cloud/aws/vars/live"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

const (
	varsTable    = "ocel-vars"
	varsTableARN = "arn:aws:dynamodb:us-east-1:1234:table/ocel-vars"
	varsClass    = "production"
)

// liveConfig is a deploy whose substrate has a variable store, which is what a
// live-class value needs to be deliverable at all.
func liveConfig() Config {
	return Config{
		VarsKeyARN:   productionVarsKeyARN,
		VarsTable:    varsTable,
		VarsTableARN: varsTableARN,
		VarsClass:    varsClass,
	}
}

func scopedVariable(key, folder string, class resourcesv1.VariableClass) *deploymentsv1.ManifestVariable {
	return &deploymentsv1.ManifestVariable{Key: key, Class: class, Folder: folder}
}

// TestRenderAppBundle_LiveValuesArePinnedByCoordinateAndNeverBaked proves the
// live class is delivered as an address and the encrypted-baked class as
// ciphertext, from the same render. A live key contributes a coordinate to the
// manifest and nothing to the sealed bundle, which is what makes possession of
// the artifact disclose where the value lives rather than what it is.
func TestRenderAppBundle_LiveValuesArePinnedByCoordinateAndNeverBaked(t *testing.T) {
	app := &deploymentsv1.ManifestApp{
		Name: "web",
		Variables: []*deploymentsv1.ManifestVariable{
			variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			variable("STRIPE_API_KEY", "sk-live", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE),
			scopedVariable("SESSION_SECRET", "/web", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
			scopedVariable("DB_PASSWORD", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		},
	}

	bundle, err := renderAppBundle(liveConfig(), "shop", app)
	if err != nil {
		t.Fatalf("renderAppBundle: %v", err)
	}

	manifest, err := live.Parse(bundle.Live)
	if err != nil {
		t.Fatalf("parse the live manifest: %v", err)
	}
	if manifest.Slug != "shop" || manifest.Table != varsTable || manifest.KeyARN != productionVarsKeyARN || manifest.Class != varsClass {
		t.Errorf("manifest = %+v, want the substrate's own store", manifest)
	}
	want := []live.Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}}
	got := slices.Clone(manifest.Keys)
	slices.SortFunc(got, func(a, b live.Key) int { return strings.Compare(a.Key, b.Key) })
	if !slices.Equal(got, want) {
		t.Errorf("manifest keys = %+v, want %+v", got, want)
	}

	key := decodeEnvelope(t, bundle.Envelope)
	values, err := baked.Open(key, bundle.Ciphertext)
	if err != nil {
		t.Fatalf("open the sealed bundle: %v", err)
	}
	if _, ok := values["SESSION_SECRET"]; ok {
		t.Error("a live value was baked into the bundle")
	}
	if got := values["STRIPE_API_KEY"]; got != "sk-live" {
		t.Errorf("STRIPE_API_KEY = %q, want the sensitive value still baked", got)
	}
}

// TestRenderAppBundle_APreviewPinsItsOwnEnvironmentAndProductionPinsNone is
// where the override axis is decided. A preview's functions resolve overrides
// for the environment they are, so the deploy states which one that is;
// production has a single environment and pins nothing, which is what keeps its
// runtime reading one cell per key.
func TestRenderAppBundle_APreviewPinsItsOwnEnvironmentAndProductionPinsNone(t *testing.T) {
	app := &deploymentsv1.ManifestApp{
		Name:      "web",
		Variables: []*deploymentsv1.ManifestVariable{scopedVariable("DB_PASSWORD", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET)},
	}

	for name, tc := range map[string]struct {
		cfg  Config
		want string
	}{
		"a preview": {cfg: previewOf(liveConfig(), "pr-42"), want: "pr-42"},
		"production": {cfg: func() Config {
			cfg := liveConfig()
			cfg.Class, cfg.Identity = deploymentsv1.Environment_CLASS_PRODUCTION, "prod"
			return cfg
		}()},
	} {
		t.Run(name, func(t *testing.T) {
			bundle, err := renderAppBundle(tc.cfg, "shop", app)
			if err != nil {
				t.Fatalf("renderAppBundle: %v", err)
			}
			manifest, err := live.Parse(bundle.Live)
			if err != nil {
				t.Fatalf("parse the live manifest: %v", err)
			}
			if manifest.Environment != tc.want {
				t.Errorf("environment = %q, want %q", manifest.Environment, tc.want)
			}
		})
	}
}

// previewOf is the same substrate deployed as one named preview environment.
func previewOf(cfg Config, identity string) Config {
	cfg.Class, cfg.Identity = deploymentsv1.Environment_CLASS_PREVIEW, identity
	return cfg
}

// TestRenderAppBundle_ALiveValueNeedsTheSubstratesStore proves a deploy that
// cannot say where the store is refuses rather than shipping a manifest the
// runtime will fail to use. The failure names the app and the key, because in
// the sandbox it would name neither.
func TestRenderAppBundle_ALiveValueNeedsTheSubstratesStore(t *testing.T) {
	app := &deploymentsv1.ManifestApp{
		Name:      "web",
		Variables: []*deploymentsv1.ManifestVariable{scopedVariable("DB_PASSWORD", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET)},
	}

	if _, err := renderAppBundle(Config{VarsKeyARN: productionVarsKeyARN}, "shop", app); err == nil {
		t.Fatal("renderAppBundle accepted a live value with no store to read it from")
	}
}

// TestRenderAppBundle_AnAppWithNoLiveValuesShipsNoManifest proves the file is
// absent rather than empty for an app that declares none. That absence is what
// a store outage is confined by: no manifest means no client, no credentials
// and no call at all.
func TestRenderAppBundle_AnAppWithNoLiveValuesShipsNoManifest(t *testing.T) {
	app := &deploymentsv1.ManifestApp{
		Name:      "web",
		Variables: []*deploymentsv1.ManifestVariable{variable("POSTHOG_ID", "ph", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN)},
	}

	bundle, err := renderAppBundle(liveConfig(), "shop", app)
	if err != nil {
		t.Fatalf("renderAppBundle: %v", err)
	}
	if len(bundle.Live) != 0 {
		t.Errorf("Live = %q, want nothing", bundle.Live)
	}
	if _, ok := bundle.overlay()[live.FilePath]; ok {
		t.Error("an app with no live values still carries a live manifest file")
	}
}

// TestAppBundle_OverlaysTheLiveManifestBesideTheCiphertext proves both
// deliveries ride in the same package, each at the one path its reader knows.
func TestAppBundle_OverlaysTheLiveManifestBesideTheCiphertext(t *testing.T) {
	bundle := appBundle{Envelope: "e", Ciphertext: []byte("sealed"), Live: []byte(`{"slug":"shop"}`)}

	overlay := bundle.overlay()
	if len(overlay) != 2 {
		t.Fatalf("overlay = %v, want the ciphertext and the live manifest", overlay)
	}
	if got := overlay[baked.FilePath]; !bytes.Equal(got, []byte("sealed")) {
		t.Errorf("overlay[%q] = %q, want the sealed bytes", baked.FilePath, got)
	}
	if got := overlay[live.FilePath]; !bytes.Equal(got, []byte(`{"slug":"shop"}`)) {
		t.Errorf("overlay[%q] = %q, want the live manifest", live.FilePath, got)
	}
}

// TestAppBundle_ALiveOnlyAppStillPackagesItsManifest proves a bundle whose only
// content is a live manifest is not mistaken for the zero bundle. An app that
// declares nothing but live values bakes no ciphertext, so a render that keyed
// on the envelope alone would drop its manifest and leave it with no addresses
// at runtime.
func TestAppBundle_ALiveOnlyAppStillPackagesItsManifest(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		Slug: "shop",
		Apps: []*deploymentsv1.ManifestApp{{
			Name:      "web",
			Variables: []*deploymentsv1.ManifestVariable{scopedVariable("DB_PASSWORD", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET)},
		}},
	}

	bundles, err := renderAppBundles(liveConfig(), manifest)
	if err != nil {
		t.Fatalf("renderAppBundles: %v", err)
	}
	bundle, ok := bundles["web"]
	if !ok {
		t.Fatal("an app whose only variables are live gained no bundle at all")
	}
	if bundle.Envelope != "" || len(bundle.Ciphertext) != 0 {
		t.Errorf("bundle = %+v, want no sealed half", bundle)
	}
	if _, ok := bundle.overlay()[live.FilePath]; !ok {
		t.Error("the live manifest is not in the package")
	}
	if len(bundle.env()) != 0 {
		t.Errorf("env = %v, want a live value to cost the function configuration nothing", bundle.env())
	}
}

// TestVarsReadPolicy_ScopesTheTableGrantToTheProjectsOwnPartition proves the
// runtime's read grant reaches one project's values in one class and nothing
// else. The table is account-global and shared by every project, so an
// unconditioned grant would let any function read every project's ciphertext;
// the condition is built from the same function that builds the key it
// constrains, so the two cannot drift.
func TestVarsReadPolicy_ScopesTheTableGrantToTheProjectsOwnPartition(t *testing.T) {
	raw, err := varsReadPolicy(executionRole{VarsKeyARN: productionVarsKeyARN, VarsTableARN: varsTableARN, Slug: "shop", VarsClass: varsClass})
	if err != nil {
		t.Fatalf("varsReadPolicy: %v", err)
	}

	var doc struct {
		Statement []struct {
			Effect    string   `json:"Effect"`
			Action    []string `json:"Action"`
			Resource  string   `json:"Resource"`
			Condition map[string]map[string][]string
		}
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v", err)
	}
	if len(doc.Statement) != 2 {
		t.Fatalf("got %d statements, want the decrypt grant and the table read", len(doc.Statement))
	}

	table := doc.Statement[1]
	if table.Resource != varsTableARN {
		t.Errorf("Resource = %q, want the vars table", table.Resource)
	}
	if want := []string{"dynamodb:Query"}; !slices.Equal(table.Action, want) {
		t.Errorf("Action = %v, want %v", table.Action, want)
	}
	leading := table.Condition["ForAllValues:StringEquals"]["dynamodb:LeadingKeys"]
	if want := []string{vars.PartitionKey("shop", varsClass)}; !slices.Equal(leading, want) {
		t.Errorf("LeadingKeys = %v, want %v", leading, want)
	}

	kms := doc.Statement[0]
	if want := []string{"kms:Decrypt"}; !slices.Equal(kms.Action, want) || kms.Resource != productionVarsKeyARN {
		t.Errorf("statement 0 = %+v, want the unchanged decrypt grant", kms)
	}
}

// TestAppExecutionRole_TakesTheTableOnlyForAnAppWithLiveValues proves the
// DynamoDB grant follows the declaration. An app that reads no live value never
// gains the ability to reach the store at all, which is what keeps the store's
// blast radius the set of functions that actually depend on it.
func TestAppExecutionRole_TakesTheTableOnlyForAnAppWithLiveValues(t *testing.T) {
	cfg := liveConfig()
	cfg.Slug = "shop"
	withLive := appExecutionRole(cfg, "shop", "web", nil, appBundle{Live: []byte(`{"slug":"shop"}`)}, nil)
	if withLive.VarsTableARN != varsTableARN {
		t.Errorf("VarsTableARN = %q, want the vars table", withLive.VarsTableARN)
	}
	if withLive.Slug != "shop" || withLive.VarsClass != varsClass {
		t.Errorf("role = %+v, want the partition it may read named", withLive)
	}

	withoutLive := appExecutionRole(cfg, "shop", "admin", nil, appBundle{}, nil)
	if withoutLive.VarsTableARN != "" {
		t.Errorf("VarsTableARN = %q, want no table grant for an app with no live values", withoutLive.VarsTableARN)
	}
	if withoutLive.VarsKeyARN != productionVarsKeyARN {
		t.Errorf("VarsKeyARN = %q, want the decrypt grant every app keeps", withoutLive.VarsKeyARN)
	}
}

// TestVarsReadPolicy_WithoutATableIsTheDecryptGrantAlone proves an app with no
// live values renders exactly the policy it rendered before this class existed.
func TestVarsReadPolicy_WithoutATableIsTheDecryptGrantAlone(t *testing.T) {
	raw, err := varsReadPolicy(executionRole{VarsKeyARN: productionVarsKeyARN})
	if err != nil {
		t.Fatalf("varsReadPolicy: %v", err)
	}
	if strings.Contains(raw, "dynamodb") {
		t.Errorf("policy = %s, want no table grant at all", raw)
	}
}

// TestLiveDelivery_TheArtifactCarriesTheAddressAndNeverTheValue is the property
// the class exists for, asserted over what a deploy actually produces: the
// package holds the coordinate of each live key, the function configuration
// gains nothing for it, and no plaintext is anywhere in either.
func TestLiveDelivery_TheArtifactCarriesTheAddressAndNeverTheValue(t *testing.T) {
	dir := writeTree(t, map[string]string{"src/server.js": "handler"})
	manifest := &deploymentsv1.Manifest{
		Slug: "shop",
		Apps: []*deploymentsv1.ManifestApp{{
			Name:   "web",
			Folder: "/web",
			Variables: []*deploymentsv1.ManifestVariable{
				scopedVariable("DB_PASSWORD", "/web", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
				variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			},
		}},
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_index", ArtifactPath: filepath.Base(dir), App: "web"},
		},
	}
	uploader := &fakeUploader{exists: map[string]bool{}}
	cfg := liveConfig()
	cfg.ArtifactRoot = filepath.Dir(dir)
	cfg.ArtifactBucket = "artifacts"
	cfg.Uploader = uploader

	bundles, err := renderAppBundles(cfg, manifest)
	if err != nil {
		t.Fatalf("renderAppBundles: %v", err)
	}
	if _, err := uploadFunctionArtifacts(context.Background(), cfg, manifest, bundles, nil); err != nil {
		t.Fatalf("uploadFunctionArtifacts: %v", err)
	}
	if len(uploader.puts) != 1 {
		t.Fatalf("uploaded %d artifacts, want 1", len(uploader.puts))
	}

	packaged := readZip(t, []byte(uploader.putBodies[uploader.puts[0]]))
	raw, ok := packaged[live.FilePath]
	if !ok {
		t.Fatalf("package = %v, want the live manifest at %s", packaged, live.FilePath)
	}
	parsed, err := live.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse the packaged manifest: %v", err)
	}
	if !slices.Equal(parsed.Keys, []live.Key{{Key: "DB_PASSWORD", Folder: "/web"}}) {
		t.Errorf("packaged keys = %+v, want DB_PASSWORD at /web", parsed.Keys)
	}

	env := variableEnv(manifest.GetApps()[0])
	if _, ok := env["DB_PASSWORD"]; ok {
		t.Error("a live key reached the function configuration")
	}
	if len(bundles["web"].env()) != 0 {
		t.Errorf("env = %v, want a live value to cost the function configuration nothing", bundles["web"].env())
	}
	for path, contents := range packaged {
		if strings.Contains(contents, "ph-123") {
			t.Errorf("%s in the package carries a plaintext value", path)
		}
	}
}

// decodeEnvelope reads a bundle's data key back out of the one configuration
// entry it travels in.
func decodeEnvelope(t *testing.T, envelope string) []byte {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(envelope)
	if err != nil {
		t.Fatalf("envelope is not base64: %v", err)
	}
	return key
}

// A grant is per function role, so which partitions it reaches is decided by
// the cells that app's own manifest names. A reference another app holds, and
// one held by a cell no runtime reads at all, are partitions this function has
// no business reaching — the project referencing something somewhere is not the
// question.
func TestRenderAppBundle_ReferencesOnlyTheOwnersOfItsOwnLiveValues(t *testing.T) {
	cfg := previewOf(liveConfig(), "pr-42")
	cfg.VarsReferenced = map[vars.Coordinate]string{
		{Slug: "shop", Key: "DB_PASSWORD"}:                                          "platform",
		{Slug: "shop", Folder: "/web", Key: "SESSION_SECRET", Environment: "pr-42"}: "identity",
		{Slug: "shop", Key: "ADMIN_TOKEN"}:                                          "ops",
		{Slug: "shop", Key: "POSTHOG_ID"}:                                           "analytics",
	}

	app := &deploymentsv1.ManifestApp{
		Name: "web",
		Variables: []*deploymentsv1.ManifestVariable{
			variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			scopedVariable("SESSION_SECRET", "/web", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
			scopedVariable("DB_PASSWORD", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		},
	}
	bundle, err := renderAppBundle(cfg, "shop", app)
	if err != nil {
		t.Fatalf("renderAppBundle: %v", err)
	}
	if want := []string{"identity", "platform"}; !slices.Equal(bundle.Referenced, want) {
		t.Errorf("Referenced = %v, want %v: the owners behind this app's live cells, at its own environment and class-wide", bundle.Referenced, want)
	}

	role := appExecutionRole(cfg, "shop", "web", nil, bundle, nil)
	if !slices.Equal(role.VarsReferenced, bundle.Referenced) {
		t.Errorf("role VarsReferenced = %v, want the app's own owners %v", role.VarsReferenced, bundle.Referenced)
	}
	other := appExecutionRole(cfg, "shop", "admin", nil, appBundle{Live: []byte(`{"slug":"shop"}`)}, nil)
	if len(other.VarsReferenced) != 0 {
		t.Errorf("an app reading no reference took %v, want no partition but its own", other.VarsReferenced)
	}
}

// A reference is followed where it is read, so a function resolving one reads
// the owner project's rows. The grant has to cover exactly those partitions and
// no others: without them the store accepts a write the runtime is then denied,
// and with a wildcard the class's isolation is gone.
func TestVarsReadPolicy_ReachesThePartitionsOfTheProjectsThisOneReferences(t *testing.T) {
	raw, err := varsReadPolicy(executionRole{VarsKeyARN: productionVarsKeyARN, VarsTableARN: varsTableARN, Slug: "shop", VarsClass: varsClass, VarsReferenced: []string{"platform", "shop", "billing"}})
	if err != nil {
		t.Fatalf("varsReadPolicy: %v", err)
	}

	var doc struct {
		Statement []struct {
			Condition map[string]map[string][]string
		}
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v", err)
	}

	leading := doc.Statement[1].Condition["ForAllValues:StringEquals"]["dynamodb:LeadingKeys"]
	want := []string{
		vars.PartitionKey("shop", varsClass),
		vars.PartitionKey("platform", varsClass),
		vars.PartitionKey("billing", varsClass),
	}
	if !slices.Equal(leading, want) {
		t.Errorf("LeadingKeys = %v, want %v (its own partition once, plus each project it references)", leading, want)
	}
}
