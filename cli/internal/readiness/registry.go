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
	password, err := projectRegistryPassword(cfg)
	if err != nil {
		return nil, err
	}
	if password == "" {
		return nil, fmt.Errorf("the registry %s is pushed to authenticates with the environment variable %s, which is unset here: "+
			"export it or set it in the project's .env before deploying, or drop `registry` from the config to push nowhere",
			cfg.Registry.Server, cfg.Registry.Password)
	}
	return imageRegistry(cfg.Registry, password), nil
}

func RemovalRegistry(cfg *project.Project) (registry *contractv1.ImageRegistry, warning string) {
	if cfg.Registry == nil {
		return nil, ""
	}
	password, err := projectRegistryPassword(cfg)
	if err != nil {
		return nil, fmt.Sprintf("The images this project pushed to %s stay there until they are deleted in the registry: %v",
			cfg.Registry.Server, err)
	}
	if password == "" {
		return nil, fmt.Sprintf("The images this project pushed to %s stay there until they are deleted in the registry: "+
			"it authenticates with the environment variable %s, which is unset here; "+
			"export it or set it in the project's .env before removing for the removal to delete them",
			cfg.Registry.Server, cfg.Registry.Password)
	}
	return imageRegistry(cfg.Registry, password), ""
}

func imageRegistry(registry *project.Registry, password string) *contractv1.ImageRegistry {
	return &contractv1.ImageRegistry{
		Server:    registry.Server,
		Namespace: registry.Namespace,
		Username:  registry.Username,
		Password:  password,
	}
}

func projectRegistryPassword(cfg *project.Project) (string, error) {
	lookup, err := project.EnvLookup(cfg.Dir)
	if err != nil {
		return "", err
	}
	password, _ := lookup(cfg.Registry.Password)
	return password, nil
}
