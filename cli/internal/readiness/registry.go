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
		return nil, fmt.Errorf("%s, the password of registry %s, is unset: set it in the environment or .env, or drop `registry` from the config",
			cfg.Registry.Password, cfg.Registry.Server)
	}
	return imageRegistry(cfg.Registry, password), nil
}

func RemovalRegistry(cfg *project.Project) (registry *contractv1.ImageRegistry, warning string) {
	if cfg.Registry == nil {
		return nil, ""
	}
	password, err := projectRegistryPassword(cfg)
	if err != nil {
		return nil, fmt.Sprintf("The images pushed to %s will stay there: %v",
			cfg.Registry.Server, err)
	}
	if password == "" {
		return nil, fmt.Sprintf("The images pushed to %s will stay there: set %s before removing to delete them",
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
