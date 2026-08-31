package providerkit

import (
	"context"
	"errors"
	"fmt"
	"slices"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (h *handlers) Preflight(ctx context.Context, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	class, err := classOf(req.GetRequiredTier())
	if err != nil {
		return nil, err
	}
	provider, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}

	resp := &contractv1.PreflightResponse{
		Identity: &contractv1.Identity{},
		Computes: ComputeNames(provider.Computes()),
	}

	identity, err := provider.Credentials().Whoami(ctx)
	if err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, CredentialProblemProto(provider.Vendor(), err))
		return resp, nil
	}
	resp.Identity = IdentityProto(provider.Vendor(), identity)

	required, err := RequiredFeatures(gate.Bootstrapper.Catalogue(), req.GetFrameworks(), string(gate.Edge))
	if err != nil {
		return nil, RefusalError(err)
	}

	standing, err := gate.Standing(ctx, class)
	if err != nil {
		return nil, RefusalError(err)
	}
	resp.Bootstrap = BootstrapStatusProto(standing, h.session.writer, req.GetRequiredTier(), required)

	if standing.Present {
		resp.InfraTier, resp.InfrastructurePresent = tierOf(class), true
		if err := checkCompat(standing.Schema, true, BootstrapSchema).explain(standing.Schema, BootstrapSchema, BootstrapCommand(class)); err != nil {
			return nil, RefusalError(err)
		}
		resp.KnownSlugs, err = slugsBesides(ctx, gate, class, req.GetSlug())
		if err != nil {
			return nil, RefusalError(err)
		}
		resp.DomainClaims, err = h.domainClaims(ctx, provider, class, req)
		if err != nil {
			return nil, RefusalError(err)
		}
		resp.Standing, err = h.standingChecks(ctx, provider, class, req.GetDomains())
		if err != nil {
			return nil, RefusalError(err)
		}
		if class == ClassPreview {
			resp.PreviewWildcard, err = heldPreviewWildcard(ctx, provider)
			if err != nil {
				return nil, RefusalError(err)
			}
		}
		return resp, nil
	}

	sibling, err := gate.Standing(ctx, siblingOf(class))
	if err != nil {
		return nil, RefusalError(err)
	}
	if sibling.Present {
		resp.InfraTier, resp.InfrastructurePresent = tierOf(sibling.Class), true
	}
	return resp, nil
}

func (h *handlers) domainClaims(ctx context.Context, provider Provider, class Class, req *contractv1.PreflightRequest) ([]*contractv1.DomainClaim, error) {
	if len(req.GetDomains()) == 0 {
		return nil, nil
	}
	front, err := h.edgeFor(provider, req.GetEdge())
	if err != nil {
		return nil, err
	}
	ours, err := boundHere(ctx, provider.Records(), class, req.GetSlug())
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

func boundHere(ctx context.Context, records RecordStore, class Class, slug string) ([]string, error) {
	if slug == "" {
		return nil, nil
	}
	state, err := (stackStore{records: records, name: EdgeStackRecord(class, slug)}).read(ctx)
	if err != nil {
		return nil, err
	}
	return state.Edge.Bound, nil
}

func slugsBesides(ctx context.Context, gate Gate, class Class, slug string) ([]string, error) {
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

func siblingOf(class Class) Class {
	if class == ClassPreview {
		return ClassProduction
	}
	return ClassPreview
}

func tierOf(class Class) environmentv1.Tier {
	if class == ClassPreview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}

func IdentityProto(vendor Vendor, id Identity) *contractv1.Identity {
	named := id.Provider
	if named == "" {
		named = vendor
	}
	out := &contractv1.Identity{
		Provider:  string(named),
		Account:   id.Account,
		Principal: id.Principal,
		EdgeScope: id.EdgeScope,
	}
	for _, detail := range id.Details {
		out.Details = append(out.Details, &contractv1.Detail{Label: detail.Label, Value: detail.Value})
	}
	return out
}

func CredentialProblemProto(vendor Vendor, err error) *contractv1.CredentialProblem {
	problem := &contractv1.CredentialProblem{Provider: string(vendor)}
	var refusal Refusal
	if errors.As(err, &refusal) && refusal.Code == CodeDenied {
		problem.Message = "could not authenticate"
		problem.Hint = refusal.Message
		return problem
	}
	problem.Message = fmt.Sprintf("could not authenticate: %v", err)
	return problem
}
