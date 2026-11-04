package endpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"

	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	"github.com/ocelhq/ocel/pkg/scope"
)

const (
	JobAudience  = "ocel-job"
	CloudID      = "ocel-cloud"
	ConfigEnvVar = "OCEL_ENDPOINT_CONFIG"
)

type Config struct {
	Console        string           `json:"console"`
	EndpointID     string           `json:"endpointId"`
	OrganizationID string           `json:"organizationId"`
	Name           string           `json:"name"`
	Target         string           `json:"target,omitempty"`
	AllowedScopes  []string         `json:"allowedScopes"`
	Credentials    *CredentialTrust `json:"credentials,omitempty"`
	KeyPath        string           `json:"keyPath"`
}

type CredentialTrust struct {
	Issuer   string `json:"issuer"`
	Audience string `json:"audience"`
	TargetID string `json:"targetId"`
}

func ReadConfig(path string) (Config, error) {
	if raw := os.Getenv(ConfigEnvVar); raw != "" {
		return ParseConfig([]byte(raw), ConfigEnvVar)
	}
	if path == "" {
		return Config{}, fmt.Errorf("endpoint: nothing names the console this endpoint trusts: neither %s nor a config file", ConfigEnvVar)
	}
	read, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read endpoint config: %w", err)
	}
	return ParseConfig(read, path)
}

func ParseConfig(raw []byte, from string) (Config, error) {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse endpoint config %s: %w", from, err)
	}
	if err := cfg.check(); err != nil {
		return Config{}, fmt.Errorf("endpoint config %s: %w", from, err)
	}
	return cfg, nil
}

func (c Config) check() error {
	if c.Console == "" {
		return errors.New("console is required")
	}
	if _, err := originOf(c.Console); err != nil {
		return err
	}
	if c.EndpointID == "" {
		return errors.New("endpointId is required")
	}
	if c.OrganizationID == "" {
		return errors.New("organizationId is required")
	}
	if _, err := c.ListAllowedScopes(); err != nil {
		return err
	}
	if c.Credentials != nil {
		return c.Credentials.refuseIncomplete()
	}
	return nil
}

func (t CredentialTrust) refuseIncomplete() error {
	if _, err := originOf(t.Issuer); err != nil {
		return fmt.Errorf("credentials: %w", err)
	}
	if t.Audience == "" || t.TargetID == "" {
		return errors.New("credentials name an issuer and no audience or target")
	}
	return nil
}

func (c Config) ListAllowedScopes() ([]scopev1.Scope, error) {
	scopes := make([]scopev1.Scope, 0, len(c.AllowedScopes))
	for _, claim := range c.AllowedScopes {
		allowed, ok := scope.ScopeOfClaim(claim)
		if !ok || !scope.IsUserScope(allowed) {
			return nil, fmt.Errorf("allowedScopes contains %q, which names no scope a person can hold", claim)
		}
		scopes = append(scopes, allowed)
	}
	return scopes, nil
}

func originOf(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", raw, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("url %q names no origin", raw)
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopback(parsed.Hostname())) {
		return "", fmt.Errorf("url %q is not https, and signing keys are fetched from it", raw)
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
