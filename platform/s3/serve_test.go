package s3

import (
	"strings"
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
)

func TestARuntimeWithNoBucketStoreOfItsOwnServesOnlyTheBoundOnes(t *testing.T) {
	none, err := ServeBound(fixedRecords{bindings: []live.Binding{{Name: "main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES}}}, "127.0.0.1:1")
	if err != nil || none.Env != nil {
		t.Fatalf("ServeBound = %+v, %v, want nothing served for a deployment binding no bucket", none, err)
	}

	served, err := ServeBound(fixedRecords{
		bindings: []live.Binding{{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}},
		values: map[string]string{
			"OCEL_RESOURCE_BUCKET_uploads": `{"name":"ocel:bucket.uploads","bucket":{"bucket":"acme","endpoint":"https://abc.r2.cloudflarestorage.com","region":"auto","accessKeyId":"AKID","secretAccessKey":"r2-secret"}}`,
		},
	}, "127.0.0.1:1")
	if err != nil {
		t.Fatalf("ServeBound: %v", err)
	}
	t.Cleanup(func() { _ = served.Close() })
	if len(served.Env) == 0 {
		t.Fatal("ServeBound served nothing, and the app reaches a bound bucket only through the proxy")
	}
	for _, entry := range served.Env {
		if strings.Contains(entry, "r2-secret") {
			t.Errorf("the app is handed %q: the store's key is the proxy's alone", entry)
		}
	}
}
