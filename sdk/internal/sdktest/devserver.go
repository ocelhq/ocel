package sdktest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const DevServerToken = "opensesame"

func ServeDevServer(t *testing.T, seen *[]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+DevServerToken {
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

func Declarations(t *testing.T) *[]map[string]any {
	t.Helper()
	var seen []map[string]any
	srv := ServeDevServer(t, &seen)
	t.Setenv("OCEL_PHASE", "discovery")
	t.Setenv("OCEL_DEV_SERVER", srv.URL)
	t.Setenv("OCEL_DEV_SERVER_TOKEN", DevServerToken)
	return &seen
}

func Declared(t *testing.T, seen []map[string]any, kind, name string) map[string]any {
	t.Helper()
	for _, decl := range seen {
		resource, _ := decl["resource"].(map[string]any)
		if resource["type"] == kind && resource["name"] == name {
			if decl["__path"] != "/app.resources.v1.ResourceService/Declare" {
				t.Errorf("%s %q was posted to %v", kind, name, decl["__path"])
			}
			return decl
		}
	}
	t.Fatalf("no %s named %q among the declarations %v", kind, name, seen)
	return nil
}
