package appregistry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type provider struct {
	mu     sync.Mutex
	asked  [][]string
	answer *contractv1.ResolveImageRegistryResponse
	err    error
}

func (p *provider) ResolveImageRegistry(_ context.Context, req *contractv1.ResolveImageRegistryRequest) (*contractv1.ResolveImageRegistryResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, req.GetRepositories())
	if p.err != nil {
		return nil, p.err
	}
	return p.answer, nil
}

func (p *provider) calls() [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asked
}

func hostless() *provider {
	return &provider{err: connect.NewError(connect.CodeUnimplemented, errors.New("no registry here"))}
}

func hosting() *provider {
	return &provider{answer: &contractv1.ResolveImageRegistryResponse{
		Server:    "native.invalid",
		Namespace: "ocel/acme",
		Username:  "native-bot",
		Password:  "native-token",
	}}
}

func project(registry *projectconfig.Registry) *projectconfig.Config {
	return &projectconfig.Config{
		Path:     "/repo/ocel.config.ts",
		Slug:     "shop",
		Apps:     []projectconfig.App{{Name: "web", Path: "services/web", Compute: "container"}},
		Registry: registry,
	}
}

func TestAProjectRegistryIsUsedWithoutAskingTheProvider(t *testing.T) {
	t.Setenv("GHCR_TOKEN", "hunter2")
	native := hosting()

	target, named, err := Resolve(context.Background(), project(&projectconfig.Registry{
		Server: "ghcr.io", Username: "acme-bot", Password: "GHCR_TOKEN",
	}), native)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if !named {
		t.Fatal("Resolve() found no registry where the project names one")
	}
	if target.Server != "ghcr.io" || target.Username != "acme-bot" || target.Password != "hunter2" {
		t.Errorf("Resolve() = %+v, want the project's own registry with its password read from the environment", target)
	}
	if calls := native.calls(); len(calls) != 0 {
		t.Errorf("the provider was asked %v for a registry the project already named, so the override crossed the provider boundary", calls)
	}
}

func TestAProjectRegistryCarriesTheNamespaceItsImagesLandUnder(t *testing.T) {
	t.Setenv("GHCR_TOKEN", "hunter2")

	target, _, err := Resolve(context.Background(), project(&projectconfig.Registry{
		Server: "ghcr.io", Namespace: "acme", Password: "GHCR_TOKEN",
	}), hosting())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if got, want := target.Coordinate("web", "sha256-abc"), "ghcr.io/acme/web:sha256-abc"; got != want {
		t.Errorf("Coordinate() = %q, want %q — ghcr takes no repository at its root", got, want)
	}
}

func TestAProviderNativeRegistryIsUsedWhenTheProjectNamesNone(t *testing.T) {
	native := hosting()
	cfg := project(nil)
	cfg.Apps = append(cfg.Apps, projectconfig.App{Name: "api", Path: "services/api", Compute: "container"})

	target, named, err := Resolve(context.Background(), cfg, native)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if !named {
		t.Fatal("Resolve() found no registry where the provider hosts one")
	}
	if target.Server != "native.invalid" || target.Namespace != "ocel/acme" || target.Password != "native-token" {
		t.Errorf("Resolve() = %+v, want the provider's own registry", target)
	}
	calls := native.calls()
	if len(calls) != 1 || strings.Join(calls[0], ",") != "web,api" {
		t.Errorf("the provider was asked %v, want one resolve carrying the repositories the deploy intends to push", calls)
	}
}

func TestAProviderThatHostsNoRegistryLeavesTheDeployWithNone(t *testing.T) {
	target, named, err := Resolve(context.Background(), project(nil), hostless())
	if err != nil {
		t.Fatalf("Resolve() error = %v, want an unimplemented resolve read as no registry", err)
	}
	if named {
		t.Errorf("Resolve() = %+v, want nothing where neither side names a registry", target)
	}
}

func TestAProviderThatFailsTheResolveFailsTheDeploy(t *testing.T) {
	broken := &provider{err: connect.NewError(connect.CodeInternal, errors.New("the repository could not be created"))}

	if _, _, err := Resolve(context.Background(), project(nil), broken); err == nil {
		t.Fatal("Resolve() swallowed a provider that failed to answer, want the deploy stopped")
	}
}

func TestAProjectPushingNoImageAsksTheProviderForNoRegistry(t *testing.T) {
	native := hosting()
	cfg := project(nil)
	cfg.Apps = []projectconfig.App{{Name: "api", Compute: "serverless", Framework: "express"}}

	target, named, err := Resolve(context.Background(), cfg, native)
	if err != nil {
		t.Fatalf("Resolve() error = %v, want a deploy that pushes nothing to want no registry", err)
	}
	if named {
		t.Errorf("Resolve() = %+v, want nothing where no app is pushed anywhere", target)
	}
	if calls := native.calls(); len(calls) != 0 {
		t.Errorf("the provider was asked %v to resolve a registry for no repository at all", calls)
	}
}

func TestARegistryWhoseVariableIsUnsetIsRefusedBeforeAnythingIsBuilt(t *testing.T) {
	cfg := project(&projectconfig.Registry{Server: "ghcr.io", Password: "GHCR_TOKEN"})

	err := RequireSecret(cfg)
	if err == nil {
		t.Fatal("RequireSecret() passed with the registry's variable unset, so the deploy would build before discovering it cannot push")
	}
	for _, want := range []string{"GHCR_TOKEN", "ghcr.io"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("RequireSecret() error = %v, want it to mention %q", err, want)
		}
	}
}

func TestARegistryWhoseVariableIsEmptyIsRefusedToo(t *testing.T) {
	t.Setenv("GHCR_TOKEN", "")
	cfg := project(&projectconfig.Registry{Server: "ghcr.io", Password: "GHCR_TOKEN"})

	if err := RequireSecret(cfg); err == nil {
		t.Fatal("RequireSecret() passed with the registry's variable empty, which authenticates as nobody")
	}
}

func TestAProjectWithNoContainerAppIsAskedForNoSecret(t *testing.T) {
	cfg := project(&projectconfig.Registry{Server: "ghcr.io", Password: "GHCR_TOKEN"})
	cfg.Apps = []projectconfig.App{{Name: "api", Compute: "serverless"}}

	if err := RequireSecret(cfg); err != nil {
		t.Errorf("RequireSecret() = %v, want a deploy that pushes no image to demand no secret", err)
	}
}

func TestAProjectWithNoRegistryIsAskedForNoSecret(t *testing.T) {
	if err := RequireSecret(project(nil)); err != nil {
		t.Errorf("RequireSecret() = %v, want a project that names no registry to demand nothing", err)
	}
}

func TestTheRepositoriesADeployPushesAreItsContainerApps(t *testing.T) {
	cfg := project(nil)
	cfg.Apps = append(cfg.Apps,
		projectconfig.App{Name: "api", Compute: "serverless", Framework: "express"},
		projectconfig.App{Name: "Worker Queue", Compute: "container"},
	)

	pushing, err := repositories(cfg)
	if err != nil {
		t.Fatalf("repositories() error = %v", err)
	}
	if strings.Join(pushing, ",") != "web,worker-queue" {
		t.Errorf("repositories() = %v, want one repository per container app and none for a serverless one", pushing)
	}
}

func TestThePasswordIsReadWhereTheTargetIsBuiltAndNowhereEarlier(t *testing.T) {
	cfg := project(&projectconfig.Registry{Server: "ghcr.io", Password: "GHCR_TOKEN"})

	t.Setenv("GHCR_TOKEN", "the-one-checked-at-preflight")
	if err := RequireSecret(cfg); err != nil {
		t.Fatalf("RequireSecret() = %v", err)
	}

	t.Setenv("GHCR_TOKEN", "the-one-the-push-uses")
	target, _, err := Resolve(context.Background(), cfg, hosting())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if target.Password != "the-one-the-push-uses" {
		t.Errorf("Resolve() carried %q, want the value the variable holds when the target is built: a password held from the plan-time check is one the deploy cannot let the user correct", target.Password)
	}
}
