package connectorkit

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
)

type Config struct {
	Console        string   `json:"console"`
	ConnectorID    string   `json:"connectorId"`
	OrganizationID string   `json:"organizationId"`
	Grants         []string `json:"grants"`
}

var grantable = []string{CapabilityEnvVarsRead, CapabilityEnvVarsWrite, CapabilityEnvVarsReveal}

func LoadConfig(path string) (Config, error) {
	if path == "" {
		return Config{}, fmt.Errorf("connectorkit: no config path, so nothing names the console this connector trusts")
	}
	read, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read connector config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(read, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse connector config %s: %w", path, err)
	}
	if err := cfg.check(); err != nil {
		return Config{}, fmt.Errorf("connector config %s: %w", path, err)
	}
	return cfg, nil
}

func (c Config) check() error {
	if c.Console == "" {
		return fmt.Errorf("console is required")
	}
	if c.ConnectorID == "" {
		return fmt.Errorf("connectorId is required")
	}
	if c.OrganizationID == "" {
		return fmt.Errorf("organizationId is required")
	}
	for _, grant := range c.Grants {
		if !slices.Contains(grantable, grant) {
			return fmt.Errorf("grants holds %q, which names no capability", grant)
		}
	}
	return nil
}
