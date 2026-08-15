package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

type recordingPublisher struct {
	slug        string
	environment string
	records     []vars.Record
	err         error
}

func (p *recordingPublisher) PublishRecords(_ context.Context, slug, environment string, records []vars.Record) (int, error) {
	p.slug, p.environment, p.records = slug, environment, records
	return 0, p.err
}

func linkedManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		Slug: "shop",
		Resources: []*deploymentsv1.ManifestResource{
			{
				LogicalName: "db--main",
				Resource:    &resourcesv1.ResourceIdentifier{Type: naming.TokenPostgres, Name: "main"},
				Config:      &deploymentsv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
			},
			{
				LogicalName: "bucket--uploads",
				Resource:    &resourcesv1.ResourceIdentifier{Type: naming.TokenBucket, Name: "uploads"},
				Config:      &deploymentsv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
			},
		},
	}
}

func provisionedLinks() []*linksv1.Link {
	return []*linksv1.Link{
		{
			Name: "db--main",
			Type: naming.TokenPostgres,
			Properties: map[string]string{
				"username": "ocel", "password": "s3cr3t", "host": "db.host", "port": "5432", "database": "ocel",
			},
		},
		{
			Name:       "bucket--uploads",
			Type:       naming.TokenBucket,
			Properties: map[string]string{"bucket": "shop-uploads-abc123"},
		},
	}
}

func TestLinkRecords(t *testing.T) {
	t.Run("names each link by its logical name and its type token", func(t *testing.T) {
		t.Parallel()
		got, err := linkRecords(linkedManifest(), provisionedLinks())
		if err != nil {
			t.Fatalf("linkRecords: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("linkRecords returned %d records, want one per provisioned link", len(got))
		}
		if got[0].Name != "db--main" || got[0].Type != naming.TokenPostgres {
			t.Errorf("postgres record = %+v, want it partitioned by logical name under its own token", got[0])
		}
		if got[1].Name != "bucket--uploads" || got[1].Type != naming.TokenBucket {
			t.Errorf("bucket record = %+v, want it partitioned by logical name under its own token", got[1])
		}
	})

	t.Run("the bucket bag leaves the address behind", func(t *testing.T) {
		t.Parallel()
		got, err := linkRecords(linkedManifest(), provisionedLinks())
		if err != nil {
			t.Fatalf("linkRecords: %v", err)
		}
		if got[1].Type != naming.TokenBucket {
			t.Errorf("record type = %q, want the publisher's own token so a consumer can tell what it was handed", got[1].Type)
		}
		if _, ok := got[1].Properties["address"]; ok {
			t.Errorf("bucket bag = %+v, want the runtime address ambient rather than in the property bag", got[1].Properties)
		}
		if got[1].Properties["bucket"] != "shop-uploads-abc123" {
			t.Errorf("bucket = %q, want the provisioned bucket", got[1].Properties["bucket"])
		}
	})

	t.Run("refuses a token this provider ships no client for", func(t *testing.T) {
		t.Parallel()
		foreign := []*linksv1.Link{{Name: "db--main", Type: "sst:aws.Postgres", Properties: map[string]string{}}}
		if _, err := linkRecords(linkedManifest(), foreign); err == nil {
			t.Fatal("linkRecords accepted a foreign token, delivering an app a shape it cannot read")
		}
	})
}

func TestPublishLinkRecords(t *testing.T) {
	t.Run("routes every credential through the store, never an env map", func(t *testing.T) {
		t.Parallel()
		publisher := &recordingPublisher{}
		cfg := liveConfig()
		cfg.Links = publisher

		if err := publishLinkRecords(context.Background(), cfg, linkedManifest(), provisionedLinks()); err != nil {
			t.Fatalf("publishLinkRecords: %v", err)
		}
		if publisher.slug != "shop" {
			t.Errorf("published under %q, want the project's own slug", publisher.slug)
		}
		if len(publisher.records) != 2 {
			t.Fatalf("published %d records, want one per link", len(publisher.records))
		}
		if !strings.Contains(publisher.records[0].Properties["connectionString"], "s3cr3t") {
			t.Errorf("postgres bag = %+v, want the credential delivered through the store", publisher.records[0].Properties)
		}
	})

	t.Run("a preview publishes under its own environment", func(t *testing.T) {
		t.Parallel()
		publisher := &recordingPublisher{}
		cfg := previewOf(liveConfig(), "pr-42")
		cfg.Links = publisher

		if err := publishLinkRecords(context.Background(), cfg, linkedManifest(), provisionedLinks()); err != nil {
			t.Fatalf("publishLinkRecords: %v", err)
		}
		if publisher.environment != "pr-42" {
			t.Errorf("environment = %q, want the preview's own, so a sibling preview's values never shadow it", publisher.environment)
		}
	})

	t.Run("refuses to strand link values when no store is reachable", func(t *testing.T) {
		t.Parallel()
		if err := publishLinkRecords(context.Background(), liveConfig(), linkedManifest(), provisionedLinks()); err == nil {
			t.Fatal("publishLinkRecords succeeded with no store; the apps would cold-start without their resources")
		}
	})

	t.Run("a project with no links needs no store", func(t *testing.T) {
		t.Parallel()
		if err := publishLinkRecords(context.Background(), liveConfig(), &deploymentsv1.Manifest{Slug: "shop"}, nil); err != nil {
			t.Fatalf("publishLinkRecords: %v", err)
		}
	})

	t.Run("reports what the store refused", func(t *testing.T) {
		t.Parallel()
		cfg := liveConfig()
		cfg.Links = &recordingPublisher{err: errors.New("table is gone")}
		err := publishLinkRecords(context.Background(), cfg, linkedManifest(), provisionedLinks())
		if err == nil || !strings.Contains(err.Error(), "table is gone") {
			t.Errorf("err = %v, want the store's own refusal named", err)
		}
	})
}

func TestManifestLinks(t *testing.T) {
	t.Parallel()
	got := manifestLinks(linkedManifest())
	want := []live.Link{
		{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: naming.TokenPostgres, Properties: []string{"connectionString"}},
		{Name: "bucket--uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: naming.TokenBucket, Properties: []string{"bucket"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("manifestLinks = %+v, want %+v — the addresses and the shape this app was built to read come from the manifest, before anything is provisioned", got, want)
	}
}

func TestPublishedRecordsMeetWhatTheManifestDeclares(t *testing.T) {
	t.Parallel()
	links := manifestLinks(linkedManifest())
	published, err := publishedRecords(t, links)
	if err != nil {
		t.Fatalf("linkRecords: %v", err)
	}

	conformed, err := live.Conform(links, published)
	if err != nil {
		t.Fatalf("what this deploy publishes drifts from what it tells the app to expect: %v", err)
	}
	for _, l := range links {
		var properties map[string]string
		if err := json.Unmarshal([]byte(conformed[l.Key]), &properties); err != nil {
			t.Fatalf("link %s conformed to %q, which no app can parse: %v", l.Name, conformed[l.Key], err)
		}
		for _, want := range l.Properties {
			if properties[want] == "" {
				t.Errorf("link %s delivers %v, which carries no %q", l.Name, properties, want)
			}
		}
	}
}

func TestLinkedAppRendersNoCredential(t *testing.T) {
	t.Parallel()
	app := &deploymentsv1.ManifestApp{Name: "web"}
	bundle, err := renderAppBundle(liveConfig(), "shop", app, manifestLinks(linkedManifest()))
	if err != nil {
		t.Fatalf("renderAppBundle: %v", err)
	}

	manifest, err := live.Parse(bundle.Live)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(manifest.Links) != 2 {
		t.Fatalf("live manifest names %d links, want the app's addresses for both", len(manifest.Links))
	}
	for _, l := range manifest.Links {
		if l.Name == "" || l.Key == "" {
			t.Errorf("link %+v is only half an address", l)
		}
	}
	if bundle.Ciphertext != nil || bundle.Envelope != "" {
		t.Errorf("bundle bakes %d bytes for an app whose only values are derived", len(bundle.Ciphertext))
	}
	if want := []string{"bucket--uploads", "db--main"}; !slices.Equal(bundle.Links, want) {
		t.Errorf("bundle.Links = %v, want %v so the role can be scoped to them", bundle.Links, want)
	}

	env := map[string]string{runtimeAddressEnv: deferredRuntimeAddress}
	maps.Copy(env, variableEnv(app))
	maps.Copy(env, bundle.env())
	for _, published := range publishedProperties(t) {
		for key, value := range env {
			if strings.Contains(value, published) {
				t.Errorf("%s = %q, which carries a published link value; a link value is read live, never written into the function configuration", key, value)
			}
		}
		if strings.Contains(string(bundle.Live), published) {
			t.Errorf("the packaged manifest carries %q; the artifact carries the address and never the value", published)
		}
	}
}

func publishedRecords(t *testing.T, links []live.Link) (map[string]string, error) {
	t.Helper()
	records, err := linkRecords(linkedManifest(), provisionedLinks())
	if err != nil {
		return nil, err
	}
	keys := make(map[string]string, len(links))
	for _, l := range links {
		keys[l.Name] = l.Key
	}
	out := make(map[string]string, len(records))
	for _, r := range records {
		out[keys[r.Name]] = live.EncodeRecord(live.Record{Type: r.Type, Properties: r.Properties})
	}
	return out, nil
}

func publishedProperties(t *testing.T) []string {
	t.Helper()
	records, err := linkRecords(linkedManifest(), provisionedLinks())
	if err != nil {
		t.Fatalf("linkRecords: %v", err)
	}
	var out []string
	for _, r := range records {
		for _, property := range r.Properties {
			out = append(out, property)
		}
	}
	if len(out) == 0 {
		t.Fatal("the links published nothing, so nothing was proven about what stays out of the artifact")
	}
	return out
}

func TestVarsReadPolicyScopesEachLink(t *testing.T) {
	t.Parallel()
	raw, err := varsReadPolicy(executionRole{
		VarsKeyARN:   productionVarsKeyARN,
		VarsTableARN: varsTableARN,
		Slug:         "shop",
		VarsClass:    varsClass,
		VarsLinks:    []string{"db--main", "bucket--uploads"},
	})
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
		vars.LinkPartitionKey("shop", varsClass, "db--main"),
		vars.LinkPartitionKey("shop", varsClass, "bucket--uploads"),
	}
	if !slices.Equal(leading, want) {
		t.Errorf("LeadingKeys = %v, want %v — one partition per link is what lets a grant narrow to a link", leading, want)
	}
}
