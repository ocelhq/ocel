package edge

import (
	"context"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
)

type Kind string

const None Kind = ""

type Need string

const (
	NeedEdgeMiddleware Need = "edge-middleware"
	NeedEdgeRuntime    Need = "edge-runtime"
	NeedPPRResume      Need = "ppr-resume"
	NeedEdgeCache      Need = "edge-cache"
	NeedStreaming      Need = "streaming"
	NeedRevalidation   Need = "revalidation"
)

func AllNeeds() []Need {
	return []Need{NeedEdgeMiddleware, NeedEdgeRuntime, NeedPPRResume, NeedEdgeCache, NeedStreaming, NeedRevalidation}
}

func CodeNeeds() []Need {
	return []Need{NeedEdgeMiddleware, NeedEdgeRuntime}
}

func NeedNames(needs []Need) []string {
	names := make([]string, 0, len(needs))
	for _, need := range needs {
		names = append(names, string(need))
	}
	return names
}

func ValidNeed(need Need) bool {
	return slices.Contains(AllNeeds(), need)
}

type Facts struct {
	Supported             []Need
	RunsCode              bool
	ServesUnbound         bool
	ProxiesRecords        bool
	ProxiedRecordNote     string
	ShieldsOrigin         bool
	TunnelsToOrigin       bool
	OriginFacingRanges    []string
	InvalidatesByCacheTag bool
	CachesResponses       bool
	CacheControlHeader    string
	CacheTagLimits        CacheTagLimits
	PurgeBudget           PurgeBudget
	Gates                 bool
	GatesAtOrigin         bool
	CredentialScope       string
}

type Edge interface {
	Kind() Kind

	Facts() Facts

	Hooks() Hooks

	Bootstrap(ctx context.Context, tier environment.Tier) (BootstrapOutput, error)

	Teardown(ctx context.Context, tier environment.Tier) error

	Reconcile(ctx context.Context, spec StackSpec, prior StackState) (EdgeStack, error)

	Open(state StackState) (EdgeStack, error)

	ReconcilePreviewWildcard(ctx context.Context, spec PreviewWildcardSpec) (string, error)

	DestroyPreviewWildcard(ctx context.Context, baseDomain string) error

	DomainOwner(ctx context.Context, hostname string) (string, error)

	ProjectOwner(slug string, tier environment.Tier) string

	PlanProjectRemoval(scope ProjectScope) []PlanGroup

	PlanPreviewWildcardRemoval(wildcard string) (removed, kept PlanGroup)

	PlanSharedPreviewRemoval() PlanGroup
}

type ProjectScope struct {
	Slug      string
	Tier      environment.Tier
	Hostnames []string
	Address   string
}

type EdgeStack interface {
	State() StackState

	BindDomain(ctx context.Context, binding DomainBinding) error

	UnbindDomain(ctx context.Context, hostname string) error

	Destroy(ctx context.Context) error
}

type Origin struct {
	Address   string
	Certified bool
	Tunneled  bool
}

type OriginCertificate struct {
	ID          string
	Certificate string
	Key         string
	ExpiresAt   time.Time
}

const OriginCertificateRenewal = 182 * 24 * time.Hour

func (c OriginCertificate) IsDue(now time.Time) bool {
	return now.Add(OriginCertificateRenewal).After(c.ExpiresAt)
}

type DomainBinding struct {
	Hostname    string
	Certificate string
	App         string
	Origin      *Origin
	Say         func(string)
}

type CredentialIdentity struct {
	Account string
}

type CodeEntitlement struct {
	Plan    string
	Granted Entitlement
	Reason  string
}

type Entitlement string

const (
	EntitlementUnknown  Entitlement = "unknown"
	EntitlementGranted  Entitlement = "granted"
	EntitlementWithheld Entitlement = "withheld"
)

type CredentialPurpose string

const (
	PurposeBootstrap CredentialPurpose = "bootstrap"
	PurposeDeploy    CredentialPurpose = "deploy"
)

type CredentialDocument struct {
	Heading  string
	Document string
}
