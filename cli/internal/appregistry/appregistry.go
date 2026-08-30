package appregistry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/appimages"
	"github.com/ocelhq/ocel/cli/internal/imagebuild"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/runui"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Target struct {
	Server    string
	Namespace string
	Username  string
	Password  string
}

func (t Target) String() string {
	return fmt.Sprintf("registry %s namespace %q username %q password [redacted]", t.Server, t.Namespace, t.Username)
}

func (t Target) GoString() string { return t.String() }

func (t Target) Coordinate(repository, tag string) string {
	parts := []string{t.Server}
	if t.Namespace != "" {
		parts = append(parts, strings.Trim(t.Namespace, "/"))
	}
	return strings.Join(append(parts, repository), "/") + ":" + tag
}

type Host interface {
	ResolveImageRegistry(ctx context.Context, req *contractv1.ResolveImageRegistryRequest) (*contractv1.ResolveImageRegistryResponse, error)
}

func repositories(cfg *projectconfig.Config) ([]string, error) {
	apps := appimages.Apps(cfg)
	repositories := make([]string, 0, len(apps))
	for _, app := range apps {
		repository, err := imagebuild.Repository(app.Name)
		if err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, nil
}

func RequireSecret(cfg *projectconfig.Config) error {
	if cfg.Registry == nil || len(appimages.Apps(cfg)) == 0 {
		return nil
	}
	_, err := secret(cfg.Registry)
	return err
}

func Resolve(ctx context.Context, cfg *projectconfig.Config, host Host) (Target, bool, error) {
	if cfg.Registry != nil {
		password, err := secret(cfg.Registry)
		if err != nil {
			return Target{}, false, err
		}
		return Target{
			Server:    cfg.Registry.Server,
			Namespace: cfg.Registry.Namespace,
			Username:  cfg.Registry.Username,
			Password:  password,
		}, true, nil
	}
	pushing, err := repositories(cfg)
	if err != nil {
		return Target{}, false, err
	}
	if len(pushing) == 0 {
		return Target{}, false, nil
	}
	resp, err := host.ResolveImageRegistry(ctx, &contractv1.ResolveImageRegistryRequest{Repositories: pushing})
	if connect.CodeOf(err) == connect.CodeUnimplemented {
		return Target{}, false, nil
	}
	if err != nil {
		return Target{}, false, fmt.Errorf("resolve the registry this provider hosts: %w", err)
	}
	return Target{
		Server:    resp.GetServer(),
		Namespace: resp.GetNamespace(),
		Username:  resp.GetUsername(),
		Password:  resp.GetPassword(),
	}, true, nil
}

func Demand(ctx context.Context, cfg *projectconfig.Config, host Host) (Target, error) {
	target, named, err := Resolve(ctx, cfg, host)
	if err != nil {
		return Target{}, err
	}
	if named {
		return target, nil
	}
	pushing, err := repositories(cfg)
	if err != nil {
		return Target{}, err
	}
	return Target{}, missing(cfg, pushing)
}

func missing(cfg *projectconfig.Config, pushing []string) error {
	return fmt.Errorf("the image for %s is served by pulling it from a registry, and nothing names one: this provider hosts none of its own, "+
		"and %s names no `registry`.\n    → add `registry: { server: \"ghcr.io/your-org\", username: \"…\", password: \"REGISTRY_TOKEN\" }` to %s, "+
		"where `password` is the name of the environment variable holding the token",
		runui.Quoted(pushing), filepath.Base(cfg.Path), filepath.Base(cfg.Path))
}

func secret(registry *projectconfig.Registry) (string, error) {
	password := os.Getenv(registry.Password)
	if password == "" {
		return "", fmt.Errorf("the registry %s is pushed to authenticates with the environment variable %s, which is unset here: "+
			"export it before deploying, or drop `registry` from the config to push nowhere",
			registry.Server, registry.Password)
	}
	return password, nil
}
