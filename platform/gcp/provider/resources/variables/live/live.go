package live

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/live"
)

const EnvVar = "OCEL_LIVE_MANIFEST"

type Manifest struct {
	Project            string         `json:"project"`
	Region             string         `json:"region"`
	Namespace          string         `json:"namespace"`
	Slug               string         `json:"slug"`
	Tier               string         `json:"tier"`
	Environment        string         `json:"environment,omitempty"`
	Endpoint           string         `json:"endpoint,omitempty"`
	Keys               []live.Key     `json:"keys"`
	Bindings           []live.Binding `json:"bindings,omitempty"`
	Tasks              *Tasks         `json:"tasks,omitempty"`
	RealtimePublishURL string         `json:"realtimePublishUrl,omitempty"`
}

type Tasks struct {
	Environment string                        `json:"environment"`
	Topics      map[string]provider.TopicSpec `json:"topics"`
	DelayQueue  string                        `json:"delayQueue"`
	Account     string                        `json:"account"`
	PublishURL  string                        `json:"publishUrl"`
}

func (m Manifest) Live() bool { return len(m.Keys) > 0 || len(m.Bindings) > 0 }

func (m Manifest) pinned() bool { return m.Live() || m.Tasks != nil }

func Render(m Manifest) ([]byte, error) {
	if !m.pinned() {
		return nil, nil
	}
	for _, component := range []struct{ name, value string }{
		{"project", m.Project},
		{"region", m.Region},
		{"namespace", m.Namespace},
		{"project slug", m.Slug},
		{"environment tier", m.Tier},
	} {
		if component.value == "" {
			return nil, fmt.Errorf("the live-value manifest names %d keys but no %s", len(m.Keys)+len(m.Bindings), component.name)
		}
	}
	switch environment.Tier(m.Tier) {
	case environment.TierProduction, environment.TierPreview:
	default:
		return nil, fmt.Errorf("the live-value manifest names tier %q, and a value is sealed under the key of %s or %s",
			m.Tier, environment.TierProduction, environment.TierPreview)
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
