package readiness

import (
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/project"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func ProjectRegistry(cfg *project.Project) (*contractv1.ImageRegistry, error) {
	if cfg.Registry == nil || len(cfg.Apps) == 0 {
		return nil, nil
	}
	return authenticatedRegistry(cfg)
}

func RemovalRegistry(cfg *project.Project) (registry *contractv1.ImageRegistry, warning string) {
	if cfg.Registry == nil {
		return nil, ""
	}
	registry, err := authenticatedRegistry(cfg)
	if err != nil {
		return nil, fmt.Sprintf("The images this project pushed to %s stay there until they are deleted in the registry: "+
			"its password is read from %s, which is unset here", cfg.Registry.Server, cfg.Registry.Password)
	}
	return registry, ""
}

func authenticatedRegistry(cfg *project.Project) (*contractv1.ImageRegistry, error) {
	password, err := projectRegistryPassword(cfg)
	if err != nil {
		return nil, err
	}
	return &contractv1.ImageRegistry{
		Server:    cfg.Registry.Server,
		Namespace: cfg.Registry.Namespace,
		Username:  cfg.Registry.Username,
		Password:  password,
	}, nil
}

func projectRegistryPassword(cfg *project.Project) (string, error) {
	lookup, err := project.EnvLookup(cfg.Dir)
	if err != nil {
		return "", err
	}
	registry := cfg.Registry
	password, _ := lookup(registry.Password)
	if password == "" {
		return "", fmt.Errorf("the registry %s is pushed to authenticates with the environment variable %s, which is unset here: "+
			"export it or set it in the project's .env before deploying, or drop `registry` from the config to push nowhere",
			registry.Server, registry.Password)
	}
	return password, nil
}
