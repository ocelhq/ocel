package ports_test

import (
	"context"
	"net/http"
	"testing"

	"google.golang.org/api/option"
	pubsub "google.golang.org/api/pubsub/v1"
	runv1 "google.golang.org/api/run/v1"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func TestAGoogleRESTCallIsASpanNamedForTheAPIMethodItCalls(t *testing.T) {
	for _, call := range []struct {
		send            func(ctx context.Context) error
		service, method string
	}{
		{
			send: func(ctx context.Context) error {
				topics, err := ports.NewRESTService(ctx, ports.Credentials{}, pubsub.NewService, "", "")
				if err != nil {
					return err
				}
				_, err = topics.Projects.Topics.Publish("projects/acme/topics/orders", &pubsub.PublishRequest{}).Context(ctx).Do()
				return err
			},
			service: "pubsub", method: "projects.topics.publish",
		},
		{
			send: func(ctx context.Context) error {
				services, err := ports.NewRESTService(ctx, ports.Credentials{}, run.NewService, "", "")
				if err != nil {
					return err
				}
				_, err = services.Projects.Locations.Services.Get("projects/acme/locations/europe-west1/services/web").Context(ctx).Do()
				return err
			},
			service: "run", method: "projects.locations.services.get",
		},
		{
			send: func(ctx context.Context) error {
				services, err := ports.NewRESTService(ctx, ports.Credentials{}, run.NewService, "", "")
				if err != nil {
					return err
				}
				_, err = services.Projects.Locations.Services.List("projects/acme/locations/europe-west1").Context(ctx).Do()
				return err
			},
			service: "run", method: "projects.locations.services.list",
		},
		{
			send: func(ctx context.Context) error {
				services, err := ports.NewRESTService(ctx, ports.Credentials{}, run.NewService, "", "")
				if err != nil {
					return err
				}
				_, err = services.Projects.Locations.Services.Patch("projects/acme/locations/europe-west1/services/web", &run.GoogleCloudRunV2Service{}).Context(ctx).Do()
				return err
			},
			service: "run", method: "projects.locations.services.patch",
		},
		{
			send: func(ctx context.Context) error {
				services, err := ports.NewRESTService(ctx, ports.Credentials{}, runv1.NewService, "", "", option.WithEndpoint("https://europe-west1-run.googleapis.com/"))
				if err != nil {
					return err
				}
				_, err = services.Namespaces.Services.ReplaceService("namespaces/acme/services/web", &runv1.Service{}).Context(ctx).Do()
				return err
			},
			service: "run", method: "namespaces.services.update",
		},
	} {
		t.Run(call.service+"/"+call.method, func(t *testing.T) {
			googletest.UseUserCredential(t)
			googletest.ServeAsGoogle(t, func(w http.ResponseWriter, _ *http.Request) {
				googletest.WriteBody(w, map[string]any{})
			})
			ctx, spans := tracingtest.Record(t)

			if err := call.send(ctx); err != nil {
				t.Fatal(err)
			}

			sent := spans.Only(t)
			if want := call.service + "/" + call.method; sent.Name() != want {
				t.Errorf("the call's span is named %q, want %q", sent.Name(), want)
			}
			for key, want := range map[string]string{"rpc.system": "gcp-api", "rpc.service": call.service, "rpc.method": call.method} {
				if got := tracingtest.Attributes(sent)[key]; got.AsString() != want {
					t.Errorf("%s = %q, want %q", key, got.AsString(), want)
				}
			}
		})
	}
}
