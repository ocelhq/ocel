package providerkit

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Provider interface {
	Vendor() Vendor

	Serves() []LinkType

	Bootstrap(kind edge.Kind) (Bootstrapper, error)
	Releases() Releaser
	Artifacts() ArtifactStore
	Records() RecordStore
	Sealer() Sealer
	Credentials() Credentials
	Edges() EdgeRegistry
	DNS() DNSRegistry
}

type Warmer interface {
	Warm(ctx context.Context, targets []string, report Reporter) error
}

type CodeEmbedder interface {
	EmbedCode(ctx context.Context, function string, artifact ArtifactRef, report Reporter) error
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

type EdgeProgrammer interface {
	EdgeProgram(ctx context.Context, req EdgeProgramRequest) (EdgeProgram, error)
}

func edgeProgramFor(ctx context.Context, provider Provider, front edge.Edge, req EdgeProgramRequest) (EdgeProgram, error) {
	if !front.Facts().RunsCode {
		return EdgeProgram{}, nil
	}
	programmer, programs := provider.(EdgeProgrammer)
	if !programs {
		return EdgeProgram{}, Refuse(CodeNotReady,
			"the %s edge answers every request from an entry worker it runs, and this provider builds no program for that worker to run: "+
				"raising the surface here would leave nothing behind it, so deploy through an edge this provider programs instead",
			front.Kind())
	}
	req.Kind = front.Kind()
	return programmer.EdgeProgram(ctx, req)
}

type DeployPreflight struct {
	Plan      DeployPlan
	Edge      edge.Kind
	Resources []Resource
	Grants    []Link
	Apps      []AppUsage
	Report    Reporter
}

type AppUsage struct {
	App       string
	Resources []Resource
	Grants    []Link
}

type DeployPreflighter interface {
	PreflightDeploy(ctx context.Context, pre DeployPreflight) error
}

type StackInspector interface {
	Inspect(ctx context.Context, ref StackRef) (StackState, error)
}

type GrantVerifier interface {
	VerifyGrants(ctx context.Context, link Link) error
}

type MembraneCrosser interface {
	CrossesMembrane(kind LinkType) bool
}

type Vendor string

type LinkType string

type Class = ports.Class

const (
	ClassProduction = ports.ClassProduction
	ClassPreview    = ports.ClassPreview
)
