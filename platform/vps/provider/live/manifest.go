package live

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit"
	rt "github.com/ocelhq/ocel/pkg/runtimekit/live"
)

const EnvVar = "OCEL_LIVE_MANIFEST"

const (
	SocketDir  = "/run/ocel-live"
	SocketFile = "values.sock"
	SocketPath = SocketDir + "/" + SocketFile
	ValuesPath = "/values"
	SpacePath  = "/space"
)

const ProjectionDir = providerkit.ContainerLivePath

const (
	StoreSecretFolder  = "resources"
	StoreSecretBinding = "store"
	StoreSecretName    = "storekey"
)

const (
	StoreSecretKey = "ocel.store.secretAccessKey"
	StorePublicKey = "ocel.store.publicBaseUrl"
)

type Store struct {
	Env           string `json:"env"`
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	AccessKeyID   string `json:"accessKeyId"`
	Sessions      string `json:"sessions,omitempty"`
	PathStyle     bool   `json:"pathStyle,omitempty"`
	PostPolicies  bool   `json:"postPolicies,omitempty"`
	PublicBaseURL string `json:"publicBaseUrl,omitempty"`
	Pointer       string `json:"pointer,omitempty"`
	Volume        string `json:"volume,omitempty"`
	Sealed        string `json:"sealed"`
}

type Manifest struct {
	Slug        string       `json:"slug"`
	Class       string       `json:"class"`
	Environment string       `json:"environment,omitempty"`
	Keys        []rt.Key     `json:"keys,omitempty"`
	Bindings    []rt.Binding `json:"bindings,omitempty"`
	Store       *Store       `json:"store,omitempty"`
}

func (m Manifest) Live() bool { return len(m.Keys) > 0 || len(m.Bindings) > 0 }

func (m Manifest) StoreCoordinate() providerkit.Coordinate {
	return providerkit.Coordinate{
		Project: m.Slug,
		Class:   providerkit.Class(m.Class),
		Env:     m.Store.Env,
		Folder:  StoreSecretFolder,
		Binding: StoreSecretBinding,
		Name:    StoreSecretName,
	}
}

func Render(m Manifest) ([]byte, error) {
	if !m.Live() {
		return nil, nil
	}
	if m.Slug == "" {
		return nil, fmt.Errorf("the live-value manifest names %d keys but no project slug", len(m.Keys)+len(m.Bindings))
	}
	switch providerkit.Class(m.Class) {
	case providerkit.ClassProduction, providerkit.ClassPreview:
	default:
		return nil, fmt.Errorf("the live-value manifest names class %q, and a value is sealed under the key of %s or %s",
			m.Class, providerkit.ClassProduction, providerkit.ClassPreview)
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

type Answer struct {
	Values map[string]string `json:"values"`
}

type Space struct {
	Free  uint64 `json:"free"`
	Total uint64 `json:"total"`
}
