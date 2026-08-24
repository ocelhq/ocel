package deploy

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

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

func TestPublishedRecordsMeetWhatTheManifestDeclares(t *testing.T) {
	t.Parallel()
	links := []live.Link{
		{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: linksv1.LinkType_LINK_TYPE_POSTGRES},
		{Name: "bucket--uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: linksv1.LinkType_LINK_TYPE_BUCKET},
	}
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

func publishedRecords(t *testing.T, links []live.Link) (map[string]string, error) {
	t.Helper()
	records := provisionedLinks()
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
