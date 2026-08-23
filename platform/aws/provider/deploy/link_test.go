package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

type recordingPublisher struct {
	slug        string
	environment string
	owner       string
	records     []*linksv1.Link
	err         error

	published map[string][]string
	resolved  map[string]PublishedRecord

	reads            int
	readEnvironment  string
	namesEnvironment string
}

func (p *recordingPublisher) PublishRecords(_ context.Context, slug, environment, owner string, records []*linksv1.Link) error {
	p.slug, p.environment, p.owner, p.records = slug, environment, owner, records
	return p.err
}

func (p *recordingPublisher) PublishedNames(_ context.Context, _, class, environment string) ([]string, error) {
	p.reads++
	p.namesEnvironment = environment
	names := slices.Clone(p.published[class])
	slices.Sort(names)
	return names, nil
}

func (p *recordingPublisher) ResolveRecords(_ context.Context, _, environment string, names []string) ([]PublishedRecord, error) {
	p.readEnvironment = environment
	out := make([]PublishedRecord, 0, len(names))
	for _, name := range names {
		record, ok := p.resolved[name]
		if !ok {
			record, ok = publishedFixtures[name]
		}
		if !ok {
			return nil, fmt.Errorf("link %s is not published: %w", name, values.ErrNotPublished)
		}
		record.Link = proto.Clone(record.Link).(*linksv1.Link)
		record.Link.Name = name
		out = append(out, record)
	}
	return out, nil
}

var publishedFixtures = map[string]PublishedRecord{
	"main": {
		Link: &linksv1.Link{
			Source:     "sst",
			Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{Host: "sst", Port: 5432, Database: "main", Username: "sst", Password: "sst-main"}},
			Grants: []*linksv1.Grant{
				{Label: "connect", Actions: []string{"rds-db:connect"}, Resources: []string{"arn:aws:rds-db:us-east-1:1:dbuser:db-main/app"}},
			},
		},
		Version: 4,
	},
	"uploads": {
		Link: &linksv1.Link{
			Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "sst-uploads"}},
		},
		Version: 1,
	},
}

func sessionConfig() Config {
	cfg := liveConfig()
	cfg.StateTable = "ocel-state"
	cfg.StateTableARN = fixtureStateTableARN
	return cfg
}

func linkedManifest() *contractv1.Manifest {
	return &contractv1.Manifest{
		Slug: "shop",
		Resources: []*contractv1.ManifestResource{
			{
				LogicalName: "db--main",
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main"},
				Config:      &contractv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
			},
			{
				LogicalName: "bucket--uploads",
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_BUCKET, Name: "uploads"},
				Config:      &contractv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
			},
		},
		Apps: []*contractv1.ManifestApp{{Name: "api"}, {Name: "web"}},
		Usages: []*contractv1.ManifestUsage{
			{App: "api", Resource: "bucket--uploads", Files: []string{"apps/api/src/upload.ts"}},
			{App: "api", Resource: "db--main", Files: []string{"apps/api/src/server.ts"}},
			{App: "web", Resource: "db--main", Files: []string{"apps/web/src/page.ts"}},
		},
	}
}

func provisionedLinks() []*linksv1.Link {
	return []*linksv1.Link{
		{
			Name: "db--main",
			Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{
				Username: "app_user", Password: "s3cr3t", Host: "db.host", Port: 5432, Database: "shopdb",
			}},
		},
		{
			Name:       "bucket--uploads",
			Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "shop-uploads-abc123"}},
		},
	}
}

func TestLinkRecords(t *testing.T) {
	t.Run("names each link by its logical name and its typed properties", func(t *testing.T) {
		t.Parallel()
		got := linkRecords(linkedManifest(), provisionedLinks())
		if len(got) != 2 {
			t.Fatalf("linkRecords returned %d records, want one per provisioned link", len(got))
		}
		if got[0].GetName() != "db--main" || naming.LinkTypeOf(got[0]) != linksv1.LinkType_LINK_TYPE_POSTGRES {
			t.Errorf("postgres record = %+v, want it partitioned by logical name under its own type", got[0])
		}
		if got[1].GetName() != "bucket--uploads" || naming.LinkTypeOf(got[1]) != linksv1.LinkType_LINK_TYPE_BUCKET {
			t.Errorf("bucket record = %+v, want it partitioned by logical name under its own type", got[1])
		}
	})

	t.Run("the bucket record carries the bucket and nothing ambient", func(t *testing.T) {
		t.Parallel()
		got := linkRecords(linkedManifest(), provisionedLinks())
		if got[1].GetBucket().GetBucket() != "shop-uploads-abc123" {
			t.Errorf("bucket = %q, want the provisioned bucket", got[1].GetBucket().GetBucket())
		}
		if got[1].GetSource() != "" {
			t.Errorf("source = %q, want ocel's own provisioning left unsourced", got[1].GetSource())
		}
	})

	t.Run("hands back the producer's links unchanged", func(t *testing.T) {
		t.Parallel()
		links := provisionedLinks()
		got := linkRecords(linkedManifest(), links)
		for i, l := range links {
			if !proto.Equal(got[i], l) {
				t.Errorf("record %d = %+v, want the provisioned link %+v", i, got[i], l)
			}
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
		if publisher.records[0].GetPostgres().GetPassword() != "s3cr3t" {
			t.Errorf("postgres record = %+v, want the credential delivered through the store", publisher.records[0])
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
		if err := publishLinkRecords(context.Background(), liveConfig(), &contractv1.Manifest{Slug: "shop"}, nil); err != nil {
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

func TestAppLinks(t *testing.T) {
	t.Parallel()
	got := appLinks(linkedManifest(), "api", nil)
	want := []live.Link{
		{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: linksv1.LinkType_LINK_TYPE_POSTGRES},
		{Name: "bucket--uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: linksv1.LinkType_LINK_TYPE_BUCKET},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("appLinks = %+v, want %+v — the addresses and the shape this app was built to read come from the manifest, before anything is provisioned", got, want)
	}

	if got := appLinks(linkedManifest(), "web", nil); !reflect.DeepEqual(got, want[:1]) {
		t.Errorf("appLinks(web) = %+v, want %+v: an app is handed an address only for what its usage edges name", got, want[:1])
	}
	if got := appLinks(linkedManifest(), "cron", nil); len(got) != 0 {
		t.Errorf("appLinks(cron) = %+v, want nothing for an app carrying no usage edge at all", got)
	}
}

func TestPublishedRecordsMeetWhatTheManifestDeclares(t *testing.T) {
	t.Parallel()
	links := appLinks(linkedManifest(), "api", nil)
	published, err := publishedRecords(t, links)
	if err != nil {
		t.Fatalf("publishedRecords: %v", err)
	}

	if err := live.Conform(links, published); err != nil {
		t.Fatalf("what this deploy publishes drifts from what it tells the app to expect: %v", err)
	}
	for _, l := range links {
		link, err := providerkit.DecodeLink([]byte(published[l.Key]))
		if err != nil {
			t.Fatalf("link %s conformed to %q, which no app can parse: %v", l.Name, published[l.Key], err)
		}
		if got := naming.LinkTypeOf(link); got != l.Type {
			t.Errorf("link %s delivers a %s, want the %s the app was built to read", l.Name, got, l.Type)
		}
	}
}

func TestLinkedAppRendersNoCredential(t *testing.T) {
	t.Parallel()
	app := &contractv1.ManifestApp{Name: "api"}
	bundle, err := renderAppBundle(liveConfig(), "shop", app, appLinks(linkedManifest(), "api", nil))
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
	env := appEnv(linkedManifest(), app, bundle, sessionConfig(), fixtureSessions)
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

func TestDeliveryScopesToTheAppsThatUseTheResource(t *testing.T) {
	t.Parallel()
	cfg := sessionConfig()
	cfg.Slug = "shop"
	manifest := linkedManifest()
	bundles, err := renderAppBundles(cfg, manifest, nil)
	if err != nil {
		t.Fatalf("renderAppBundles: %v", err)
	}

	envs := map[string]map[string]string{}
	addresses := map[string][]string{}
	for _, app := range manifest.GetApps() {
		bundle := bundles[app.GetName()]
		envs[app.GetName()] = appEnv(manifest, app, bundle, cfg, fixtureSessions)
		parsed, err := live.Parse(bundle.Live)
		if err != nil {
			t.Fatalf("parse %s's live manifest: %v", app.GetName(), err)
		}
		for _, l := range parsed.Links {
			addresses[app.GetName()] = append(addresses[app.GetName()], l.Key)
		}
	}

	if want := []string{"OCEL_RESOURCE_POSTGRES_main", "OCEL_RESOURCE_BUCKET_uploads"}; !slices.Equal(addresses["api"], want) {
		t.Errorf("api reads %v, want %v", addresses["api"], want)
	}
	if want := []string{"OCEL_RESOURCE_POSTGRES_main"}; !slices.Equal(addresses["web"], want) {
		t.Errorf("web reads %v, want %v: web uses the shared postgres and never the bucket", addresses["web"], want)
	}

	if got := envs["api"][envStateTable]; got != cfg.StateTable {
		t.Errorf("api's env carries %s=%q, want %q so the membrane it brings up can keep the bucket's sessions", envStateTable, got, cfg.StateTable)
	}
	if got := envs["api"][envSessionPrefix]; got != fixtureSessions.KeyPrefix {
		t.Errorf("api's env carries %s=%q, want %q so the membrane writes only under this deployment's scope", envSessionPrefix, got, fixtureSessions.KeyPrefix)
	}
	if _, ok := envs["web"][envStateTable]; ok {
		t.Errorf("web's env names the state table for a membrane it never brings up")
	}

	for key, value := range envs["web"] {
		if strings.Contains(key, "BUCKET") || strings.Contains(value, "uploads") {
			t.Errorf("web's env carries %s=%q for a bucket it never uses", key, value)
		}
	}
	if strings.Contains(string(bundles["web"].Live), "bucket--uploads") {
		t.Error("web packages the bucket's address; a compromise of web must expose no credential it never needed")
	}

	for _, app := range []string{"web", "api"} {
		raw, err := varsReadPolicy(appExecutionRole(cfg, app, nil, nil, bundles[app], nil, nil, false, nil))
		if err != nil {
			t.Fatalf("varsReadPolicy: %v", err)
		}
		if !strings.Contains(raw, partitionOf(t, "shop")) {
			t.Errorf("%s's role cannot reach its own project's values: %s", app, raw)
		}
		if strings.Contains(raw, partitionOf(t, "other")) {
			t.Errorf("%s's role reaches another project's values: %s", app, raw)
		}
	}
}

func publishedRecords(t *testing.T, links []live.Link) (map[string]string, error) {
	t.Helper()
	records := linkRecords(linkedManifest(), provisionedLinks())
	keys := make(map[string]string, len(links))
	for _, l := range links {
		keys[l.Name] = l.Key
	}
	out := make(map[string]string, len(records))
	for _, r := range records {
		encoded, err := providerkit.EncodeLink(r)
		if err != nil {
			return nil, err
		}
		out[keys[r.GetName()]] = string(encoded)
	}
	return out, nil
}

func publishedProperties(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, r := range linkRecords(linkedManifest(), provisionedLinks()) {
		for _, name := range naming.LinkPropertyNames(r) {
			value, _ := naming.LinkProperty(r, name)
			out = append(out, fmt.Sprint(value))
		}
	}
	if len(out) == 0 {
		t.Fatal("the links published nothing, so nothing was proven about what stays out of the artifact")
	}
	return out
}

func TestVarsReadPolicyReachesOneValuePartitionPerProject(t *testing.T) {
	t.Parallel()
	raw, err := varsReadPolicy(executionRole{
		VarsKeyARN:     productionVarsKeyARN,
		ValuesTableARN: valuesTableARN,
		Slug:           "shop",
		VarsClass:      varsClass,
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
	if want := []string{partitionOf(t, "shop")}; !slices.Equal(leading, want) {
		t.Errorf("LeadingKeys = %v, want %v — a project's values and its links share one partition, and a role reaches its own", leading, want)
	}
}
