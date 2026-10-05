package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	variables "github.com/ocelhq/ocel/platform/gcp/provider/live"
)

const appBucket = "ocel-shop-prod-uploads-0dd909b8"

func TestTheRuntimeServesTheCloudStorageBucketsTheDeploymentBinds(t *testing.T) {
	t.Parallel()

	var asked []string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.URL.EscapedPath())
		if r.URL.EscapedPath() != "/storage/v1/b/"+appBucket+"/o/report.pdf" {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"message":"No such object"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"bucket":"` + appBucket + `","name":"report.pdf","size":"7","contentType":"application/pdf","generation":"42","timeCreated":"2026-10-05T12:00:00Z"}`))
	}))
	t.Cleanup(storage.Close)

	record, err := protojson.Marshal(&bindingsv1.Binding{Name: "uploads", Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: appBucket}}})
	if err != nil {
		t.Fatal(err)
	}
	bindings := []live.Binding{{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}}
	values := live.New(recordedValues{"OCEL_RESOURCE_BUCKET_uploads": string(record)}, []string{"OCEL_RESOURCE_BUCKET_uploads"}, bindings, nil)
	if err := values.Join(values.Prefetch(context.Background())); err != nil {
		t.Fatalf("Prefetch() = %v", err)
	}
	served, err := serveProxy(context.Background(), values, variables.Manifest{Bindings: bindings, Endpoint: storage.URL}, "127.0.0.1:1")
	if err != nil {
		t.Fatalf("serveProxy() = %v", err)
	}
	t.Cleanup(func() { _ = served.Close() })
	client := bucketv1connect.NewBucketServiceClient(http.DefaultClient, readEnv(served, processenv.RuntimeAddressEnvVar),
		connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", localrpc.FormatAuthHeader(readEnv(served, localrpc.SessionTokenEnvVar)))
				return next(ctx, req)
			}
		})))

	head, err := client.Head(context.Background(), &bucketv1.HeadRequest{Bucket: appBucket, Key: "report.pdf"})
	if err != nil {
		t.Fatalf("Head() = %v, asked Cloud Storage %v", err, asked)
	}
	if head.GetObject().GetSize() != 7 || head.GetObject().GetContentType() != "application/pdf" {
		t.Errorf("Head() = %v, want what Cloud Storage keeps about the object", head.GetObject())
	}
	if len(asked) == 0 || !strings.HasPrefix(asked[0], "GET /storage/v1/b/"+appBucket+"/") {
		t.Errorf("the runtime asked %v, want the bucket's object read from Cloud Storage", asked)
	}
}
