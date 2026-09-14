package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/constants"
	rt "github.com/ocelhq/ocel/pkg/runtimekit/live"
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

func TestTheRuntimeProjectsLiveValuesIntoADirectoryTheImageNeverHadToCarry(t *testing.T) {
	socket := answering(t, map[string]string{"DATABASE_URL": "postgres://app:hunter2@db/orders"})
	manifest, err := vars.Render(vars.Manifest{Slug: "shop", Class: "production", Keys: []rt.Key{{Key: "DATABASE_URL"}}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "ocel", "live")

	values, err := resolve(context.Background(), string(manifest), socket, dir)
	if err != nil {
		t.Fatalf("resolve() = %v, want the values projected into a directory that did not exist: an image built from scratch carries no /tmp", err)
	}

	read, err := os.ReadFile(filepath.Join(dir, "DATABASE_URL"))
	if err != nil {
		t.Fatalf("the projection holds no DATABASE_URL: %v", err)
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
