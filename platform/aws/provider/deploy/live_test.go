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

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/baked"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

const (
	valuesTable    = "ocel-state"
	valuesTableARN = "arn:aws:dynamodb:us-east-1:1234:table/ocel-state"
	varsClass      = "production"
)

func liveConfig() Config {
	return Config{
		VarsKeyARN:    productionVarsKeyARN,
		StateTable:    valuesTable,
		StateTableARN: valuesTableARN,
		Class:         varsClass,
	}
}

func partitionOf(t *testing.T, slug string) string {
	t.Helper()
	partition, err := valuePartition(slug, varsClass)
	if err != nil {
		t.Fatalf("valuePartition: %v", err)
	}
	return partition
}

func scopedVariable(key, folder string, class resourcesv1.VariableClass) *contractv1.ManifestVariable {
	return &contractv1.ManifestVariable{Key: key, Class: class, Folder: folder}
}

func previewOf(cfg Config, identity string) Config {
	cfg.Class, cfg.Env = providerkit.ClassPreview, identity
	return cfg
}

func decodeEnvelope(t *testing.T, envelope string) []byte {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(envelope)
	if err != nil {
		t.Fatalf("envelope is not base64: %v", err)
	}
	return key
}

func TestRenderAppBundle(t *testing.T) {
	t.Run("live values are pinned by coordinate and never baked", func(t *testing.T) {
		t.Parallel()
		app := &contractv1.ManifestApp{
			Name: "web",
			Variables: []*contractv1.ManifestVariable{
				variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
				variable("STRIPE_API_KEY", "sk-live", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE),
				scopedVariable("SESSION_SECRET", "/web", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
				scopedVariable("DB_PASSWORD", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
			},
		}

		bundle, err := renderAppBundle(liveConfig(), "shop", app, nil)
		if err != nil {
			t.Fatalf("renderAppBundle: %v", err)
		}

		manifest, err := live.Parse(bundle.Live)
		if err != nil {
			t.Fatalf("parse the live manifest: %v", err)
		}
		if manifest.Slug != "shop" || manifest.Table != valuesTable || manifest.KeyARN != productionVarsKeyARN || manifest.Class != varsClass {
			t.Errorf("manifest = %+v, want the bootstrap's own store", manifest)
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
	})

	t.Run("a preview pins its own environment and production pins none", func(t *testing.T) {
		t.Parallel()
		app := &contractv1.ManifestApp{
			Name:      "web",
			Variables: []*contractv1.ManifestVariable{scopedVariable("DB_PASSWORD", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET)},
		}

		production := liveConfig()
		production.Class, production.Env = providerkit.ClassProduction, providerkit.ProductionEnv

		for _, tc := range []struct {
			name string
			cfg  Config
			want string
		}{
			{name: "a preview", cfg: previewOf(liveConfig(), "pr-42"), want: "pr-42"},
			{name: "production", cfg: production},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bundle, err := renderAppBundle(tc.cfg, "shop", app, nil)
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
	})

	t.Run("a live value needs the bootstrap's store", func(t *testing.T) {
		t.Parallel()
		app := &contractv1.ManifestApp{
			Name:      "web",
			Variables: []*contractv1.ManifestVariable{scopedVariable("DB_PASSWORD", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET)},
		}

		if _, err := renderAppBundle(Config{VarsKeyARN: productionVarsKeyARN}, "shop", app, nil); err == nil {
			t.Fatal("renderAppBundle accepted a live value with no store to read it from")
		}
	})

	t.Run("an app with no live values ships no manifest", func(t *testing.T) {
		t.Parallel()
		app := &contractv1.ManifestApp{
			Name:      "web",
			Variables: []*contractv1.ManifestVariable{variable("POSTHOG_ID", "ph", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN)},
		}

		bundle, err := renderAppBundle(liveConfig(), "shop", app, nil)
		if err != nil {
			t.Fatalf("renderAppBundle: %v", err)
		}
		if len(bundle.Live) != 0 {
			t.Errorf("Live = %q, want nothing", bundle.Live)
		}
		if _, ok := bundle.overlay()[live.FilePath]; ok {
			t.Error("an app with no live values still carries a live manifest file")
		}
	})

	t.Run("references only the owners of its own live values", func(t *testing.T) {
		t.Parallel()
		cfg := previewOf(liveConfig(), "pr-42")
		cfg.VarsReferenced = map[values.Coordinate]string{
			{Cell: values.Cell{Key: "DB_PASSWORD"}}:                                          "platform",
			{Cell: values.Cell{Folder: "/web", Key: "SESSION_SECRET"}, Environment: "pr-42"}: "identity",
			{Cell: values.Cell{Key: "ADMIN_TOKEN"}}:                                          "ops",
			{Cell: values.Cell{Key: "POSTHOG_ID"}}:                                           "analytics",
		}

		app := &contractv1.ManifestApp{
			Name: "web",
			Variables: []*contractv1.ManifestVariable{
				variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
				scopedVariable("SESSION_SECRET", "/web", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
				scopedVariable("DB_PASSWORD", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
			},
		}
		bundle, err := renderAppBundle(cfg, "shop", app, nil)
		if err != nil {
			t.Fatalf("renderAppBundle: %v", err)
		}
		if want := []string{"identity", "platform"}; !slices.Equal(bundle.Referenced, want) {
			t.Errorf("Referenced = %v, want %v: the owners behind this app's live cells, at its own environment and class-wide", bundle.Referenced, want)
		}

		role := appExecutionRole(cfg, "web", nil, nil, bundle, nil, nil, false, nil)
		if !slices.Equal(role.VarsReferenced, bundle.Referenced) {
			t.Errorf("role VarsReferenced = %v, want the app's own owners %v", role.VarsReferenced, bundle.Referenced)
		}
		other := appExecutionRole(cfg, "admin", nil, nil, appBundle{Live: []byte(`{"slug":"shop"}`)}, nil, nil, false, nil)
		if len(other.VarsReferenced) != 0 {
			t.Errorf("an app reading no reference took %v, want no partition but its own", other.VarsReferenced)
		}
	})
}

func TestAppBundle(t *testing.T) {
	t.Run("overlays the live manifest beside the ciphertext", func(t *testing.T) {
		t.Parallel()
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
	})

	t.Run("a live-only app still packages its manifest", func(t *testing.T) {
		t.Parallel()
		manifest := &contractv1.Manifest{
			Slug: "shop",
			Apps: []*contractv1.ManifestApp{{
				Name:      "web",
				Variables: []*contractv1.ManifestVariable{scopedVariable("DB_PASSWORD", "", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET)},
			}},
		}

		bundles, err := renderAppBundles(liveConfig(), manifest, nil)
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
	})
}

func TestVarsReadPolicy(t *testing.T) {
	t.Run("scopes the table grant to the project's own partition", func(t *testing.T) {
		t.Parallel()
		raw, err := varsReadPolicy(executionRole{VarsKeyARN: productionVarsKeyARN, ValuesTableARN: valuesTableARN, Slug: "shop", VarsClass: varsClass})
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
		if table.Resource != valuesTableARN {
			t.Errorf("Resource = %q, want the table the values live in", table.Resource)
		}
		if want := []string{"dynamodb:Query"}; !slices.Equal(table.Action, want) {
			t.Errorf("Action = %v, want %v", table.Action, want)
		}
		leading := table.Condition["ForAllValues:StringEquals"]["dynamodb:LeadingKeys"]
		if want := []string{partitionOf(t, "shop")}; !slices.Equal(leading, want) {
			t.Errorf("LeadingKeys = %v, want %v", leading, want)
		}

		kms := doc.Statement[0]
		if want := []string{"kms:Decrypt"}; !slices.Equal(kms.Action, want) || kms.Resource != productionVarsKeyARN {
			t.Errorf("statement 0 = %+v, want the unchanged decrypt grant", kms)
		}
	})

	t.Run("without a table is the decrypt grant alone", func(t *testing.T) {
		t.Parallel()
		raw, err := varsReadPolicy(executionRole{VarsKeyARN: productionVarsKeyARN})
		if err != nil {
			t.Fatalf("varsReadPolicy: %v", err)
		}
		if strings.Contains(raw, "dynamodb") {
			t.Errorf("policy = %s, want no table grant at all", raw)
		}
	})

	t.Run("reaches the partitions of the projects this one references", func(t *testing.T) {
		t.Parallel()
		raw, err := varsReadPolicy(executionRole{VarsKeyARN: productionVarsKeyARN, ValuesTableARN: valuesTableARN, Slug: "shop", VarsClass: varsClass, VarsReferenced: []string{"platform", "shop", "billing"}})
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
			partitionOf(t, "shop"),
			partitionOf(t, "platform"),
			partitionOf(t, "billing"),
		}
		if !slices.Equal(leading, want) {
			t.Errorf("LeadingKeys = %v, want %v (its own partition once, plus each project it references)", leading, want)
		}
	})
}

func TestAppExecutionRoleLiveValues(t *testing.T) {
	t.Run("takes the table only for an app with live values", func(t *testing.T) {
		t.Parallel()
		cfg := liveConfig()
		cfg.Slug = "shop"
		withLive := appExecutionRole(cfg, "web", nil, nil, appBundle{Live: []byte(`{"slug":"shop"}`)}, nil, nil, false, nil)
		if withLive.ValuesTableARN != valuesTableARN {
			t.Errorf("ValuesTableARN = %q, want the table the values live in", withLive.ValuesTableARN)
		}
		if withLive.Slug != "shop" || withLive.VarsClass != varsClass {
			t.Errorf("role = %+v, want the partition it may read named", withLive)
		}

		withoutLive := appExecutionRole(cfg, "admin", nil, nil, appBundle{}, nil, nil, false, nil)
		if withoutLive.ValuesTableARN != "" {
			t.Errorf("ValuesTableARN = %q, want no table grant for an app with no live values", withoutLive.ValuesTableARN)
		}
		if withoutLive.VarsKeyARN != productionVarsKeyARN {
			t.Errorf("VarsKeyARN = %q, want the decrypt grant every app keeps", withoutLive.VarsKeyARN)
		}
	})
}

func TestLiveDelivery(t *testing.T) {
	t.Run("the artifact carries the address and never the value", func(t *testing.T) {
		t.Parallel()
		dir := writeTree(t, map[string]string{"src/server.js": "handler"})
		manifest := &contractv1.Manifest{
			Slug: "shop",
			Apps: []*contractv1.ManifestApp{{
				Name:   "web",
				Folder: "/web",
				Variables: []*contractv1.ManifestVariable{
					scopedVariable("DB_PASSWORD", "/web", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
					variable("POSTHOG_ID", "ph-123", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
				},
			}},
			Functions: []*contractv1.ManifestFunction{
				{LogicalName: "web_index", ArtifactPath: filepath.Base(dir), App: "web"},
			},
		}
		uploader := &fakeUploader{exists: map[string]bool{}}
		cfg := liveConfig()
		cfg.ArtifactRoot = filepath.Dir(dir)
		cfg.ArtifactBucket = "artifacts"
		cfg.Uploader = uploader

		bundles, err := renderAppBundles(cfg, manifest, nil)
		if err != nil {
			t.Fatalf("renderAppBundles: %v", err)
		}
		if _, err := uploadFunctionArtifacts(context.Background(), cfg, manifest, bakedBuilds(t, cfg, manifest, bundles), nil); err != nil {
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
	})
}
