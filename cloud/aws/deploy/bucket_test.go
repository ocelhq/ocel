package deploy

import (
	"reflect"
	"slices"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestTranslateBucket_CORSFromAllowedOrigins(t *testing.T) {
	origins := []string{"https://app.example.com", "https://www.example.com"}
	got := translateBucket(&resourcesv1.BucketConfig{AllowedOrigins: origins})

	if !reflect.DeepEqual(got.AllowedOrigins, origins) {
		t.Errorf("AllowedOrigins = %v, want %v (carried through for the listener allowlist)", got.AllowedOrigins, origins)
	}
	if !reflect.DeepEqual(got.CORS.AllowedOrigins, origins) {
		t.Errorf("CORS.AllowedOrigins = %v, want the app's declared origins %v", got.CORS.AllowedOrigins, origins)
	}
	if !slices.Contains(got.CORS.AllowedMethods, "PUT") {
		t.Errorf("CORS.AllowedMethods = %v, want it to permit browser PUT", got.CORS.AllowedMethods)
	}
	if len(got.CORS.AllowedHeaders) == 0 {
		t.Error("CORS.AllowedHeaders is empty; the presigned PUT's signed headers need a preflight allow")
	}
	if got.CORS.MaxAgeSeconds != bucketCORSMaxAgeSeconds {
		t.Errorf("CORS.MaxAgeSeconds = %d, want %d", got.CORS.MaxAgeSeconds, bucketCORSMaxAgeSeconds)
	}
}

func TestTranslateBucket_NotificationAndLambdaArgs(t *testing.T) {
	got := translateBucket(&resourcesv1.BucketConfig{})

	if !reflect.DeepEqual(got.NotificationEvents, []string{"s3:ObjectCreated:*"}) {
		t.Errorf("NotificationEvents = %v, want [s3:ObjectCreated:*]", got.NotificationEvents)
	}
	if got.ListenerRuntime != listenerRuntime {
		t.Errorf("ListenerRuntime = %q, want %q (Go custom runtime)", got.ListenerRuntime, listenerRuntime)
	}
	if got.ListenerHandler != listenerHandler {
		t.Errorf("ListenerHandler = %q, want %q", got.ListenerHandler, listenerHandler)
	}
	if got.ListenerTimeoutSeconds != listenerTimeoutSeconds {
		t.Errorf("ListenerTimeoutSeconds = %d, want %d", got.ListenerTimeoutSeconds, listenerTimeoutSeconds)
	}
}

func TestTranslateBucket_IAMArgs(t *testing.T) {
	got := translateBucket(&resourcesv1.BucketConfig{})

	// The runtime process presigns PUTs and reads/writes the session table.
	if !slices.Contains(got.RuntimeS3Actions, "s3:PutObject") {
		t.Errorf("RuntimeS3Actions = %v, want it to include s3:PutObject (presign)", got.RuntimeS3Actions)
	}
	for _, want := range []string{"dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem"} {
		if !slices.Contains(got.RuntimeSessionActions, want) {
			t.Errorf("RuntimeSessionActions = %v, want it to include %q", got.RuntimeSessionActions, want)
		}
	}

	// The listener reads the landed object's tags and performs the guarded
	// transition; it must NOT be able to presign or write objects.
	if !slices.Contains(got.ListenerS3Actions, "s3:GetObjectTagging") {
		t.Errorf("ListenerS3Actions = %v, want it to include s3:GetObjectTagging", got.ListenerS3Actions)
	}
	if slices.Contains(got.ListenerS3Actions, "s3:PutObject") {
		t.Errorf("ListenerS3Actions = %v, must not grant s3:PutObject (least privilege)", got.ListenerS3Actions)
	}
	if !slices.Contains(got.ListenerSessionActions, "dynamodb:UpdateItem") {
		t.Errorf("ListenerSessionActions = %v, want it to include dynamodb:UpdateItem (transition)", got.ListenerSessionActions)
	}
}

func TestTranslateBucket_EmptyOriginsYieldEmptyCORSOrigins(t *testing.T) {
	got := translateBucket(&resourcesv1.BucketConfig{})
	if len(got.CORS.AllowedOrigins) != 0 {
		t.Errorf("CORS.AllowedOrigins = %v, want empty for a bucket with no declared origins", got.CORS.AllowedOrigins)
	}
}
