package postgres_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ocel.dev"
	"ocel.dev/internal/declaration"
	"ocel.dev/internal/sdktest"
	"ocel.dev/postgres"
)

func TestADeclarationSendsTheDevServerToken(t *testing.T) {
	var seen []map[string]any
	srv := sdktest.ServeDevServer(t, &seen)
	t.Setenv("OCEL_PHASE", "discovery")
	t.Setenv("OCEL_DEV_SERVER", srv.URL)
	t.Setenv("OCEL_DEV_SERVER_TOKEN", sdktest.DevServerToken)

	postgres.New("main")

	if len(seen) != 1 {
		t.Fatalf("declares = %d, want 1", len(seen))
	}
	if got := seen[0]["__authorization"]; got != "Bearer "+sdktest.DevServerToken {
		t.Errorf("Authorization = %v, want the dev server token", got)
	}
	if got, want := seen[0]["__sdk"], "go/"+declaration.Version; got != want {
		t.Errorf("Ocel-Sdk-Version = %v, want %q", got, want)
	}
}

func TestPostgresDeclaresDuringDiscovery(t *testing.T) {
	var seen []map[string]any
	srv := sdktest.ServeDevServer(t, &seen)
	t.Setenv("OCEL_PHASE", "discovery")
	t.Setenv("OCEL_DEV_SERVER", srv.URL)
	t.Setenv("OCEL_DEV_SERVER_TOKEN", sdktest.DevServerToken)

	_, file, line, _ := runtime.Caller(0)
	db := postgres.New("main")

	if got := db.Name(); got != "main" {
		t.Errorf("Name() = %q, want %q", got, "main")
	}
	if len(seen) != 1 {
		t.Fatalf("declares = %d, want 1", len(seen))
	}
	got := seen[0]
	if got["__path"] != "/app.resources.v1.ResourceService/Declare" {
		t.Errorf("path = %v", got["__path"])
	}
	if ct, _ := got["__contentType"].(string); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %v", got["__contentType"])
	}

	resource, _ := got["resource"].(map[string]any)
	if resource["type"] != "RESOURCE_TYPE_POSTGRES" || resource["name"] != "main" {
		t.Errorf("resource = %v", resource)
	}
	postgres, _ := got["postgres"].(map[string]any)
	if postgres["version"] != "17" {
		t.Errorf("postgres = %v", postgres)
	}

	want := fmt.Sprintf("%s:%d", file, line+1)
	if got["source"] != want {
		t.Errorf("source = %v, want %q", got["source"], want)
	}
	if source, _ := got["source"].(string); !strings.HasSuffix(source, fmt.Sprintf("postgres_test.go:%d", line+1)) {
		t.Errorf("source = %q, want it to end at this test file", source)
	}
}

func TestVersionOverridesTheDeclaredVersion(t *testing.T) {
	var seen []map[string]any
	srv := sdktest.ServeDevServer(t, &seen)
	t.Setenv("OCEL_PHASE", "discovery")
	t.Setenv("OCEL_DEV_SERVER", srv.URL)
	t.Setenv("OCEL_DEV_SERVER_TOKEN", sdktest.DevServerToken)

	postgres.New("main", postgres.Version("16"))

	if len(seen) != 1 {
		t.Fatalf("declares = %d, want 1", len(seen))
	}
	postgres, _ := seen[0]["postgres"].(map[string]any)
	if postgres["version"] != "16" {
		t.Errorf("postgres = %v", postgres)
	}
}

func TestAccessorsRefuseDuringDiscovery(t *testing.T) {
	var seen []map[string]any
	srv := sdktest.ServeDevServer(t, &seen)
	t.Setenv("OCEL_PHASE", "discovery")
	t.Setenv("OCEL_DEV_SERVER", srv.URL)
	t.Setenv("OCEL_DEV_SERVER_TOKEN", sdktest.DevServerToken)

	db := postgres.New("main")

	for _, tc := range []struct {
		access string
		err    error
	}{
		{"ConnectionString", sdktest.ErrorOf(db.ConnectionString())},
		{"Pool", sdktest.ErrorOf(db.Pool(t.Context()))},
	} {
		var unprovisioned *ocel.UnprovisionedError
		if !errors.As(tc.err, &unprovisioned) {
			t.Fatalf("%s() error = %v, want an *UnprovisionedError", tc.access, tc.err)
		}
		want := fmt.Sprintf(
			"'postgres(\"main\")' cannot be used during discovery: tried to access '%s' before the resource was provisioned",
			tc.access,
		)
		if unprovisioned.Error() != want {
			t.Errorf("%s() error = %q, want %q", tc.access, unprovisioned, want)
		}
	}
}

func TestConnectionStringPercentEncodesCredentials(t *testing.T) {
	t.Setenv("OCEL_RESOURCE_POSTGRES_main", `{"name":"main","postgres":{"host":"h","port":5432,"database":"d","username":"user name","password":"p@ss:word/with#odd?chars"}}`)

	got, err := postgres.New("main").ConnectionString()
	if err != nil {
		t.Fatalf("ConnectionString() error = %v", err)
	}
	want := "postgres://user%20name:p%40ss%3Aword%2Fwith%23odd%3Fchars@h:5432/d"
	if got != want {
		t.Errorf("ConnectionString() = %q, want %q", got, want)
	}
}

func TestABindingIsReadFromTheProjectedLiveDirectory(t *testing.T) {
	dir := t.TempDir()
	write := []byte(`{"name":"main","postgres":{"host":"h","port":5432,"database":"d","username":"u","password":"p"}}`)
	if err := os.WriteFile(filepath.Join(dir, "OCEL_RESOURCE_POSTGRES_main"), write, 0o600); err != nil {
		t.Fatalf("project the binding: %v", err)
	}
	t.Setenv("OCEL_LIVE_DIR", dir)

	got, err := postgres.New("main").ConnectionString()
	if err != nil {
		t.Fatalf("ConnectionString() error = %v", err)
	}
	if want := "postgres://u:p@h:5432/d"; got != want {
		t.Errorf("ConnectionString() = %q, want %q", got, want)
	}
}

func TestAMissingBindingNamesTheCommandsThatDeliverIt(t *testing.T) {
	_, err := postgres.New("main").ConnectionString()

	want := "OCEL_RESOURCE_POSTGRES_main is not delivered to this process: " +
		"`ocel dev` delivers it locally and `ocel deploy` to the deployed app, and a build gets it only from an " +
		"`ocel deploy` whose provider forwards a port to the resource, so code that runs while building, " +
		"such as prerendering a page, cannot use it under `ocel build`"
	if err == nil || err.Error() != want {
		t.Errorf("ConnectionString() error = %v, want %q", err, want)
	}
}

func TestABindingOfAnotherTypeIsRefused(t *testing.T) {
	t.Setenv("OCEL_RESOURCE_POSTGRES_main", `{"name":"main","bucket":{"bucket":"b"}}`)

	_, err := postgres.New("main").ConnectionString()

	want := "OCEL_RESOURCE_POSTGRES_main contains a BUCKET binding, and this app reads it as a POSTGRES"
	if err == nil || err.Error() != want {
		t.Errorf("ConnectionString() error = %v, want %q", err, want)
	}
}

func TestAMalformedBindingKeepsTheValueOutOfTheError(t *testing.T) {
	t.Setenv("OCEL_RESOURCE_POSTGRES_main", `{"name":"main","postgres":{"password":s3cretpassword}}`)

	_, err := postgres.New("main").ConnectionString()
	if err == nil {
		t.Fatal("ConnectionString() succeeded on a malformed binding, want error")
	}
	if strings.Contains(err.Error(), "s3cretpassword") {
		t.Errorf("ConnectionString() error = %q, want it to keep the value out", err)
	}
	if !strings.Contains(err.Error(), "OCEL_RESOURCE_POSTGRES_main") {
		t.Errorf("ConnectionString() error = %q, want it to name the key", err)
	}
}

func TestAMissingBindingIsAMissingBindingError(t *testing.T) {
	_, err := postgres.New("main").ConnectionString()

	var missing *ocel.MissingBindingError
	if !errors.As(err, &missing) {
		t.Fatalf("ConnectionString() error = %v, want a *ocel.MissingBindingError", err)
	}
	if missing.Key != "OCEL_RESOURCE_POSTGRES_main" {
		t.Errorf("Key = %q, want the env var the binding arrives in", missing.Key)
	}
}

func TestPoolOpensOnceTheBindingArrivesAfterAFailedOpen(t *testing.T) {
	db := postgres.New("late")

	var missing *ocel.MissingBindingError
	if _, err := db.Pool(t.Context()); !errors.As(err, &missing) {
		t.Fatalf("Pool() before the binding err = %v, want a *ocel.MissingBindingError", err)
	}
	t.Setenv("OCEL_RESOURCE_POSTGRES_late", `{"name":"late","postgres":{"host":"127.0.0.1","port":5432,"database":"d","username":"u","password":"p"}}`)

	pool, err := db.Pool(t.Context())
	if err != nil {
		t.Fatalf("Pool() after the binding arrived = %v, want the pool opened", err)
	}
	t.Cleanup(pool.Close)
}

func TestAPoolPresentsTheRotatedPasswordOnItsNextConnection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OCEL_LIVE_DIR", dir)
	deliver := func(password string) {
		t.Helper()
		record := fmt.Sprintf(`{"name":"main","postgres":{"host":"127.0.0.1","port":5432,"database":"d","username":"u","password":%q}}`, password)
		if err := os.WriteFile(filepath.Join(dir, "OCEL_RESOURCE_POSTGRES_main"), []byte(record), 0o600); err != nil {
			t.Fatalf("project the binding: %v", err)
		}
	}
	deliver("generation-1")
	pool, err := postgres.New("main").Pool(t.Context())
	if err != nil {
		t.Fatalf("Pool() error = %v", err)
	}
	t.Cleanup(pool.Close)

	deliver("generation-2")
	config := pool.Config()
	if config.BeforeConnect == nil {
		t.Fatal("pool BeforeConnect = nil, want the binding re-read before each connection")
	}
	next := config.ConnConfig.Copy()
	if err := config.BeforeConnect(t.Context(), next); err != nil {
		t.Fatalf("BeforeConnect() error = %v", err)
	}
	if next.Password != "generation-2" {
		t.Errorf("next connection's password = %q, want the rotated generation-2", next.Password)
	}
}
