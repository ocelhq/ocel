package s3

import (
	"strings"
	"testing"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
)

type heldRecords struct {
	bindings []live.Binding
	values   map[string]string
}

func (h heldRecords) Value(key string) string  { return h.values[key] }
func (h heldRecords) Bindings() []live.Binding { return h.bindings }

func TestBackendsAreBuiltForTheBucketsARecordPointsAtAStore(t *testing.T) {
	held := heldRecords{
		bindings: []live.Binding{
			{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET},
			{Name: "avatars", Key: "OCEL_RESOURCE_BUCKET_avatars", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET},
			{Name: "main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES},
		},
		values: map[string]string{
			"OCEL_RESOURCE_BUCKET_uploads": `{"name":"ocel:bucket.uploads","bucket":{"bucket":"acme","endpoint":"https://abc.r2.cloudflarestorage.com","region":"auto","accessKeyId":"AKID","secretAccessKey":"secret"}}`,
			"OCEL_RESOURCE_BUCKET_avatars": `{"name":"bucket--avatars","bucket":{"bucket":"shop-prod-avatars"}}`,
			"OCEL_RESOURCE_POSTGRES_main":  `{"name":"db--main","postgres":{"host":"db"}}`,
		},
	}

	backends, own, err := Backends(held, &recordingPoster{})
	if err != nil {
		t.Fatalf("Backends: %v", err)
	}
	if len(backends) != 1 || !backends[0].holds("acme") {
		t.Fatalf("backends = %d, want one serving acme", len(backends))
	}
	if !own {
		t.Error("own = false, want the bucket the runtime's own backend reaches reported")
	}
}

type arriving struct {
	heldRecords
	arrived bool
}

func (a *arriving) Value(key string) string {
	if !a.arrived {
		return ""
	}
	return a.values[key]
}

func TestARouterOverRecordsServesThoseThatArriveAfterItStarted(t *testing.T) {
	records := &arriving{heldRecords: heldRecords{
		bindings: []live.Binding{{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}},
		values: map[string]string{
			"OCEL_RESOURCE_BUCKET_uploads": `{"name":"ocel:bucket.uploads","bucket":{"bucket":"acme","endpoint":"http://127.0.0.1:1","region":"auto","accessKeyId":"AKID","secretAccessKey":"secret"}}`,
		},
	}}
	router := RouteRecords(&ownBackend{}, records, &recordingPoster{})

	records.arrived = true
	resp, err := router.Sign(t.Context(), &bucketv1.SignRequest{
		Bucket:    "acme",
		Key:       "a.png",
		Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
		Audience:  bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !strings.Contains(resp.GetTarget().GetUrl(), "127.0.0.1:1") {
		t.Errorf("signed url = %q, want it signed for the store the record that arrived names", resp.GetTarget().GetUrl())
	}
}

func TestAnUnreadableBucketRecordIsRefusedWithoutItsValue(t *testing.T) {
	held := heldRecords{
		bindings: []live.Binding{{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}},
		values:   map[string]string{"OCEL_RESOURCE_BUCKET_uploads": `{"bucket": secret-key-here`},
	}
	_, _, err := Backends(held, &recordingPoster{})
	if err == nil {
		t.Fatal("Backends = nil error, want an unreadable record refused")
	}
	if got := err.Error(); len(got) == 0 || strings.Contains(got, "secret-key-here") {
		t.Errorf("error = %q, want the record named and its value kept out", got)
	}
}
