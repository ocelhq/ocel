package provider_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestAStackSpecWithARegistryRendersWithoutItsPassword(t *testing.T) {
	target := images.Registry{Server: "ghcr.io", Namespace: "acme", Username: "acme-bot", Password: "ghp_livesecret"}
	spec := provider.StackSpec{
		Kind: provider.StackApp,
		Images: provider.ImagePushes{
			Store:  images.RegistryStore(target),
			Pushes: []images.Push{{App: "web", Source: "ocel/web@sha256:abc", ImageRef: target.ImageRef("web", "sha256-abc"), Digest: "sha256:abc"}},
		},
	}
	unrendered := provider.StackSpec{
		Kind:   provider.StackApp,
		Images: provider.ImagePushes{Store: keptSecret{password: "ghp_livesecret"}, Pushes: spec.Images.Pushes},
	}

	for _, rendered := range []string{
		fmt.Sprintf("%v", spec),
		fmt.Sprintf("%+v", spec),
		fmt.Sprintf("%#v", spec),
		fmt.Sprintf("%+v", spec.Images),
		fmt.Sprintf("%#v", spec.Images.Store),
		fmt.Sprintf("%+v", unrendered),
		fmt.Sprintf("%#v", unrendered.Images),
	} {
		if strings.Contains(rendered, "ghp_livesecret") {
			t.Errorf("a stack spec rendered as %q, and the registry password rides along into any log line that prints one", rendered)
		}
		if !strings.Contains(rendered, "ghcr.io") {
			t.Errorf("a stack spec rendered as %q, want it to still name the registry its images are pushed to", rendered)
		}
	}
}

func TestASpecWithAnAppsValuesRendersWithoutThem(t *testing.T) {
	values := provider.AppValues{
		Plain:     map[string]string{"REGION": "eu-west-1"},
		Sensitive: map[string]string{"API_TOKEN": "sk-live-secret"},
		Secrets:   []provider.SecretRef{{Key: "DATABASE_URL"}},
		ContainerEnv: map[string]string{
			"REGION":                        "eu-west-1",
			"API_TOKEN":                     "sk-live-secret",
			"DATABASE_URL":                  "postgres://app:hunter2@db.internal/orders",
			"OCEL_RESOURCE_POSTGRES_orders": `{"postgres":{"password":"hunter2"}}`,
		},
	}
	app := provider.AppSpec{App: "web", Values: values}
	spec := provider.StackSpec{Kind: provider.StackApp, App: &app}

	for _, rendered := range []string{
		fmt.Sprintf("%v", values),
		fmt.Sprintf("%+v", values),
		fmt.Sprintf("%#v", values),
		fmt.Sprintf("%v", app),
		fmt.Sprintf("%+v", app),
		fmt.Sprintf("%#v", app),
		fmt.Sprintf("%+v", *spec.App),
		fmt.Errorf("release %v: %w", app, errors.New("denied")).Error(),
	} {
		for _, secret := range []string{"sk-live-secret", "hunter2", "postgres://app"} {
			if strings.Contains(rendered, secret) {
				t.Errorf("an app's values rendered as %q, and %s rides along into any log line, error wrap or panic dump that prints one", rendered, secret)
			}
		}
		if !strings.Contains(rendered, "API_TOKEN") {
			t.Errorf("an app's values rendered as %q, want it to still name what the app was handed", rendered)
		}
	}
}

type keptSecret struct{ password string }

func (keptSecret) Has(context.Context, images.Push) (bool, error) { return false, nil }

func (keptSecret) Destination() string { return "the kept registry" }

func (keptSecret) Push(context.Context, images.Push, edge.Progress) error {
	return nil
}
