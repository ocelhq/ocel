package provider

import (
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
)

type Provider interface {
	Facts() Facts
	Hooks() Hooks

	Bootstrap(kind edge.Kind) (Bootstrap, error)
	Stacks() Stacks
	Artifacts() ArtifactStore
	KeyValues() records.Store
	Cipher() seal.Cipher
	Credentials() Credentials
	Edges() Edges
	Routers() Routers
	DNS() DNS
	Certificates() Certificates
	Connector() Connector
	Runtime() Runtime
	Liveness() Liveness
	Logs() Logs
}

type Facts struct {
	Vendor            Vendor
	Bindings          []BindingType
	Computes          []Compute
	Edges             []edge.Kind
	DefaultEdge       edge.Kind
	Pairings          []Pairing
	DNSKinds          []DNSKind
	RendersTransforms bool
	StoresArtifacts   bool
	RunsTunnels       bool
	WorkerCeilings    []WorkerCeiling

	RetainsContainerReleases bool
	RunsGates                bool
	RunsFunctionsAsImages    bool
	CachesBytecode           bool
	MaxFunctionBytes         int64
}

type EdgeProgramRequest struct {
	Tier environment.Tier
	Kind edge.Kind
}

type DeployPreflight struct {
	Deploy            DeploySpec
	PreviewBaseDomain string
	Edge              edge.Kind
	Resources         []Resource
	Grants            []Binding
	Apps              []AppUsage
	Progress          progress.Log
	WrittenBy         WrittenBy
	Uses              []buildoutput.Uses
}

type AppUsage struct {
	App       string
	Resources []Resource
	Grants    []Binding
}

type Vendor string

type BindingType string
