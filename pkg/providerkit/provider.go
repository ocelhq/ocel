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

type DeployPreflight struct {
	Plan      DeployPlan
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

type Vendor string

type LinkType string

type Class = ports.Class

const (
	ClassProduction = ports.ClassProduction
	ClassPreview    = ports.ClassPreview
)

type Removal = edge.Surface
