package live

import (
	"encoding/json"
	"fmt"
)

const FilePath = ".ocel/variables.live.json"

type Manifest struct {
	Slug        string `json:"slug"`
	Table       string `json:"table"`
	KeyARN      string `json:"keyArn"`
	Class       string `json:"class"`
	Environment string `json:"environment,omitempty"`
	Keys        []Key  `json:"keys"`
	Links       []Link `json:"links,omitempty"`
}

type Key struct {
	Key    string `json:"key"`
	Folder string `json:"folder,omitempty"`
}

type Link struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

func Render(m Manifest) ([]byte, error) {
	if len(m.Keys) == 0 && len(m.Links) == 0 {
		return nil, nil
	}
	for _, component := range []struct{ name, value string }{
		{"project slug", m.Slug},
		{"variable table", m.Table},
		{"variable key", m.KeyARN},
		{"environment class", m.Class},
	} {
		if component.value == "" {
			return nil, fmt.Errorf("the live-value manifest names %d keys but no %s", len(m.Keys)+len(m.Links), component.name)
		}
	}
	return json.Marshal(m)
}

func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", FilePath, err)
	}
	return m, nil
}
