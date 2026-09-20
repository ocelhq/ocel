package ocel_test

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/constants"
	ocel "github.com/ocelhq/ocel/sdk"
)

func TestBucketDeclaresDuringDiscovery(t *testing.T) {
	var seen []map[string]any
	srv := collector(t, &seen)
	t.Setenv(constants.PhaseEnvName, "discovery")
	t.Setenv(constants.DevServerEnvName, srv.URL)
	t.Setenv(constants.DevServerTokenEnvName, collectorToken)

	_, file, line, _ := runtime.Caller(0)
	store := ocel.Bucket("avatars", ocel.BucketPublic(), ocel.BucketAllowedOrigins("https://example.com"))

	if got := store.Name(); got != "avatars" {
		t.Errorf("Name() = %q, want %q", got, "avatars")
	}
	if len(seen) != 1 {
		t.Fatalf("declares = %d, want 1", len(seen))
	}
	got := seen[0]
	if got["__path"] != "/app.resources.v1.ResourceService/Declare" {
		t.Errorf("path = %v", got["__path"])
	}
	resource, _ := got["resource"].(map[string]any)
	if resource["type"] != "RESOURCE_TYPE_BUCKET" || resource["name"] != "avatars" {
		t.Errorf("resource = %v", resource)
	}
	bucket, _ := got["bucket"].(map[string]any)
	if bucket["public"] != true {
		t.Errorf("bucket = %v, want it declared public", bucket)
	}
	origins, _ := bucket["allowedOrigins"].([]any)
	if len(origins) != 1 || origins[0] != "https://example.com" {
		t.Errorf("allowedOrigins = %v", origins)
	}

	want := fmt.Sprintf("%s:%d", file, line+1)
	if got["source"] != want {
		t.Errorf("source = %v, want %q", got["source"], want)
	}
	if source, _ := got["source"].(string); !strings.HasSuffix(source, fmt.Sprintf("bucket_test.go:%d", line+1)) {
		t.Errorf("source = %q, want it to end at this test file", source)
	}
}

func TestBucketAccessorsRefuseDuringDiscovery(t *testing.T) {
	var seen []map[string]any
	srv := collector(t, &seen)
	t.Setenv(constants.PhaseEnvName, "discovery")
	t.Setenv(constants.DevServerEnvName, srv.URL)
	t.Setenv(constants.DevServerTokenEnvName, collectorToken)

	store := ocel.Bucket("avatars")

	for _, tc := range []struct {
		access string
		err    error
	}{
		{"Attrs", second(store.Attrs(t.Context(), "a"))},
		{"Exists", second(store.Exists(t.Context(), "a"))},
		{"Delete", store.Delete(t.Context(), "a")},
		{"Copy", second(store.Copy(t.Context(), "b", "a"))},
		{"NewReader", second(store.NewReader(t.Context(), "a"))},
		{"NewWriter", second(0, store.NewWriter(t.Context(), "a").Close())},
		{"SignedURL", second(store.SignedURL(t.Context(), "a"))},
		{"SignedUpload", second(store.SignedUpload(t.Context(), "a"))},
		{"PublicURL", second(store.PublicURL("a"))},
	} {
		var unprovisioned *ocel.UnprovisionedError
		if !errors.As(tc.err, &unprovisioned) {
			t.Fatalf("%s() error = %v, want an *UnprovisionedError", tc.access, tc.err)
		}
		if !strings.Contains(unprovisioned.Error(), `'bucket("avatars")' cannot be used during discovery`) {
			t.Errorf("%s() error = %q, want it to name the declaration", tc.access, unprovisioned)
		}
	}

	for _, err := range store.List(t.Context()) {
		var unprovisioned *ocel.UnprovisionedError
		if !errors.As(err, &unprovisioned) {
			t.Errorf("List() error = %v, want an *UnprovisionedError", err)
		}
	}
}

func TestABucketIsPrivateWithNoOriginsByDefault(t *testing.T) {
	var seen []map[string]any
	srv := collector(t, &seen)
	t.Setenv(constants.PhaseEnvName, "discovery")
	t.Setenv(constants.DevServerEnvName, srv.URL)
	t.Setenv(constants.DevServerTokenEnvName, collectorToken)

	ocel.Bucket("avatars")

	if len(seen) != 1 {
		t.Fatalf("declares = %d, want 1", len(seen))
	}
	bucket, _ := seen[0]["bucket"].(map[string]any)
	if bucket["public"] != nil || bucket["allowedOrigins"] != nil {
		t.Errorf("bucket = %v, want neither public nor allowed origins", bucket)
	}
}
