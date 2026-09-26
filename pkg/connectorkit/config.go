package connectorkit

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

type Config struct {
	Console        string   `json:"console"`
	ConnectorID    string   `json:"connectorId"`
	OrganizationID string   `json:"organizationId"`
	Target         string   `json:"target,omitempty"`
	Grants         []string `json:"grants"`
	KeyPath        string   `json:"keyPath"`
}

var grantable = []string{CapabilityEnvVarsRead, CapabilityEnvVarsWrite, CapabilityEnvVarsReveal}

func ReadConfig(path string) (Config, error) {
	if carried := os.Getenv(provider.ConnectorConfigEnvVar); carried != "" {
		return ParseConfig([]byte(carried), provider.ConnectorConfigEnvVar)
	}
	if path == "" {
		return Config{}, fmt.Errorf("connectorkit: nothing names the console this connector trusts: neither %s nor a config file", provider.ConnectorConfigEnvVar)
	}
	read, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read connector config: %w", err)
	}
	return ParseConfig(read, path)
}

func ParseConfig(raw []byte, from string) (Config, error) {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse connector config %s: %w", from, err)
	}
	if err := cfg.check(); err != nil {
		return Config{}, fmt.Errorf("connector config %s: %w", from, err)
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
