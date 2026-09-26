package providerserver

import (
	"context"
	"errors"
	"fmt"
	"slices"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (h *handlers) Preflight(ctx context.Context, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	class, err := classOf(req.GetRequiredTier())
	if err != nil {
		return nil, err
	}
	p, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}

	resp := &contractv1.PreflightResponse{
		Identity: &contractv1.Identity{},
		Computes: provider.ComputeNames(p.Facts().Computes),
	}

	principal, err := p.Credentials().Whoami(ctx)
	if err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, CredentialProblemProto(p.Facts().Vendor, err))
		return resp, nil
	}
	resp.Identity = PrincipalProto(p.Facts().Vendor, principal)
	resp.ContainerArchs, err = containerArchs(ctx, p.Runtime(), req.GetContainers())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if err := h.edgeIdentity(ctx, p, gate.Edge, req.GetEdge(), resp); err != nil {
		return nil, err
	}

	required, err := bootstrapplan.RequiredFeatures(gate.Bootstrap.Catalogue(), req.GetFrameworks(), string(gate.Edge))
	if err != nil {
		return nil, provider.RefusalError(err)
	}

	standing, err := gate.Status(ctx, class)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	resp.Bootstrap = BootstrapStatusProto(standing, h.session.writer, req.GetRequiredTier(), required)

	if standing.Present {
		resp.InfraTier, resp.InfrastructurePresent = tierOf(class), true
		if err := checkCompat(standing.Schema, true, provider.BootstrapSchema).explain(standing.Schema, provider.BootstrapSchema, provider.BootstrapCommand(class)); err != nil {
			return nil, provider.RefusalError(err)
		}
		resp.KnownSlugs, err = slugsBesides(ctx, gate, class, req.GetSlug())
		if err != nil {
			return nil, provider.RefusalError(err)
		}
		resp.DomainClaims, err = h.domainClaims(ctx, p, class, req)
		if err != nil {
			return nil, provider.RefusalError(err)
		}
		if req.GetCheckHosts() {
			resp.HostChecks = h.hostChecks(ctx, p, class, req.GetHostCheckDomains())
		}
		if class == edge.ClassPreview {
			resp.PreviewWildcard, err = recordedPreviewWildcard(ctx, p)
			if err != nil {
				return nil, provider.RefusalError(err)
			}
		}
		return resp, nil
	}

	sibling, err := gate.Status(ctx, siblingOf(class))
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if sibling.Present {
		resp.InfraTier, resp.InfrastructurePresent = tierOf(sibling.Class), true
	}
	return resp, nil
}

func containerArchs(ctx context.Context, runtime images.Runtime, containers []*contractv1.ContainerApp) (map[string]string, error) {
	if len(containers) == 0 {
		return nil, nil
	}
	archs := make(map[string]string, len(containers))
	for _, container := range containers {
		runs, err := runtime.Arch(ctx, container.GetApp(), container.GetArch())
		if err != nil {
			return nil, err
		}
		archs[container.GetApp()] = runs
	}
	return archs, nil
}

func (h *handlers) edgeIdentity(
	ctx context.Context,
	p provider.Provider,
	kind edge.Kind,
	sel *contractv1.EdgeSelection,
	resp *contractv1.PreflightResponse,
) error {
	if kind == "" {
		return nil
	}
	front, err := h.edgeFor(p, sel)
	if err != nil {
		return provider.RefusalError(err)
	}
	verify := front.Hooks().VerifyCredentials
	if verify == nil {
		return nil
	}
	scope, err := verify(ctx)
	if err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, CredentialProblemProto(provider.Vendor(kind), err))
		return nil
	}
	resp.Identity.EdgeScope = scope.Account
	return nil
}

func (h *handlers) domainClaims(ctx context.Context, p provider.Provider, class edge.Class, req *contractv1.PreflightRequest) ([]*contractv1.DomainClaim, error) {
	if len(req.GetDomains()) == 0 {
		return nil, nil
	}
	front, err := h.edgeFor(p, req.GetEdge())
	if err != nil {
		return nil, err
	}
	ours, err := boundHere(ctx, p.Records(), class, req.GetSlug())
	if err != nil {
		return nil, err
	}
	var mine string
	if slug := req.GetSlug(); slug != "" {
		mine = front.ProjectOwner(slug, class)
	}
	claims := make([]*contractv1.DomainClaim, 0, len(req.GetDomains()))
	for _, hostname := range req.GetDomains() {
		claim := &contractv1.DomainClaim{Hostname: hostname, Status: contractv1.DomainClaim_STATUS_UNCLAIMED}
		if !slices.Contains(ours, hostname) {
			owner, err := front.DomainOwner(ctx, hostname)
			switch {
			case err != nil && ctx.Err() != nil:
				return nil, err
			case err != nil:
				claim.Status, claim.Cause = contractv1.DomainClaim_STATUS_UNSPECIFIED, err.Error()
			case owner != "" && owner != edge.PreviewEntryOwner && owner != mine:
				claim.Status, claim.Owner = contractv1.DomainClaim_STATUS_CLAIMED, owner
			}
		}
		claims = append(claims, claim)
	}
	return claims, nil
}

func boundHere(ctx context.Context, records records.Store, class edge.Class, slug string) ([]string, error) {
	if slug == "" {
		return nil, nil
	}
	state, err := (edgeStateStore{records: records, name: stackrecords.EdgeStackRecord(class, slug)}).read(ctx)
	if err != nil {
		return nil, err
	}
	return state.Edge.Bound, nil
}

func slugsBesides(ctx context.Context, gate Gate, class edge.Class, slug string) ([]string, error) {
	if slug == "" {
		return nil, nil
	}
	recorded, err := gate.RecordedFeatures(ctx, class)
	if err != nil {
		return nil, err
	}
	var slugs []string
	for known := range recorded {
		if known != slug {
			slugs = append(slugs, known)
		}
	}
	slices.Sort(slugs)
	return slugs, nil
}

func siblingOf(class edge.Class) edge.Class {
	if class == edge.ClassPreview {
		return edge.ClassProduction
	}
	return edge.ClassPreview
}

func tierOf(class edge.Class) environmentv1.Tier {
	if class == edge.ClassPreview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}

func PrincipalProto(vendor provider.Vendor, principal provider.Principal) *contractv1.Identity {
	named := principal.Vendor
	if named == "" {
		named = vendor
	}
	out := &contractv1.Identity{
		Provider:  string(named),
		Account:   principal.Account,
		Principal: principal.Name,
		Location:  principal.Location,
		EdgeScope: principal.EdgeScope,
	}
	for _, detail := range principal.Details {
		out.Details = append(out.Details, &contractv1.Detail{Label: detail.Label, Value: detail.Value})
	}
	return out
}

var credentialTrouble = map[refusal.Code]string{
	refusal.CodeDenied:   "could not authenticate",
	refusal.CodeNotReady: "could not reach",
	refusal.CodeInvalid:  "misconfigured",
}

func CredentialProblemProto(vendor provider.Vendor, err error) *contractv1.CredentialProblem {
	problem := &contractv1.CredentialProblem{Provider: string(vendor)}
	var refusal refusal.Refusal
	if errors.As(err, &refusal) {
		if trouble, named := credentialTrouble[refusal.Code]; named {
			problem.Message, problem.Hint = trouble, refusal.Message
			return problem
		}
	}
	problem.Message = fmt.Sprintf("could not authenticate: %v", err)
	return problem
}
