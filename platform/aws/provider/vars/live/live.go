package live

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/providerkit"
	rt "github.com/ocelhq/ocel/pkg/runtimekit/live"
)

const FilePath = constants.ProjectStateDirName + "/variables.live.json"

const EnvVar = "OCEL_LIVE_MANIFEST"

type Manifest struct {
	Slug        string       `json:"slug"`
	Table       string       `json:"table"`
	KeyARN      string       `json:"keyArn"`
	Class       string       `json:"class"`
	Environment string       `json:"environment,omitempty"`
	Keys        []rt.Key     `json:"keys"`
	Bindings    []rt.Binding `json:"bindings,omitempty"`
}

func (m Manifest) Live() bool { return len(m.Keys) > 0 || len(m.Bindings) > 0 }

func Render(m Manifest) ([]byte, error) {
	if !m.Live() {
		return nil, nil
	}
	for _, component := range []struct{ name, value string }{
		{"project slug", m.Slug},
		{"variable table", m.Table},
		{"environment class", m.Class},
	} {
		if component.value == "" {
			return nil, fmt.Errorf("the live-value manifest names %d keys but no %s", len(m.Keys)+len(m.Bindings), component.name)
		}
	}
	if m.KeyARN == "" {
		return nil, fmt.Errorf("the live-value manifest names %d keys but the %s bootstrap holds no key to read them through.\nRun `%s` to add one, then deploy again",
			len(m.Keys)+len(m.Bindings), m.Class, providerkit.BootstrapVarsKeyCommand(providerkit.Class(m.Class)))
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
