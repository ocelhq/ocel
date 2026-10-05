package providerserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (h *handlers) Preflight(ctx context.Context, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	tier, err := decodeTier(req.GetRequiredTier())
	if err != nil {
		return nil, err
	}
	p, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}

	resp := &contractv1.PreflightResponse{Identity: &contractv1.Identity{}}

	principal, err := p.Credentials().Whoami(ctx)
	if _, asked := provider.QuestionOf(err); asked {
		return nil, provider.RefusalError(err)
	}
	if err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, CredentialProblemProto(p.Facts().Vendor, err))
		return resp, nil
	}
	resp.Identity = PrincipalProto(p.Facts().Vendor, principal)
	resp.ContainerArchs, err = containerArchs(ctx, p.Runtime(), req.GetContainers())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	edgeCredentialsChecked, err := h.verifyEdgeCredentials(ctx, p, gate.Edge, req.GetEdge(), resp)
	if err != nil {
		return nil, err
	}
	if err := verifyDNSCredentials(ctx, p, gate.Edge, edgeCredentialsChecked, req.GetEdge().GetDns(), resp); err != nil {
		return nil, err
	}
	resp.HostnameRequired, err = h.hostnameRequired(p, req.GetEdge())
	if err != nil {
		return nil, provider.RefusalError(err)
	}

	required, err := bootstrapplan.RequiredFeatures(gate.Bootstrap.Catalogue(), req.GetFrameworks(), gate.Edge)
	if err != nil {
		return nil, provider.RefusalError(err)
	}

	status, err := gate.Status(ctx, tier)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	resp.Bootstrap = BootstrapStatusProto(status, h.session.writer, req.GetRequiredTier(), required)

	if status.Present {
		resp.InfraTier, resp.InfrastructurePresent = encodeTier(tier), true
		resp.KnownSlugs, err = listOtherSlugsIfUnrecorded(ctx, gate, tier, req.GetSlug())
		if err != nil {
			return nil, provider.RefusalError(err)
		}
		resp.DomainClaims, err = h.domainClaims(ctx, p, tier, req)
		if err != nil {
			return nil, provider.RefusalError(err)
		}
		if req.GetCheckHosts() {
			resp.HostChecks = h.hostChecks(ctx, p, provider.HostCheckRequest{Tier: tier, Edge: gate.Edge, Hostnames: req.GetHostCheckDomains()})
		}
		if tier == environment.TierPreview {
			resp.PreviewWildcard, err = recordedPreviewWildcard(ctx, p)
			if err != nil {
				return nil, provider.RefusalError(err)
			}
		}
		return resp, nil
	}

	sibling, err := gate.Status(ctx, tier.Sibling())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if sibling.Present {
		resp.InfraTier, resp.InfrastructurePresent = encodeTier(sibling.Tier), true
	}
	return resp, nil
}

func containerArchs(ctx context.Context, runtime provider.Runtime, containers []*contractv1.ContainerApp) (map[string]string, error) {
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

func (h *handlers) verifyEdgeCredentials(
	ctx context.Context,
	p provider.Provider,
	kind edge.Kind,
	sel *contractv1.EdgeSelection,
	resp *contractv1.PreflightResponse,
) (bool, error) {
	if kind == "" {
		return false, nil
	}
	front, err := h.edgeFor(p, sel)
	if err != nil {
		return false, provider.RefusalError(err)
	}
	verify := front.Hooks().VerifyCredentials
	if verify == nil {
		return false, nil
	}
	scope, err := verify(ctx)
	if err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, CredentialProblemProto(provider.Vendor(kind), err))
		return true, nil
	}
	resp.Identity.EdgeScope = scope.Account
	return true, nil
}

func verifyDNSCredentials(
	ctx context.Context,
	p provider.Provider,
	front edge.Kind,
	edgeCredentialsChecked bool,
	sel *contractv1.Dns,
	resp *contractv1.PreflightResponse,
) error {
	kind := provider.DNSKind(sel.GetKind())
	if kind == "" {
		return nil
	}
	writer, err := p.DNS().Open(kind, sel.GetZone(), front)
	if err != nil {
		return provider.RefusalError(err)
	}
	vendor := provider.Vendor(kind)
	if edgeCredentialsChecked && vendor == provider.Vendor(front) {
		return nil
	}
	if err := writer.VerifyCredentials(ctx); err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, CredentialProblemProto(vendor, err))
	}
	return nil
}

func (h *handlers) hostnameRequired(p provider.Provider, sel *contractv1.EdgeSelection) (bool, error) {
	front, err := h.edgeFor(p, sel)
	if err != nil {
		return false, err
	}
	paired, err := openEdgeRouter(p, front.Kind())
	if err != nil {
		return false, err
	}
	return !paired.Facts().AddressesItself, nil
}

func (h *handlers) domainClaims(ctx context.Context, p provider.Provider, tier environment.Tier, req *contractv1.PreflightRequest) ([]*contractv1.DomainClaim, error) {
	if len(req.GetDomains()) == 0 {
		return nil, nil
	}
	front, err := h.edgeFor(p, req.GetEdge())
	if err != nil {
		return nil, err
	}
	ours, err := boundHere(ctx, p.KeyValues(), tier, req.GetSlug())
	if err != nil {
		return nil, err
	}
	var mine string
	if slug := req.GetSlug(); slug != "" {
		mine = front.ProjectOwner(slug, tier)
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

func boundHere(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug string) ([]string, error) {
	if slug == "" {
		return nil, nil
	}
	state, err := (edgeStateStore{keyValues: store, name: stackrecords.EdgeStackKey(tier, slug)}).read(ctx)
	if err != nil {
		return nil, err
	}
	return state.Edge.Bound, nil
}

func listOtherSlugsIfUnrecorded(ctx context.Context, gate Gate, tier environment.Tier, slug string) ([]string, error) {
	if slug == "" {
		return nil, nil
	}
	own, err := keyvalue.ReadOrEmpty(ctx, gate.KeyValues, stackrecords.ProjectKey(tier, slug))
	if err != nil {
		return nil, fmt.Errorf("read %s's record: %w", slug, err)
	}
	if len(own.Value) > 0 {
		return nil, nil
	}
	recorded, err := gate.RecordedFeatures(ctx, tier)
	if err != nil {
		return nil, err
	}
	slugs := slices.Collect(maps.Keys(recorded))
	slices.Sort(slugs)
	return slugs, nil
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
	var refused refusal.Refusal
	if errors.As(err, &refused) {
		if trouble, named := credentialTrouble[refused.Code]; named {
			problem.Message, problem.Hint = trouble, refused.Message
			return problem
		}
	}
	problem.Message = fmt.Sprintf("could not authenticate: %v", err)
	return problem
}
