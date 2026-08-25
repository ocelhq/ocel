package resolver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/cli/internal/resourceregistry"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestEnvFragment(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		typ     linksv1.LinkType
		want    string
		wantErr bool
	}{
		{"renders the env fragment", linksv1.LinkType_LINK_TYPE_POSTGRES, "POSTGRES", false},
		{"renders the env fragment for buckets", linksv1.LinkType_LINK_TYPE_BUCKET, "BUCKET", false},
		{"rejects an unspecified type", linksv1.LinkType_LINK_TYPE_UNSPECIFIED, "", true},
		{"rejects an unknown type", linksv1.LinkType(99), "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := envFragment(tt.typ)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("envFragment(%v) = %q, want an error", tt.typ, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("envFragment: %v", err)
			}
			if got != tt.want {
				t.Fatalf("envFragment() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	onePostgres := []resourceregistry.Entry{{Name: "main", Type: linksv1.LinkType_LINK_TYPE_POSTGRES}}

	t.Run("an empty registry yields no resources without calling resolve", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("Resolve made an HTTP request for an empty resource list")
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}))
		defer ts.Close()

		got, err := Resolve(context.Background(), resolve.Account{ProjectID: "proj_abc", APIURL: ts.URL}, nil)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("Resolve() = %+v, want empty", got)
		}
	})

	t.Run("calls the resolve endpoint", func(t *testing.T) {
		var gotAuth string
		mux := http.NewServeMux()
		mux.HandleFunc("/api/resources/resolve", func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")

			var req resolveRequestBody
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode request: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.ProjectID != "proj_abc" {
				t.Errorf("ProjectID = %q, want %q", req.ProjectID, "proj_abc")
			}
			if len(req.Resources) != 1 || req.Resources[0].Name != "main" || req.Resources[0].Type != "POSTGRES" {
				t.Errorf("Resources = %+v", req.Resources)
			}

			_ = json.NewEncoder(w).Encode(resolveResponseBody{
				Env: map[string]string{
					"OCEL_RESOURCE_POSTGRES_main": `{"name":"main","postgres":{"host":"resolved","port":5432,"database":"main"}}`,
				},
				ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		})
		ts := httptest.NewServer(mux)
		defer ts.Close()

		got, err := Resolve(context.Background(), resolve.Account{ProjectID: "proj_abc", APIURL: ts.URL, Token: "tok_123"}, onePostgres)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("Resolve() len = %d, want 1", len(got))
		}
		if gotAuth != "Bearer tok_123" {
			t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer tok_123")
		}
		if got[0].Name != "main" {
			t.Fatalf("Name = %q, want %q", got[0].Name, "main")
		}
		if got[0].Type != linksv1.LinkType_LINK_TYPE_POSTGRES {
			t.Fatalf("Type = %v, want POSTGRES", got[0].Type)
		}
	})

	t.Run("injects the link under the canonical env key", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(resolveResponseBody{
				Env: map[string]string{
					"OCEL_RESOURCE_POSTGRES_main": `{"name":"main","postgres":{"host":"resolved","port":5432,"database":"main"}}`,
				},
				ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		}))
		defer ts.Close()

		got, err := Resolve(context.Background(), resolve.Account{ProjectID: "proj_abc", APIURL: ts.URL, Token: "tok_123"}, onePostgres)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("Resolve() len = %d, want 1", len(got))
		}

		const key = "OCEL_RESOURCE_POSTGRES_main"
		raw, ok := got[0].Env[key]
		if !ok {
			t.Fatalf("Env missing key %q, got %+v", key, got[0].Env)
		}

		var link linksv1.Link
		if err := protojson.Unmarshal([]byte(raw), &link); err != nil {
			t.Fatalf("Env[%q] = %q is not a link: %v", key, raw, err)
		}
		if link.GetPostgres().GetHost() != "resolved" || link.GetPostgres().GetDatabase() != "main" {
			t.Fatalf("link = %v, want host resolved and database main", &link)
		}
	})

	t.Run("propagates a non-OK status", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer ts.Close()

		_, err := Resolve(context.Background(), resolve.Account{ProjectID: "proj_abc", APIURL: ts.URL}, onePostgres)
		if err == nil {
			t.Fatal("Resolve: expected error, got nil")
		}
	})

	t.Run("a missing env key in the response is an error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(resolveResponseBody{Env: map[string]string{}})
		}))
		defer ts.Close()

		_, err := Resolve(context.Background(), resolve.Account{ProjectID: "proj_abc", APIURL: ts.URL}, onePostgres)
		if err == nil {
			t.Fatal("Resolve: expected error for missing env key, got nil")
		}
	})
}
