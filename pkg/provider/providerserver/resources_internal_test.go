package providerserver

import (
	"errors"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestAConfigOnAResourceOfAnotherTypeIsRefusedAsInvalid(t *testing.T) {
	t.Parallel()

	realtimeApp := &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME, Name: "app"}
	postgresApp := &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "app"}
	bucketApp := &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "app"}
	kvApp := &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV, Name: "app"}
	for _, tc := range []struct {
		name    string
		message *contractv1.ManifestResource
		says    []string
	}{
		{
			name:    "a postgres config on realtime",
			message: &contractv1.ManifestResource{Resource: realtimeApp, Config: &contractv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{}}},
			says:    []string{"realtime app", "postgres config"},
		},
		{
			name:    "a bucket config on postgres",
			message: &contractv1.ManifestResource{Resource: postgresApp, Config: &contractv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}}},
			says:    []string{"postgres app", "bucket config"},
		},
		{
			name:    "a kv config on a bucket",
			message: &contractv1.ManifestResource{Resource: bucketApp, Config: &contractv1.ManifestResource_Kv{Kv: &resourcesv1.KvConfig{}}},
			says:    []string{"bucket app", "kv config"},
		},
		{
			name:    "a topic config on kv",
			message: &contractv1.ManifestResource{Resource: kvApp, Config: &contractv1.ManifestResource_Topic{Topic: &contractv1.ManifestTopic{}}},
			says:    []string{"kv app", "topic's config"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := manifestResource(tc.message)
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("manifestResource() = %v, want a refusal with code %s", err, refusal.CodeInvalid)
			}
			for _, said := range tc.says {
				if !strings.Contains(err.Error(), said) {
					t.Errorf("manifestResource() = %q, want it to say %q", err, said)
				}
			}
		})
	}
}
