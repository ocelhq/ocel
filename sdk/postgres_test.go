package sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/sdk"
)

func collector(t *testing.T, seen *[]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		*seen = append(*seen, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPostgresDeclaresDuringDiscovery(t *testing.T) {
	var seen []map[string]any
	srv := collector(t, &seen)
	t.Setenv("OCEL_PHASE", "discovery")
	t.Setenv("OCEL_DEV_SERVER", srv.URL)

	_, file, line, _ := runtime.Caller(0)
	db := sdk.Postgres("main")

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
	if resource["type"] != "LINK_TYPE_POSTGRES" || resource["name"] != "main" {
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

	sdk.Postgres("main", sdk.Version("16"))

	if len(seen) != 1 {
		t.Fatalf("declares = %d, want 1", len(seen))
	}
	postgres, _ := seen[0]["postgres"].(map[string]any)
	if postgres["version"] != "16" {
		t.Errorf("postgres = %v", postgres)
	}
}
