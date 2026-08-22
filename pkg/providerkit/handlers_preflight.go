package providerkit

import (
	"context"
	"errors"
	"fmt"
	"slices"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func (h *handlers) Preflight(ctx context.Context, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	class, err := classOf(req.GetRequiredTier())
	if err != nil {
		return nil, err
	}
	provider, gate, err := h.gate()
	if err != nil {
		return nil, err
	}

	resp := &contractv1.PreflightResponse{Identity: &contractv1.Identity{}}

	identity, err := provider.Credentials().Whoami(ctx)
	if err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, credentialProblem(provider.Vendor(), err))
		return resp, nil
	}
	resp.Identity = identityProto(provider.Vendor(), identity)

	required, err := RequiredFeatures(provider.Bootstrap().Catalogue(), nil, req.GetEdge().GetKind())
	if err != nil {
		return nil, refusalError(err)
	}

	standing, err := gate.Standing(ctx, class)
	if err != nil {
		return nil, refusalError(err)
	}
	resp.Bootstrap = bootstrapStatusProto(standing, h.session.writer, req.GetRequiredTier(), required)

	if standing.Present {
		resp.InfraTier, resp.InfrastructurePresent = tierOf(class), true
		if err := checkCompat(standing.Schema, true, BootstrapSchema).explain(standing.Schema, BootstrapSchema, bootstrapCommand(class)); err != nil {
			return nil, refusalError(err)
		}
		resp.KnownSlugs, err = slugsBesides(ctx, provider.Records(), req.GetSlug())
		if err != nil {
			return nil, refusalError(err)
		}
		return resp, nil
	}

	sibling, err := gate.Standing(ctx, siblingOf(class))
	if err != nil {
		return nil, refusalError(err)
	}
	if sibling.Present {
		resp.InfraTier, resp.InfrastructurePresent = tierOf(sibling.Class), true
	}
	return resp, nil
}

func slugsBesides(ctx context.Context, records RecordStore, slug string) ([]string, error) {
	if slug == "" {
		return nil, nil
	}
	recorded, err := recordedFeatures(ctx, records)
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

func identityProto(vendor Vendor, id Identity) *contractv1.Identity {
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

func credentialProblem(vendor Vendor, err error) *contractv1.CredentialProblem {
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
