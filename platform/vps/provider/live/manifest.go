package live

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/seal"
)

const EnvVar = "OCEL_LIVE_MANIFEST"

const (
	SocketDir  = "/run/ocel-live"
	SocketFile = "values.sock"
	SocketPath = SocketDir + "/" + SocketFile
	ValuesPath = "/values"
	SpacePath  = "/space"
)

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
	Env          string   `json:"env"`
	Endpoint     string   `json:"endpoint"`
	Region       string   `json:"region"`
	AccessKeyID  string   `json:"accessKeyId"`
	Sessions     string   `json:"sessions,omitempty"`
	Granted      []string `json:"granted,omitempty"`
	PathStyle    bool     `json:"pathStyle,omitempty"`
	SweepUploads bool     `json:"sweepUploads,omitempty"`
	Pointer      string   `json:"pointer,omitempty"`
	Volume       string   `json:"volume,omitempty"`
	Sealed       string   `json:"sealed"`
}

type Manifest struct {
	Slug        string         `json:"slug"`
	Tier        string         `json:"tier"`
	Environment string         `json:"environment,omitempty"`
	Keys        []live.Key     `json:"keys,omitempty"`
	Bindings    []live.Binding `json:"bindings,omitempty"`
	Store       *Store         `json:"store,omitempty"`
}

func (m Manifest) Live() bool { return len(m.Keys) > 0 || len(m.Bindings) > 0 }

func NewStoreSecretAssociatedData(project string, tier environment.Tier, stack string) (seal.AssociatedData, error) {
	return NewSecretAssociatedData(project, tier, stack, StoreSecretFolder, StoreSecretBinding, StoreSecretName)
}

func NewSecretAssociatedData(project string, tier environment.Tier, stack, folder, binding, name string) (seal.AssociatedData, error) {
	if project == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "the %s secret names no project, and its project is what the secret is sealed to", name)
	}
	if stack == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "the %s secret names no stack, and its stack is what the secret is sealed to", name)
	}
	return seal.AssociatedData{
		{Name: "project", Value: project},
		{Name: "tier", Value: string(tier)},
		{Name: "stack", Value: stack},
		{Name: "folder", Value: folder},
		{Name: "binding", Value: binding},
		{Name: "name", Value: name},
	}, nil
}

func Render(m Manifest) ([]byte, error) {
	if !m.Live() {
		return nil, nil
	}
	if m.Slug == "" {
		return nil, fmt.Errorf("the live-value manifest names %d keys but no project slug", len(m.Keys)+len(m.Bindings))
	}
	switch environment.Tier(m.Tier) {
	case environment.TierProduction, environment.TierPreview:
	default:
		return nil, fmt.Errorf("the live-value manifest names tier %q, want %s or %s",
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

type Answer struct {
	Values map[string]string `json:"values"`
}

type Space struct {
	Free  uint64 `json:"free"`
	Total uint64 `json:"total"`
}
