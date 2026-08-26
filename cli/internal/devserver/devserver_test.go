package devserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/cli/internal/resourceregistry"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func newFakeResolveServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/resources/resolve" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Resources []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"resources"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		env := make(map[string]string, len(req.Resources))
		for _, r := range req.Resources {
			key := fmt.Sprintf("OCEL_RESOURCE_%s_%s", r.Type, r.Name)
			env[key] = fmt.Sprintf(`{"name":%q,"postgres":{"host":"resolved","port":5432,"database":%q}}`, r.Name, r.Name)
		}

		json.NewEncoder(w).Encode(map[string]any{
			"env":       env,
			"expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func serve(t *testing.T, s *Server) string {
	t.Helper()
	ts := httptest.NewServer(s.Mux())
	t.Cleanup(ts.Close)
	return ts.URL
}

func declareResource(t *testing.T, url, name string, typ linksv1.LinkType) {
	t.Helper()
	client := resourcesv1connect.NewResourceServiceClient(http.DefaultClient, url)
	req := &resourcesv1.DeclareRequest{Resource: &resourcesv1.ResourceIdentifier{Name: name, Type: typ}}
	switch typ {
	case linksv1.LinkType_LINK_TYPE_POSTGRES:
		req.Config = &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{}}
	case linksv1.LinkType_LINK_TYPE_BUCKET:
		req.Config = &resourcesv1.DeclareRequest_Bucket{Bucket: &resourcesv1.BucketConfig{}}
	}
	if _, err := client.Declare(context.Background(), req); err != nil {
		t.Fatalf("Declare %s: %v", name, err)
	}
}

func postSync(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Post(url+"/sync", "application/octet-stream", nil)
	if err != nil {
		t.Fatalf("POST /sync: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestDeclare(t *testing.T) {
	t.Parallel()

	t.Run("rejects an unspecified resource type", func(t *testing.T) {
		t.Parallel()
		s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")

		_, err := s.Declare(context.Background(), &resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "main"},
		})
		if err == nil {
			t.Fatal("Declare: expected error for unspecified resource type, got nil")
		}
	})
}

func TestSync(t *testing.T) {
	t.Run("provisions a declared resource", func(t *testing.T) {
		resolveServer := newFakeResolveServer(t)
		s := New(resolveServer.URL, "tok", "proj_1", "http://127.0.0.1:0")
		url := serve(t, s)

		declareResource(t, url, "main", linksv1.LinkType_LINK_TYPE_POSTGRES)

		if status := postSync(t, url); status != http.StatusOK {
			t.Fatalf("POST /sync status = %d, want 200", status)
		}

		result := <-s.Sync()
		if result.Err != nil {
			t.Fatalf("Sync result error: %v", result.Err)
		}
		if result.Account.ProjectID != "proj_1" {
			t.Fatalf("Account.ProjectID = %q, want %q", result.Account.ProjectID, "proj_1")
		}
		if len(result.Resources) != 1 || result.Resources[0].Name != "main" {
			t.Fatalf("Resources = %+v, want one entry named main", result.Resources)
		}
	})

	t.Run("synthesizes bucket env locally and keeps it out of resolve", func(t *testing.T) {
		resolveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Resources []struct {
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"resources"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			env := make(map[string]string, len(req.Resources))
			for _, res := range req.Resources {
				if res.Type == "BUCKET" {
					http.Error(w, "resolve must never see a BUCKET", http.StatusBadRequest)
					return
				}
				env[fmt.Sprintf("OCEL_RESOURCE_%s_%s", res.Type, res.Name)] = `{"name":"main","postgres":{"host":"x","port":5432,"database":"main"}}`
			}
			json.NewEncoder(w).Encode(map[string]any{
				"env":       env,
				"expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		}))
		defer resolveServer.Close()

		s := New(resolveServer.URL, "tok", "proj_1", "http://dev.local:1234")
		url := serve(t, s)

		declareResource(t, url, "main", linksv1.LinkType_LINK_TYPE_POSTGRES)
		declareResource(t, url, "storage", linksv1.LinkType_LINK_TYPE_BUCKET)

		if status := postSync(t, url); status != http.StatusOK {
			t.Fatalf("POST /sync status = %d, want 200", status)
		}

		result := <-s.Sync()
		if result.Err != nil {
			t.Fatalf("Sync result error: %v", result.Err)
		}

		i := slices.IndexFunc(result.Resources, func(r resolve.Resource) bool { return r.Name == "storage" })
		if i < 0 {
			t.Fatalf("Resources = %+v, want a synthesized storage bucket entry", result.Resources)
		}

		raw, ok := result.Resources[i].Env["OCEL_RESOURCE_BUCKET_storage"]
		if !ok {
			t.Fatalf("bucket env = %+v, want OCEL_RESOURCE_BUCKET_storage", result.Resources[i].Env)
		}
		var link linksv1.Link
		if err := protojson.Unmarshal([]byte(raw), &link); err != nil {
			t.Fatalf("unmarshal bucket env: %v", err)
		}
		want := &linksv1.Link{Name: "storage", Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "storage"}}}
		if !proto.Equal(&link, want) {
			t.Fatalf("bucket env = %s, want %v", raw, want)
		}
		if result.DevServerAddress != "http://dev.local:1234" {
			t.Fatalf("DevServerAddress = %q, want the dev server address every membrane-backed resource reaches", result.DevServerAddress)
		}
	})

	t.Run("only sees resources declared after a reset", func(t *testing.T) {
		resolveServer := newFakeResolveServer(t)
		s := New(resolveServer.URL, "tok", "proj_1", "http://127.0.0.1:0")
		url := serve(t, s)

		declareResource(t, url, "stale", linksv1.LinkType_LINK_TYPE_POSTGRES)
		s.ResetManifest()
		declareResource(t, url, "fresh", linksv1.LinkType_LINK_TYPE_POSTGRES)

		postSync(t, url)

		result := <-s.Sync()
		if result.Err != nil {
			t.Fatalf("Sync result error: %v", result.Err)
		}
		if len(result.Resources) != 1 || result.Resources[0].Name != "fresh" {
			t.Fatalf("Resources = %+v, want one entry named fresh", result.Resources)
		}
	})

	t.Run("refuses a method other than POST", func(t *testing.T) {
		t.Parallel()
		url := serve(t, New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0"))

		resp, err := http.Get(url + "/sync")
		if err != nil {
			t.Fatalf("GET /sync: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET /sync status = %d, want 405", resp.StatusCode)
		}
	})

	t.Run("propagates a provision error", func(t *testing.T) {
		t.Parallel()
		s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
		s.resolve = func(context.Context, resolve.Account, []resourceregistry.Entry) ([]resolve.Resource, error) {
			return nil, errors.New("boom")
		}
		url := serve(t, s)

		if status := postSync(t, url); status != http.StatusInternalServerError {
			t.Fatalf("POST /sync status = %d, want 500", status)
		}

		result := <-s.Sync()
		if result.Err == nil {
			t.Fatal("Sync result: expected error, got nil")
		}
	})

	t.Run("resolves only live keys eagerly", func(t *testing.T) {
		t.Parallel()
		s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")

		var asked []string
		s.fetchLiveValues = func(_ context.Context, _, _, _ string, keys []string) (map[string]string, error) {
			asked = keys
			return map[string]string{"WEBHOOK_SECRET": "whsec_live"}, nil
		}
		url := serve(t, s)

		declareEnv(t, url,
			&resourcesv1.VariableDefinition{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
			&resourcesv1.VariableDefinition{Key: "STRIPE_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE},
			&resourcesv1.VariableDefinition{Key: "WEBHOOK_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET},
		)

		postSync(t, url)

		result := <-s.Sync()
		if result.Err != nil {
			t.Fatalf("Sync result error: %v", result.Err)
		}
		if want := []string{"WEBHOOK_SECRET"}; !slices.Equal(asked, want) {
			t.Errorf("fetched %v, want only the live-class keys %v", asked, want)
		}
		if got := result.LiveValues["WEBHOOK_SECRET"]; got != "whsec_live" {
			t.Errorf("LiveValues[WEBHOOK_SECRET] = %q, want the value the control plane returned", got)
		}
		if _, ok := result.LiveValues["POSTHOG_ID"]; ok {
			t.Errorf("LiveValues = %v, want no entry for a key delivered by the artifact", result.LiveValues)
		}
		if want := []string{"WEBHOOK_SECRET"}; !slices.Equal(result.LiveKeys, want) {
			t.Errorf("LiveKeys = %v, want %v", result.LiveKeys, want)
		}
	})

	t.Run("reports the declared live keys even when the source resolves none", func(t *testing.T) {
		t.Parallel()
		s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
		s.fetchLiveValues = func(context.Context, string, string, string, []string) (map[string]string, error) {
			return map[string]string{}, nil
		}
		url := serve(t, s)

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "WEBHOOK_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
		})

		postSync(t, url)

		result := <-s.Sync()
		if result.Err != nil {
			t.Fatalf("Sync result error: %v", result.Err)
		}
		if len(result.LiveValues) != 0 {
			t.Fatalf("LiveValues = %v, want the empty result the source gave", result.LiveValues)
		}
		if want := []string{"WEBHOOK_SECRET"}; !slices.Equal(result.LiveKeys, want) {
			t.Errorf("LiveKeys = %v, want %v: what the run declared, not what resolved", result.LiveKeys, want)
		}
	})

	t.Run("declaring no live keys asks the control plane for nothing", func(t *testing.T) {
		t.Parallel()
		s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")

		called := false
		s.fetchLiveValues = func(context.Context, string, string, string, []string) (map[string]string, error) {
			called = true
			return nil, errors.New("the control plane is unreachable")
		}
		url := serve(t, s)

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		})

		postSync(t, url)

		if result := <-s.Sync(); result.Err != nil {
			t.Fatalf("Sync result error: %v", result.Err)
		}
		if called {
			t.Error("a project declaring no live-class key still asked the control plane for live values")
		}
	})

	t.Run("an unreachable live source fails the dev run", func(t *testing.T) {
		t.Parallel()
		s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
		s.fetchLiveValues = func(context.Context, string, string, string, []string) (map[string]string, error) {
			return nil, errors.New("the control plane is unreachable")
		}
		url := serve(t, s)

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "WEBHOOK_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
		})

		if status := postSync(t, url); status == http.StatusOK {
			t.Errorf("POST /sync status = %d, want a failure the dev run stops on", status)
		}

		result := <-s.Sync()
		if result.Err == nil {
			t.Fatal("Sync result error = nil, want the unreachable live source reported")
		}
		if !strings.Contains(result.Err.Error(), "WEBHOOK_SECRET") {
			t.Errorf("Sync result error = %q, want it to name the key that could not be resolved", result.Err)
		}
	})

	t.Run("forgets live keys a declaration no longer names after a reset", func(t *testing.T) {
		t.Parallel()
		s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")

		var asked []string
		s.fetchLiveValues = func(_ context.Context, _, _, _ string, keys []string) (map[string]string, error) {
			asked = keys
			return map[string]string{}, nil
		}
		url := serve(t, s)

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "GONE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
		})

		s.ResetManifest()

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "KEPT", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
		})

		postSync(t, url)
		<-s.Sync()

		if want := []string{"KEPT"}; !slices.Equal(asked, want) {
			t.Errorf("fetched %v, want only %v — the reset dropped the prior declaration", asked, want)
		}
	})
}

func TestEnvStream(t *testing.T) {
	t.Parallel()

	t.Run("receives env pushed after connecting", func(t *testing.T) {
		t.Parallel()
		s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
		s.PushEnv(map[string]string{"INITIAL": "1"})
		url := serve(t, s)

		stream := openEnvStream(t, url)

		if got := readEnvEvent(t, stream)["INITIAL"]; got != "1" {
			t.Fatalf("initial env INITIAL = %q, want %q", got, "1")
		}

		s.PushEnv(map[string]string{"OCEL_RESOURCE_POSTGRES_main": "conn"})

		got := readEnvEvent(t, stream)
		if got["OCEL_RESOURCE_POSTGRES_main"] != "conn" {
			t.Fatalf("pushed env = %+v, want OCEL_RESOURCE_POSTGRES_main=conn", got)
		}
	})

	t.Run("a new subscriber immediately gets the latest env", func(t *testing.T) {
		t.Parallel()
		s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
		s.PushEnv(map[string]string{"FOO": "bar"})
		url := serve(t, s)

		if got := readEnvEvent(t, openEnvStream(t, url))["FOO"]; got != "bar" {
			t.Fatalf("pushed env FOO = %q, want %q", got, "bar")
		}
	})
}

func openEnvStream(t *testing.T, url string) *bufio.Reader {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/env", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /env: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /env = %s, want 200", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	return bufio.NewReader(resp.Body)
}

func readEnvEvent(t *testing.T, reader *bufio.Reader) map[string]string {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		payload, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data: ")
		if !ok {
			continue
		}
		var env map[string]string
		if err := json.Unmarshal([]byte(payload), &env); err != nil {
			t.Fatalf("decode event %q: %v", payload, err)
		}
		return env
	}
}
