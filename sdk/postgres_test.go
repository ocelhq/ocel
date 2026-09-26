package ocel_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ocel.dev"
)

const collectorToken = "opensesame"

func collector(t *testing.T, seen *[]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+collectorToken {
			http.Error(w, "this request has no valid session token", http.StatusForbidden)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("unmarshal %q: %v", raw, err)
		}
		body["__path"] = r.URL.Path
		body["__contentType"] = r.Header.Get("Content-Type")
		body["__authorization"] = r.Header.Get("Authorization")
		body["__sdk"] = r.Header.Get("Ocel-Sdk-Version")
		*seen = append(*seen, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestADeclarationSendsTheDevServerToken(t *testing.T) {
	var seen []map[string]any
	srv := collector(t, &seen)
	t.Setenv("OCEL_PHASE", "discovery")
	t.Setenv("OCEL_DEV_SERVER", srv.URL)
	t.Setenv("OCEL_DEV_SERVER_TOKEN", collectorToken)

	ocel.Postgres("main")

	if len(seen) != 1 {
		t.Fatalf("declares = %d, want 1", len(seen))
	}
	if got := seen[0]["__authorization"]; got != "Bearer "+collectorToken {
		t.Errorf("Authorization = %v, want the dev server token", got)
	}
	if got, want := seen[0]["__sdk"], "go/"+ocel.Version(); got != want {
		t.Errorf("Ocel-Sdk-Version = %v, want %q", got, want)
	}
}

func TestPostgresDeclaresDuringDiscovery(t *testing.T) {
	var seen []map[string]any
	srv := collector(t, &seen)
	t.Setenv("OCEL_PHASE", "discovery")
	t.Setenv("OCEL_DEV_SERVER", srv.URL)
	t.Setenv("OCEL_DEV_SERVER_TOKEN", collectorToken)

	_, file, line, _ := runtime.Caller(0)
	db := ocel.Postgres("main")

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
	srv := collector(t, &seen)
	t.Setenv("OCEL_PHASE", "discovery")
	t.Setenv("OCEL_DEV_SERVER", srv.URL)
	t.Setenv("OCEL_DEV_SERVER_TOKEN", collectorToken)

	ocel.Postgres("main", ocel.PostgresVersion("16"))

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
	srv := collector(t, &seen)
	t.Setenv("OCEL_PHASE", "discovery")
	t.Setenv("OCEL_DEV_SERVER", srv.URL)
	t.Setenv("OCEL_DEV_SERVER_TOKEN", collectorToken)

	db := ocel.Postgres("main")

	for _, tc := range []struct {
		access string
		err    error
	}{
		{"ConnectionString", second(db.ConnectionString())},
		{"Pool", second(db.Pool(t.Context()))},
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

func second[T any](_ T, err error) error { return err }

func TestConnectionStringPercentEncodesCredentials(t *testing.T) {
	t.Setenv("OCEL_RESOURCE_POSTGRES_main", `{"name":"main","postgres":{"host":"h","port":5432,"database":"d","username":"user name","password":"p@ss:word/with#odd?chars"}}`)

	got, err := ocel.Postgres("main").ConnectionString()
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

	got, err := ocel.Postgres("main").ConnectionString()
	if err != nil {
		t.Fatalf("ConnectionString() error = %v", err)
	}
	if want := "postgres://u:p@h:5432/d"; got != want {
		t.Errorf("ConnectionString() = %q, want %q", got, want)
	}
}

func TestAMissingBindingNamesTheCommandsThatDeliverIt(t *testing.T) {
	_, err := ocel.Postgres("main").ConnectionString()

	want := "Value for OCEL_RESOURCE_POSTGRES_main is not defined. " +
		"Run `ocel dev` to resolve it locally, or `ocel deploy` to have it delivered from the resource this app binds."
	if err == nil || err.Error() != want {
		t.Errorf("ConnectionString() error = %v, want %q", err, want)
	}
}

func TestABindingOfAnotherTypeIsRefused(t *testing.T) {
	t.Setenv("OCEL_RESOURCE_POSTGRES_main", `{"name":"main","bucket":{"bucket":"b"}}`)

	_, err := ocel.Postgres("main").ConnectionString()

	want := "OCEL_RESOURCE_POSTGRES_main carries a BUCKET binding, and this app reads it as a POSTGRES"
	if err == nil || err.Error() != want {
		t.Errorf("ConnectionString() error = %v, want %q", err, want)
	}
}

func TestAMalformedBindingKeepsTheValueOutOfTheError(t *testing.T) {
	t.Setenv("OCEL_RESOURCE_POSTGRES_main", `{"name":"main","postgres":{"password":s3cretpassword}}`)

	_, err := ocel.Postgres("main").ConnectionString()
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
	_, err := ocel.Postgres("main").ConnectionString()

	var missing *ocel.MissingBindingError
	if !errors.As(err, &missing) {
		t.Fatalf("ConnectionString() error = %v, want a *ocel.MissingBindingError", err)
	}
	if missing.Key != "OCEL_RESOURCE_POSTGRES_main" {
		t.Errorf("Key = %q, want the env var the binding arrives in", missing.Key)
	}
}
