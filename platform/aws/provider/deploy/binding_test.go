package deploy

import (
	"encoding/json"
	"slices"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/bindings"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
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
	declared := []live.Binding{
		{Name: "db--main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES},
		{Name: "bucket--uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET},
	}
	published, err := publishedRecords(t, declared)
	if err != nil {
		t.Fatalf("publishedRecords: %v", err)
	}

	if err := live.Conform(declared, published); err != nil {
		t.Fatalf("what this deploy publishes drifts from what it tells the app to expect: %v", err)
	}
	for _, l := range declared {
		binding := &bindingsv1.Binding{}
		if err := protojson.Unmarshal([]byte(published[l.Key]), binding); err != nil {
			t.Fatalf("binding %s conformed to %q, which no app can parse: %v", l.Name, published[l.Key], err)
		}
		if got := bindings.TypeOf(binding); got != l.Type {
			t.Errorf("binding %s delivers a %s, want the %s the app was built to read", l.Name, got, l.Type)
		}
	}
}

func publishedRecords(t *testing.T, bindings []live.Binding) (map[string]string, error) {
	t.Helper()
	provisioned := provisionedBindings()
	records := make([]variablestore.StoredBinding, 0, len(bindings))
	for _, l := range bindings {
		at := slices.IndexFunc(provisioned, func(r *bindingsv1.Binding) bool { return r.GetName() == l.Name })
		pair, err := variablestoreserver.BindingPair(variablestore.OwnerOcel, &variablestorev1.PublishedBinding{Binding: provisioned[at]})
		if err != nil {
			return nil, err
		}
		records = append(records, variablestore.StoredBinding{Name: l.Name, Value: pair.Value})
	}
	return live.MergeBindings(nil, bindings, records), nil
}

func TestVariablesReadPolicyReachesOneValuePartitionPerProject(t *testing.T) {
	t.Parallel()
	raw, err := variablesReadPolicy(executionRole{
		VariablesKeyARN: productionVariablesKeyARN,
		ValuesTableARN:  valuesTableARN,
		Slug:            "shop",
		VariablesTier:   variablesTier,
	})
	if err != nil {
		t.Fatalf("variablesReadPolicy: %v", err)
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
