package live

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/live"
)

const PostgresPasswordGrant = "master password"

func PostgresPasswordSecret(binding *bindingsv1.Binding) string {
	if binding.GetPostgres() == nil {
		return ""
	}
	for _, grant := range binding.GetGrants() {
		if grant.GetLabel() == PostgresPasswordGrant && len(grant.GetResources()) == 1 {
			return grant.GetResources()[0]
		}
	}
	return ""
}

const FilePath = buildoutput.StateDir + "/variables.live.json"

const EnvVar = "OCEL_LIVE_MANIFEST"

type Manifest struct {
	Slug        string         `json:"slug"`
	Table       string         `json:"table"`
	KeyARN      string         `json:"keyArn"`
	Tier        string         `json:"tier"`
	Environment string         `json:"environment,omitempty"`
	Keys        []live.Key     `json:"keys"`
	Bindings    []live.Binding `json:"bindings,omitempty"`
}

func (m Manifest) Live() bool { return len(m.Keys) > 0 || len(m.Bindings) > 0 }

func Render(m Manifest) ([]byte, error) {
	if !m.Live() {
		return nil, nil
	}
	for _, component := range []struct{ name, value string }{
		{"project slug", m.Slug},
		{"variable table", m.Table},
		{"environment tier", m.Tier},
	} {
		if component.value == "" {
			return nil, fmt.Errorf("the live-value manifest names %d keys but no %s", len(m.Keys)+len(m.Bindings), component.name)
		}
	}
	if m.KeyARN == "" {
		return nil, provider.WantedPartError{Part: provider.VariablesKeyPart, Err: fmt.Errorf("the live-value manifest names %d keys but the %s bootstrap has no key to read them through.\nRun `%s` to add one, then deploy again",
			len(m.Keys)+len(m.Bindings), m.Tier, provider.BootstrapCommand(environment.Tier(m.Tier)))}
	}
	return json.Marshal(m)
}

func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("decode the live-value manifest: %w", err)
	}
	return m, nil
}
