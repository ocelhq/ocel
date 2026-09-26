package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/constants"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
)

func answering(t *testing.T, values map[string]string) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "values.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != vars.ValuesPath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(vars.Answer{Values: values})
	})}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket
}

func TestTheRuntimeProjectsLiveValuesIntoADirectoryTheImageNeverHadToShip(t *testing.T) {
	socket := answering(t, map[string]string{"DATABASE_URL": "postgres://app:hunter2@db/orders"})
	manifest, err := vars.Render(vars.Manifest{Slug: "shop", Class: "production", Keys: []live.Key{{Key: "DATABASE_URL"}}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "ocel", "live")

	values, err := resolve(context.Background(), string(manifest), socket, dir)
	if err != nil {
		t.Fatalf("resolve() = %v, want the values projected into a directory that did not exist: an image built from scratch has no /tmp", err)
	}

	read, err := os.ReadFile(filepath.Join(dir, "DATABASE_URL"))
	if err != nil {
		t.Fatalf("the projection contains no DATABASE_URL: %v", err)
	}
	if string(read) != "postgres://app:hunter2@db/orders" {
		t.Errorf("the projection reads DATABASE_URL as %q", read)
	}
	env := values.Env()
	for _, want := range []string{constants.LiveKeysEnvName + "=DATABASE_URL", constants.LiveDirEnvName + "=" + dir} {
		if !slices.Contains(env, want) {
			t.Errorf("the app is handed %q, which never says %s", env, want)
		}
	}
}

func bucketManifest(t *testing.T, store *vars.Store) (vars.Manifest, string) {
	t.Helper()
	manifest := vars.Manifest{
		Slug: "shop", Class: "production",
		Bindings: []live.Binding{{
			Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads",
			Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET,
		}},
		Store: store,
	}
	record, err := protojson.Marshal(&bindingsv1.Binding{
		Name:       "uploads",
		Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "shop-prod-uploads"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return manifest, string(record)
}

func TestTheRuntimeFrontsAProxiedBindingAndKeepsTheStoreCredentialToItself(t *testing.T) {
	manifest, record := bucketManifest(t, &vars.Store{
		Env: "shop-prod", Endpoint: "http://shop-prod-store-s3:9000", Region: "us-east-1",
		AccessKeyID: "ocel", PathStyle: true, Sessions: "shop-prod-uploads", Volume: "shop-prod-store-s3",
	})
	socket := answering(t, map[string]string{
		"OCEL_RESOURCE_BUCKET_uploads": record,
		vars.StoreSecretKey:            "s3cr3t",
	})
	rendered, err := vars.Render(manifest)
	if err != nil {
		t.Fatal(err)
	}
	values, err := resolve(context.Background(), string(rendered), socket, filepath.Join(t.TempDir(), "live"))
	if err != nil {
		t.Fatalf("resolve() = %v", err)
	}

	served, err := proxying(manifest, values, socket, "127.0.0.1:1")
	if err != nil {
		t.Fatalf("proxying() = %v", err)
	}
	t.Cleanup(func() { _ = served.Close() })

	var address, token string
	for _, entry := range served.Env {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case constants.RuntimeAddressEnvName:
			address = value
		case channel.SessionTokenEnvVar:
			token = value
		}
	}
	if !strings.HasPrefix(address, "http://127.0.0.1:") {
		t.Errorf("the app is pointed at %q, and the proxy answers on loopback alone", address)
	}
	if token == "" {
		t.Errorf("the app is handed no session token, and an unauthenticated proxy serves anyone in the container")
	}
	for _, entry := range append(slices.Clone(served.Env), values.Env()...) {
		if strings.Contains(entry, "s3cr3t") {
			t.Errorf("the app is handed %q: the store credential is the proxy's alone", entry)
		}
	}
}

func TestTheRuntimeFrontsABucketBoundToAStoreWithNoStoreOfItsOwn(t *testing.T) {
	manifest, _ := bucketManifest(t, nil)
	bound, err := protojson.Marshal(&bindingsv1.Binding{
		Name: "ocel:bucket.uploads",
		Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{
			Bucket: "acme", Endpoint: "https://abc.r2.cloudflarestorage.com", Region: "auto",
			AccessKeyId: "AKID", SecretAccessKey: "r2-s3cr3t",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := answering(t, map[string]string{"OCEL_RESOURCE_BUCKET_uploads": string(bound)})
	rendered, err := vars.Render(manifest)
	if err != nil {
		t.Fatal(err)
	}
	values, err := resolve(context.Background(), string(rendered), socket, filepath.Join(t.TempDir(), "live"))
	if err != nil {
		t.Fatalf("resolve() = %v", err)
	}

	served, err := proxying(manifest, values, socket, "127.0.0.1:1")
	if err != nil {
		t.Fatalf("proxying() = %v", err)
	}
	t.Cleanup(func() { _ = served.Close() })
	if len(served.Env) == 0 {
		t.Fatal("proxying() started nothing, and the app reaches its bound bucket only through the proxy")
	}
	for _, entry := range served.Env {
		if strings.Contains(entry, "r2-s3cr3t") {
			t.Errorf("the app is handed %q: the store's secret key is the proxy's alone", entry)
		}
	}
}

func TestTheRuntimeFrontsNothingWhereNoBindingIsProxied(t *testing.T) {
	manifest := vars.Manifest{Slug: "shop", Class: "production", Keys: []live.Key{{Key: "DATABASE_URL"}}}
	served, err := proxying(manifest, nil, filepath.Join(t.TempDir(), "absent.sock"), "127.0.0.1:1")
	if err != nil || served.Env != nil {
		t.Errorf("proxying() = %+v, %v, want no proxy for a deployment binding nothing it must be fronted for", served, err)
	}
}

func TestAProxiedBindingWithNoStoreCredentialIsRefused(t *testing.T) {
	manifest, record := bucketManifest(t, &vars.Store{Env: "shop-prod", Endpoint: "http://store:9000", Region: "us-east-1", AccessKeyID: "ocel"})
	socket := answering(t, map[string]string{"OCEL_RESOURCE_BUCKET_uploads": record})
	rendered, err := vars.Render(manifest)
	if err != nil {
		t.Fatal(err)
	}
	values, err := resolve(context.Background(), string(rendered), socket, filepath.Join(t.TempDir(), "live"))
	if err != nil {
		t.Fatalf("resolve() = %v", err)
	}
	if _, err := proxying(manifest, values, socket, "127.0.0.1:1"); err == nil {
		t.Error("proxying() started a proxy with no credential to reach the store with, which would fail every write instead of the deploy")
	}
}

func TestAManifestNamingNothingLiveResolvesToNoValuesAndNoDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "live")

	values, err := resolve(context.Background(), `{"slug":"shop","class":"production"}`, filepath.Join(t.TempDir(), "absent.sock"), dir)
	if err != nil {
		t.Fatalf("resolve() = %v", err)
	}
	if values != nil {
		t.Errorf("resolve() = %+v, want nothing for a deployment that reads nothing live", values)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("a deployment reading nothing live still had %s made for it", dir)
	}
}
