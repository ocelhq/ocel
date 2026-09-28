package provider

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/records"
)

type Provider interface {
	Facts() Facts
	Hooks() Hooks

	Bootstrap(kind edge.Kind) (Bootstrap, error)
	Stacks() Stacks
	Artifacts() ArtifactStore
	Records() records.Store
	Cipher() records.Cipher
	Credentials() Credentials
	Edges() Edges
	DNS() DNS
	Certificates() Certificates
	Connector() Connector
	Runtime() Runtime
	Liveness() Liveness
}

type Facts struct {
	Vendor            Vendor
	Bindings          []BindingType
	Computes          []Compute
	Edges             []edge.Kind
	DefaultEdge       edge.Kind
	DNSKinds          []DNSKind
	RendersTransforms bool
	StoresArtifacts   bool
}

type EdgeProgramRequest struct {
	Class             edge.Class
	Kind              edge.Kind
	Slug              string
	Env               string
	PreviewBaseDomain string
	Apps              []string
}

type EdgeProgram struct {
	Spec   *edge.ProgramSpec
	Values map[string]string
}

type DeployPreflight struct {
	Deploy    DeploySpec
	Edge      edge.Kind
	Resources []Resource
	Grants    []Binding
	Apps      []AppUsage
	Progress  progress.Progress
	WrittenBy WrittenBy
	Dry       bool
}

type AppUsage struct {
	App       string
	Resources []Resource
	Grants    []Binding
}

type Vendor string

type BindingType string
