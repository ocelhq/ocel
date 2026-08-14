package deploy

import (
	"context"
	"encoding/json"
	"errors"
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
	values      []vars.LinkValue
	err         error
}

func (p *recordingPublisher) PublishLinks(_ context.Context, slug, environment string, links []vars.LinkValue) (int, error) {
	p.slug, p.environment, p.values = slug, environment, links
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

func TestLinkValues(t *testing.T) {
	t.Run("addresses each link by its own name and the key the app reads", func(t *testing.T) {
		t.Parallel()
		got, err := linkValues(linkedManifest(), provisionedLinks())
		if err != nil {
			t.Fatalf("linkValues: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("linkValues returned %d values, want one per provisioned link", len(got))
		}
		if got[0].Link != "db--main" || got[0].Key != "OCEL_RESOURCE_POSTGRES_main" {
			t.Errorf("postgres value = %+v, want it partitioned by logical name and keyed by the SDK's lookup", got[0])
		}
		if got[1].Link != "bucket--uploads" || got[1].Key != "OCEL_RESOURCE_BUCKET_uploads" {
			t.Errorf("bucket value = %+v, want it partitioned by logical name and keyed by the SDK's lookup", got[1])
		}
	})

	t.Run("the bucket payload leaves the address behind", func(t *testing.T) {
		t.Parallel()
		got, err := linkValues(linkedManifest(), provisionedLinks())
		if err != nil {
			t.Fatalf("linkValues: %v", err)
		}
		var payload map[string]string
		if err := json.Unmarshal([]byte(got[1].Value), &payload); err != nil {
			t.Fatalf("bucket payload is not JSON: %v", err)
		}
		if _, ok := payload["address"]; ok {
			t.Errorf("bucket payload = %s, want the runtime address ambient rather than in the property bag", got[1].Value)
		}
		if payload["bucket"] != "shop-uploads-abc123" {
			t.Errorf("bucket = %q, want the provisioned bucket", payload["bucket"])
		}
	})

	t.Run("refuses a token this provider ships no client for", func(t *testing.T) {
		t.Parallel()
		foreign := []*linksv1.Link{{Name: "db--main", Type: "sst:aws.Postgres", Properties: map[string]string{}}}
		if _, err := linkValues(linkedManifest(), foreign); err == nil {
			t.Fatal("linkValues accepted a foreign token, delivering an app a shape it cannot read")
		}
	})
}

func TestPublishLinkValues(t *testing.T) {
	t.Run("routes every credential through the store, never an env map", func(t *testing.T) {
		t.Parallel()
		publisher := &recordingPublisher{}
		cfg := liveConfig()
		cfg.Links = publisher

		if err := publishLinkValues(context.Background(), cfg, linkedManifest(), provisionedLinks()); err != nil {
			t.Fatalf("publishLinkValues: %v", err)
		}
		if publisher.slug != "shop" {
			t.Errorf("published under %q, want the project's own slug", publisher.slug)
		}
		if len(publisher.values) != 2 {
			t.Fatalf("published %d values, want one per link", len(publisher.values))
		}
		if !strings.Contains(publisher.values[0].Value, "s3cr3t") {
			t.Errorf("postgres value = %q, want the credential delivered through the store", publisher.values[0].Value)
		}
	})

	t.Run("a preview publishes under its own environment", func(t *testing.T) {
		t.Parallel()
		publisher := &recordingPublisher{}
		cfg := previewOf(liveConfig(), "pr-42")
		cfg.Links = publisher

		if err := publishLinkValues(context.Background(), cfg, linkedManifest(), provisionedLinks()); err != nil {
			t.Fatalf("publishLinkValues: %v", err)
		}
		if publisher.environment != "pr-42" {
			t.Errorf("environment = %q, want the preview's own, so a sibling preview's values never shadow it", publisher.environment)
		}
	})

	t.Run("refuses to strand link values when no store is reachable", func(t *testing.T) {
		t.Parallel()
		if err := publishLinkValues(context.Background(), liveConfig(), linkedManifest(), provisionedLinks()); err == nil {
			t.Fatal("publishLinkValues succeeded with no store; the apps would cold-start without their resources")
		}
	})

	t.Run("a project with no links needs no store", func(t *testing.T) {
		t.Parallel()
		if err := publishLinkValues(context.Background(), liveConfig(), &deploymentsv1.Manifest{Slug: "shop"}, nil); err != nil {
			t.Fatalf("publishLinkValues: %v", err)
		}
	})

	t.Run("reports what the store refused", func(t *testing.T) {
		t.Parallel()
		cfg := liveConfig()
		cfg.Links = &recordingPublisher{err: errors.New("table is gone")}
		err := publishLinkValues(context.Background(), cfg, linkedManifest(), provisionedLinks())
		if err == nil || !strings.Contains(err.Error(), "table is gone") {
			t.Errorf("err = %v, want the store's own refusal named", err)
		}
	})
}

func TestManifestLinks(t *testing.T) {
	t.Parallel()
	got := manifestLinks(linkedManifest())
	want := []live.Link{
		{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main"},
		{Name: "bucket--uploads", Key: "OCEL_RESOURCE_BUCKET_uploads"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("manifestLinks = %+v, want %+v — the addresses come from the manifest, before anything is provisioned", got, want)
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
