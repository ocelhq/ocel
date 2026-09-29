package deploy

import (
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func projectRegistry(cfg *projectconfig.Config) (*contractv1.ImageRegistry, error) {
	if cfg.Registry == nil || len(cfg.Apps) == 0 {
		return nil, nil
	}
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

func requireProjectRegistryPassword(cfg *projectconfig.Config) error {
	if cfg.Registry == nil || len(cfg.Apps) == 0 {
		return nil
	}
	_, err := projectRegistryPassword(cfg)
	return err
}

func projectRegistryPassword(cfg *projectconfig.Config) (string, error) {
	lookup, err := projectconfig.EnvLookup(cfg.Dir)
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
