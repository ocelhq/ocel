package deploy

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

func provisionedBindings() []*bindingsv1.Binding {
	return []*bindingsv1.Binding{
		{
			Name: "db--main",
			Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
				Username: "app_user", Password: "s3cr3t", Host: "db.host", Port: 5432, Database: "shopdb",
			}},
		},
		{
			Name:       "bucket--uploads",
			Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "shop-uploads-abc123"}},
		},
	}
}

func TestPublishedRecordsMeetWhatTheManifestDeclares(t *testing.T) {
	t.Parallel()
	bindings := []live.Binding{
		{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES},
		{Name: "bucket--uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET},
	}
	published, err := publishedRecords(t, bindings)
	if err != nil {
		t.Fatalf("publishedRecords: %v", err)
	}

	if err := live.Conform(bindings, published); err != nil {
		t.Fatalf("what this deploy publishes drifts from what it tells the app to expect: %v", err)
	}
	for _, l := range bindings {
		binding, err := providerkit.DecodeBinding([]byte(published[l.Key]))
		if err != nil {
			t.Fatalf("binding %s conformed to %q, which no app can parse: %v", l.Name, published[l.Key], err)
		}
		if got := naming.BindingTypeOf(binding); got != l.Type {
			t.Errorf("binding %s delivers a %s, want the %s the app was built to read", l.Name, got, l.Type)
		}
	}
}

func publishedRecords(t *testing.T, bindings []live.Binding) (map[string]string, error) {
	t.Helper()
	records := provisionedBindings()
	keys := make(map[string]string, len(bindings))
	for _, l := range bindings {
		keys[l.Name] = l.Key
	}
	out := make(map[string]string, len(records))
	for _, r := range records {
		encoded, err := providerkit.EncodeBinding(r)
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
		t.Errorf("LeadingKeys = %v, want %v — a project's values and its bindings share one partition, and a role reaches its own", leading, want)
	}
}
