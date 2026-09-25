package providerkit

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Provider interface {
	Facts() Facts
	Hooks() Hooks

	Bootstrap(kind edge.Kind) (Bootstrap, error)
	Stacks() Stacks
	Artifacts() ArtifactStore
	Records() RecordStore
	Cipher() Cipher
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
	Class             Class
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

func edgeProgramFor(ctx context.Context, provider Provider, front edge.Edge, req EdgeProgramRequest) (EdgeProgram, error) {
	if !front.Facts().RunsCode {
		return EdgeProgram{}, nil
	}
	program := provider.Hooks().ProgramEdge
	if program == nil {
		return EdgeProgram{}, Refuse(CodeNotReady,
			"the %s edge answers every request from an entry worker it runs, and this provider builds no program for that worker to run: "+
				"raising the surface here would leave nothing behind it, so deploy through an edge this provider programs instead",
			front.Kind())
	}
	req.Kind = front.Kind()
	return program(ctx, req)
}

type DeployPreflight struct {
	Plan      DeployPlan
	Edge      edge.Kind
	Resources []Resource
	Grants    []Binding
	Apps      []AppUsage
	Progress  Progress
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

type Class = ports.Class

const (
	ClassProduction = ports.ClassProduction
	ClassPreview    = ports.ClassPreview
)
