package devserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources"
	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker/dockertest"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/localrpc"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
)

type fakeResources struct {
	mu      sync.Mutex
	asked   [][]declaration.Resource
	resolve func([]declaration.Resource) ([]binding.Resolved, error)
	mounted bool
}

func (f *fakeResources) Resolve(_ context.Context, resources []declaration.Resource) ([]binding.Resolved, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, resources)
	if f.resolve != nil {
		return f.resolve(resources)
	}
	out := make([]binding.Resolved, 0, len(resources))
	for _, r := range resources {
		out = append(out, binding.Resolved{Name: r.Name, Type: r.Type, Env: map[string]string{"BOUND_" + r.Name: "yes"}})
	}
	return out, nil
}

func (f *fakeResources) Routes(mux *http.ServeMux, guard func(http.Handler) http.Handler, options ...connect.HandlerOption) {
	f.mounted = true
	path, handler := bucketv1connect.NewBucketServiceHandler(bucketv1connect.UnimplementedBucketServiceHandler{}, options...)
	mux.Handle(path, guard(handler))
}

func newDevServer(resources *fakeResources) *Server {
	return New("http://127.0.0.1:0", resources)
}

const testDiscoveryToken = "opensesame"

const testAppToken = "openforme"

type authorizing struct {
	token string
	base  http.RoundTripper
}

func (a authorizing) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", localrpc.FormatAuthHeader(a.token))
	return a.base.RoundTrip(r)
}

var bareClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

var testClient = &http.Client{Transport: authorizing{testDiscoveryToken, &http.Transport{DisableKeepAlives: true}}}

var appClient = &http.Client{Transport: authorizing{testAppToken, &http.Transport{DisableKeepAlives: true}}}

func serve(t *testing.T, s *Server) string {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	s.url = "http://" + ts.Listener.Addr().String()
	s.discoveryToken = testDiscoveryToken
	s.appToken = testAppToken
	ts.Config.Handler = s.Mux()
	ts.Start()
	t.Cleanup(ts.Close)
	return ts.URL
}

func declareResource(t *testing.T, url, name string, typ resourcesv1.ResourceType) {
	t.Helper()
	client := resourcesv1connect.NewResourceServiceClient(testClient, url)
	req := &resourcesv1.DeclareRequest{Resource: &resourcesv1.ResourceIdentifier{Name: name, Type: typ}}
	switch typ {
	case resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES:
		req.Config = &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{}}
	case resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET:
		req.Config = &resourcesv1.DeclareRequest_Bucket{Bucket: &resourcesv1.BucketConfig{}}
	}
	if _, err := client.Declare(context.Background(), req); err != nil {
		t.Fatalf("Declare %s: %v", name, err)
	}
}

func TestACallToTheDevServerWithNoDiscoveryTokenIsRefused(t *testing.T) {
	t.Parallel()

	url := serve(t, newDevServer(&fakeResources{}))

	_, err := resourcesv1connect.NewResourceServiceClient(bareClient, url).Declare(
		context.Background(),
		&resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
			Config:   &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
		},
	)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("Declare error = %v, want permission denied", err)
	}

	resp, err := bareClient.Post(url+"/sync", "application/octet-stream", nil)
	if err != nil {
		t.Fatalf("POST /sync: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /sync status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestAnAppRouteAnswersOnlyTheAppTokenTheChildWasHanded(t *testing.T) {
	t.Parallel()

	s := newDevServer(&fakeResources{})
	s.PushEnv(map[string]string{"SECRET": "hunter2"})
	url := serve(t, s)

	for name, client := range map[string]*http.Client{
		"no token":            bareClient,
		"the discovery token": testClient,
	} {
		t.Run(name+" is refused", func(t *testing.T) {
			t.Parallel()

			resp, err := client.Get(url + "/env")
			if err != nil {
				t.Fatalf("GET /env: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("GET /env status = %d, want %d", resp.StatusCode, http.StatusForbidden)
			}

			_, err = bucketv1connect.NewBucketServiceClient(client, url).GetUploadStatus(
				context.Background(),
				&bucketv1.GetUploadStatusRequest{SessionId: "sess_1"},
			)
			if connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Errorf("GetUploadStatus error = %v, want permission denied", err)
			}
		})
	}

	t.Run("the app token is refused by the discovery routes", func(t *testing.T) {
		t.Parallel()

		_, err := resourcesv1connect.NewResourceServiceClient(appClient, url).Declare(
			context.Background(),
			&resourcesv1.DeclareRequest{
				Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
				Config:   &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
			},
		)
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("Declare error = %v, want permission denied", err)
		}

		resp, err := appClient.Post(url+"/sync", "application/octet-stream", nil)
		if err != nil {
			t.Fatalf("POST /sync: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST /sync status = %d, want %d", resp.StatusCode, http.StatusForbidden)
		}
	})

	t.Run("a request naming another host is refused even with the app token", func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequest(http.MethodGet, url+"/env", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Host = "dev.example.com"
		resp, err := appClient.Do(req)
		if err != nil {
			t.Fatalf("GET /env: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET /env status = %d, want %d", resp.StatusCode, http.StatusForbidden)
		}
	})

	t.Run("the app token opens the env stream", func(t *testing.T) {
		t.Parallel()

		if got := readEnvEvent(t, openEnvStream(t, url))["SECRET"]; got != "hunter2" {
			t.Fatalf("env SECRET = %q, want %q", got, "hunter2")
		}
	})
}

func TestTheTaskAndTopicServicesOfTheDevResourcesAnswerOnlyTheAppToken(t *testing.T) {
	t.Parallel()

	resources := devresources.New("shop", devresources.Options{Open: (&dockertest.Engine{}).OpenFunc(), StateDir: t.TempDir()})
	url := serve(t, New("http://127.0.0.1:0", resources))

	for name, client := range map[string]*http.Client{"no token": bareClient, "the discovery token": testClient} {
		_, err := taskv1connect.NewTaskServiceClient(client, url).Trigger(context.Background(), &taskv1.TriggerRequest{Task: "greet"})
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("Trigger with %s = %v, want permission denied", name, err)
		}
		_, err = topicv1connect.NewTopicServiceClient(client, url).Send(context.Background(), &topicv1.SendRequest{Topic: "orders"})
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("Send with %s = %v, want permission denied", name, err)
		}
	}

	_, err := taskv1connect.NewTaskServiceClient(appClient, url).Trigger(context.Background(), &taskv1.TriggerRequest{Task: "greet"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("Trigger with the app token = %v, want it past the guard and refused for want of a declared task", err)
	}
	_, err = topicv1connect.NewTopicServiceClient(appClient, url).Send(context.Background(), &topicv1.SendRequest{Topic: "orders"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("Send with the app token = %v, want it past the guard and refused for want of a declared topic", err)
	}
}

func TestTheSyncResultIncludesTheTokenTheAppReachesTheDevServerWith(t *testing.T) {
	t.Parallel()

	s := newDevServer(&fakeResources{})
	url := serve(t, s)

	if status := postSync(t, url); status != http.StatusOK {
		t.Fatalf("POST /sync status = %d, want %d", status, http.StatusOK)
	}
	result := <-s.SyncResults()
	if result.AppToken != testAppToken {
		t.Fatalf("AppToken = %q, want the token the app routes answer to", result.AppToken)
	}
	if result.AppToken == s.DiscoveryTarget().Token {
		t.Fatal("the app token is the discovery token, so an app that leaks its environment also hands out the discovery routes")
	}
}

func postSync(t *testing.T, url string) int {
	t.Helper()
	resp, err := testClient.Post(url+"/sync", "application/octet-stream", nil)
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
		s := newDevServer(&fakeResources{})

		_, err := s.declarations.Declare(context.Background(), &resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "main"},
		})
		if err == nil {
			t.Fatal("Declare: expected error for unspecified resource type, got nil")
		}
	})
}

func TestSync(t *testing.T) {
	t.Parallel()

	t.Run("hands every declaration, config and all, to the dev resources and delivers what it resolved", func(t *testing.T) {
		t.Parallel()
		resources := &fakeResources{}
		s := newDevServer(resources)
		url := serve(t, s)

		declareResource(t, url, "main", resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES)
		declareResource(t, url, "storage", resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET)

		if status := postSync(t, url); status != http.StatusOK {
			t.Fatalf("POST /sync status = %d, want 200", status)
		}

		result := <-s.SyncResults()
		if result.Err != nil {
			t.Fatalf("Sync result error: %v", result.Err)
		}
		if len(resources.asked) != 1 || len(resources.asked[0]) != 2 {
			t.Fatalf("the dev resources were asked %+v, want one call passing both declarations", resources.asked)
		}
		if main := resources.asked[0][0]; main.Name != "main" || main.Postgres == nil {
			t.Errorf("the dev resources saw %+v, want main with its postgres config", main)
		}
		if storage := resources.asked[0][1]; storage.Name != "storage" || storage.Bucket == nil {
			t.Errorf("the dev resources saw %+v, want storage with its bucket config, through the same door as every other kind", storage)
		}
		if len(result.Resources) != 2 || result.Resources[0].Env["BOUND_main"] != "yes" || result.Resources[1].Env["BOUND_storage"] != "yes" {
			t.Fatalf("Resources = %+v, want what the dev resources resolved", result.Resources)
		}
	})

	t.Run("only sees resources declared after a reset", func(t *testing.T) {
		t.Parallel()
		s := newDevServer(&fakeResources{})
		url := serve(t, s)

		declareResource(t, url, "stale", resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES)
		s.ResetDeclarations()
		declareResource(t, url, "main", resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES)

		postSync(t, url)

		result := <-s.SyncResults()
		if len(result.Resources) != 1 || result.Resources[0].Name != "main" {
			t.Fatalf("Resources = %+v, want only the entry declared after the reset", result.Resources)
		}
	})

	t.Run("refuses a method other than POST", func(t *testing.T) {
		t.Parallel()
		url := serve(t, newDevServer(&fakeResources{}))

		resp, err := testClient.Get(url + "/sync")
		if err != nil {
			t.Fatalf("GET /sync: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET /sync status = %d, want 405", resp.StatusCode)
		}
	})

	t.Run("propagates what the dev resources refused", func(t *testing.T) {
		t.Parallel()
		s := newDevServer(&fakeResources{resolve: func([]declaration.Resource) ([]binding.Resolved, error) {
			return nil, errors.New("boom")
		}})
		url := serve(t, s)

		if status := postSync(t, url); status != http.StatusInternalServerError {
			t.Fatalf("POST /sync status = %d, want 500", status)
		}

		result := <-s.SyncResults()
		if result.Err == nil || !strings.Contains(result.Err.Error(), "boom") {
			t.Fatalf("Sync result = %v, want the dev resources' error", result.Err)
		}
	})

	t.Run("serves a secret-class value from the values it was given, like every other value", func(t *testing.T) {
		t.Parallel()
		s := newDevServer(&fakeResources{})
		s.UseValues(map[string]string{"WEBHOOK_SECRET": "whsec_from_the_dotfile", "POSTHOG_ID": "ph_1"}, variables.Scope{})
		url := serve(t, s)

		declareEnv(t, url,
			&resourcesv1.VariableDefinition{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
			&resourcesv1.VariableDefinition{Key: "WEBHOOK_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET},
		)
		if status := postSync(t, url); status != http.StatusOK {
			t.Fatalf("POST /sync status = %d, want 200", status)
		}

		result := <-s.SyncResults()
		if result.Err != nil {
			t.Fatalf("Sync result error: %v", result.Err)
		}
		if want := []string{"WEBHOOK_SECRET"}; !slices.Equal(result.SecretKeys, want) {
			t.Errorf("SecretKeys = %v, want %v", result.SecretKeys, want)
		}
		if got := result.SecretValues["WEBHOOK_SECRET"]; got != "whsec_from_the_dotfile" {
			t.Errorf("SecretValues[WEBHOOK_SECRET] = %q, want the value it was given", got)
		}
		if _, ok := result.SecretValues["POSTHOG_ID"]; ok {
			t.Errorf("SecretValues = %v, want no entry for a key that is not secret", result.SecretValues)
		}
	})

	t.Run("names a secret key it has no value for rather than resolving it", func(t *testing.T) {
		t.Parallel()
		s := newDevServer(&fakeResources{})
		s.UseValues(map[string]string{}, variables.Scope{})
		url := serve(t, s)

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "WEBHOOK_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
		})
		postSync(t, url)

		result := <-s.SyncResults()
		if result.Err != nil {
			t.Fatalf("Sync result error: %v", result.Err)
		}
		if want := []string{"WEBHOOK_SECRET"}; !slices.Equal(result.SecretKeys, want) {
			t.Errorf("SecretKeys = %v, want %v", result.SecretKeys, want)
		}
		if len(result.SecretValues) != 0 {
			t.Errorf("SecretValues = %v, want nothing for a key nothing sets", result.SecretValues)
		}
	})

	t.Run("forgets secret keys a declaration no longer names after a reset", func(t *testing.T) {
		t.Parallel()
		s := newDevServer(&fakeResources{})
		s.UseValues(map[string]string{"GONE": "a", "KEPT": "b"}, variables.Scope{})
		url := serve(t, s)

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "GONE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
		})

		s.ResetDeclarations()

		declareEnv(t, url, &resourcesv1.VariableDefinition{
			Key: "KEPT", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
		})

		postSync(t, url)
		result := <-s.SyncResults()

		if want := []string{"KEPT"}; !slices.Equal(result.SecretKeys, want) {
			t.Errorf("SecretKeys = %v, want only %v — the reset dropped the prior declaration", result.SecretKeys, want)
		}
	})
}

func TestEnvStream(t *testing.T) {
	t.Parallel()

	t.Run("receives env pushed after connecting", func(t *testing.T) {
		t.Parallel()
		s := newDevServer(&fakeResources{})
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
		s := newDevServer(&fakeResources{})
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
	resp, err := appClient.Do(req)
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
