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
	provider, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}

	resp := &contractv1.PreflightResponse{Identity: &contractv1.Identity{}}

	identity, err := provider.Credentials().Whoami(ctx)
	if err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, CredentialProblemProto(provider.Vendor(), err))
		return resp, nil
	}
	resp.Identity = IdentityProto(provider.Vendor(), identity)

	required, err := RequiredFeatures(gate.Bootstrapper.Catalogue(), req.GetFrameworks(), req.GetEdge().GetKind())
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
		if err := checkCompat(standing.Schema, true, BootstrapSchema).explain(standing.Schema, BootstrapSchema, bootstrapCommand(class)); err != nil {
			return nil, RefusalError(err)
		}
		resp.KnownSlugs, err = slugsBesides(ctx, gate, class, req.GetSlug())
		if err != nil {
			return nil, RefusalError(err)
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
