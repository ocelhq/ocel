package live

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const EnvVar = "OCEL_LIVE_MANIFEST"

type Manifest struct {
	Project     string         `json:"project"`
	Region      string         `json:"region"`
	Namespace   string         `json:"namespace"`
	Slug        string         `json:"slug"`
	Class       string         `json:"class"`
	Environment string         `json:"environment,omitempty"`
	Endpoint    string         `json:"endpoint,omitempty"`
	Keys        []live.Key     `json:"keys"`
	Bindings    []live.Binding `json:"bindings,omitempty"`
}

func (m Manifest) Live() bool { return len(m.Keys) > 0 || len(m.Bindings) > 0 }

func Render(m Manifest) ([]byte, error) {
	if !m.Live() {
		return nil, nil
	}
	for _, component := range []struct{ name, value string }{
		{"project", m.Project},
		{"region", m.Region},
		{"namespace", m.Namespace},
		{"project slug", m.Slug},
		{"environment class", m.Class},
	} {
		if component.value == "" {
			return nil, fmt.Errorf("the live-value manifest names %d keys but no %s", len(m.Keys)+len(m.Bindings), component.name)
		}
	}
	switch edge.Class(m.Class) {
	case edge.ClassProduction, edge.ClassPreview:
	default:
		return nil, fmt.Errorf("the live-value manifest names class %q, and a value is sealed under the key of %s or %s",
			m.Class, edge.ClassProduction, edge.ClassPreview)
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
