package gcloud_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	runv1 "google.golang.org/api/run/v1"
	run "google.golang.org/api/run/v2"
	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestAFailedProjectNumberReadIsAskedAgainByTheNextCaller(t *testing.T) {
	t.Parallel()

	var reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if reads.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"code":403,"message":"permission still propagating"}}`))
			return
		}
		w.Write([]byte(`{"projectId":"acme-prod","projectNumber":"123456789"}`))
	}))
	t.Cleanup(server.Close)
	c := &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1", Endpoint: server.URL}
	const domain = "@gcp-sa-pubsub.iam.gserviceaccount.com"

	if _, err := c.ReadServiceAgent(context.Background(), domain); err == nil {
		t.Fatal("ReadServiceAgent succeeded on a refused project read")
	}
	got, err := c.ReadServiceAgent(context.Background(), domain)
	if err != nil {
		t.Fatalf("ReadServiceAgent after the refusal cleared: %v", err)
	}
	if want := "serviceAccount:service-123456789" + domain; got != want {
		t.Errorf("ReadServiceAgent = %q, want %q", got, want)
	}
	if _, err := c.ReadServiceAgent(context.Background(), domain); err != nil {
		t.Fatalf("ReadServiceAgent once the number is known: %v", err)
	}
	if n := reads.Load(); n != 2 {
		t.Errorf("the project was read %d times, want 2: once refused, once remembered", n)
	}
}

func TestLoggingOpensOneSharedClientAgainstTheEmulator(t *testing.T) {
	t.Parallel()

	c := &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1", Endpoint: "http://127.0.0.1:1"}

	first, err := c.Logging()
	if err != nil {
		t.Fatalf("Logging: %v", err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := c.Logging()
	if err != nil {
		t.Fatalf("Logging again: %v", err)
	}
	if first != second {
		t.Error("Logging opened a second client, want the first remembered")
	}
}

const sensitiveValue = "correct-horse-battery-staple"

func refuseEchoing(w http.ResponseWriter, r *http.Request) {
	sent, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"error":{"code":400,"message":"invalid request: ` + strings.ReplaceAll(string(sent), `"`, `'`) + `"}}`))
}

func TestACallThatFailsIsTracedWithoutTheSecretItCarries(t *testing.T) {
	for _, call := range []struct {
		name string
		send func(ctx context.Context, c *gcloud.Clients) error
	}{
		{"a service update", func(ctx context.Context, c *gcloud.Clients) error {
			services, err := c.Run()
			if err != nil {
				return err
			}
			_, err = services.Projects.Locations.Services.Patch(c.ServicePath("web"), &run.GoogleCloudRunV2Service{
				Template: &run.GoogleCloudRunV2RevisionTemplate{Containers: []*run.GoogleCloudRunV2Container{{
					Env: []*run.GoogleCloudRunV2EnvVar{{Name: "SESSION_KEY", Value: sensitiveValue}},
				}}},
			}).Context(ctx).Do()
			return err
		}},
		{"a v1 service replacement", func(ctx context.Context, c *gcloud.Clients) error {
			services, err := c.RunV1()
			if err != nil {
				return err
			}
			_, err = services.Namespaces.Services.ReplaceService("namespaces/"+googletest.Project+"/services/web", &runv1.Service{
				Spec: &runv1.ServiceSpec{Template: &runv1.RevisionTemplate{Spec: &runv1.RevisionSpec{Containers: []*runv1.Container{{
					Env: []*runv1.EnvVar{{Name: "SESSION_KEY", Value: sensitiveValue}},
				}}}}},
			}).Context(ctx).Do()
			return err
		}},
		{"a Cloud SQL password change", func(ctx context.Context, c *gcloud.Clients) error {
			instances, err := c.SQL()
			if err != nil {
				return err
			}
			_, err = instances.Users.Update(googletest.Project, "web", &sqladmin.User{Password: sensitiveValue}).Name("postgres").Context(ctx).Do()
			return err
		}},
	} {
		t.Run(call.name, func(t *testing.T) {
			googletest.UseUserCredential(t)
			googletest.ServeAsGoogle(t, refuseEchoing)
			ctx, spans := tracingtest.Record(t)

			if err := call.send(ctx, &gcloud.Clients{Names: googletest.Names, Region: googletest.Region}); err == nil {
				t.Fatal("the call succeeded, want Google's refusal")
			}

			for _, span := range spans.Ended() {
				for _, keyValue := range span.Attributes() {
					if strings.Contains(keyValue.Value.String(), sensitiveValue) {
						t.Errorf("span %s holds %s = %s, a secret the call carried", span.Name(), keyValue.Key, keyValue.Value.String())
					}
				}
			}
		})
	}
}
