package providerserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (h *handlers) Preflight(ctx context.Context, req *contractv1.PreflightRequest, stream *connect.ServerStream[contractv1.PreflightEvent]) (err error) {
	tier, err := decodeTier(req.GetRequiredTier())
	if err != nil {
		return err
	}
	p, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return err
	}
	sender := newEventStream(ctx, func(ev *progressv1.OperationEvent) error {
		return stream.Send(&contractv1.PreflightEvent{Body: &contractv1.PreflightEvent_Progress{Progress: ev}})
	})
	resp, err := h.preflight(ctx, preflightSteps{spans: newSpanEvents(sender)}, p, gate, tier, req)
	err = errors.Join(err, sender.close())
	if err != nil {
		return err
	}
	return stream.Send(&contractv1.PreflightEvent{Body: &contractv1.PreflightEvent_Response{Response: resp}})
}

func (h *handlers) preflight(ctx context.Context, steps preflightSteps, p provider.Provider, gate Gate, tier environment.Tier, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	resp := &contractv1.PreflightResponse{Identity: &contractv1.Identity{}}
	vendor := p.Facts().Vendor

	var principal provider.Principal
	err := steps.credentials(string(vendor), func() (err error) {
		principal, err = p.Credentials().Whoami(ctx)
		return err
	})
	if _, asked := provider.QuestionOf(err); asked {
		return nil, provider.RefusalError(err)
	}
	if err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, CredentialProblemProto(vendor, err))
		return resp, nil
	}
	resp.Identity = PrincipalProto(vendor, principal)

	edgeCredentialsChecked, err := h.verifyEdgeCredentials(ctx, steps, p, gate.Edge, req.GetEdge(), resp)
	if err != nil {
		return nil, err
	}
	if err := verifyDNSCredentials(ctx, steps, p, gate.Edge, edgeCredentialsChecked, req.GetEdge().GetDns(), resp); err != nil {
		return nil, err
	}
	if err := verifyProjectPushAccess(ctx, steps, p, req, resp); err != nil {
		return nil, err
	}
	if len(resp.GetCredentialProblems()) > 0 {
		return resp, nil
	}

	if len(req.GetContainers()) > 0 {
		err = steps.run(string(vendor), progress.Reading.Title("the architecture your containers run on"), func() (err error) {
			resp.ContainerArchs, err = containerArchs(ctx, p.Runtime(), req.GetContainers())
			return err
		})
		if err != nil {
			return nil, provider.RefusalError(err)
		}
	}
	resp.HostnameRequired, err = h.hostnameRequired(p, req.GetEdge())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	required, err := bootstrapplan.RequiredFeatures(gate.Bootstrap.Catalogue(), req.GetFrameworks(), gate.Edge)
	if err != nil {
		return nil, provider.RefusalError(err)
	}

	var status bootstrapStatus
	err = steps.run(string(vendor), progress.Reading.Title(bootstrapName(req.GetRequiredTier())), func() (err error) {
		status, err = readBootstrap(ctx, p, gate, tier, req)
		return err
	})
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	resp.Bootstrap = BootstrapStatusProto(status.own, h.session.writer, req.GetRequiredTier(), required)
	if !status.own.Present {
		if status.siblingPresent {
			resp.InfraTier, resp.InfrastructurePresent = encodeTier(tier.Sibling()), true
		}
		return resp, nil
	}
	resp.InfraTier, resp.InfrastructurePresent = encodeTier(tier), true
	resp.KnownSlugs, resp.PreviewWildcard = status.knownSlugs, status.previewWildcard
	if err := verifyOwnPushAccess(ctx, steps, p, tier, req, resp); err != nil {
		return nil, err
	}
	if len(resp.GetCredentialProblems()) > 0 {
		return resp, nil
	}

	if len(req.GetDomains()) > 0 {
		err = steps.run(string(gate.Edge), progress.Checking.Title("who serves "+strings.Join(req.GetDomains(), ", ")), func() (err error) {
			resp.DomainClaims, err = h.domainClaims(ctx, p, tier, req)
			return err
		})
		if err != nil {
			return nil, provider.RefusalError(err)
		}
	}
	if req.GetCheckHosts() {
		_ = steps.run(string(vendor), progress.Checking.Title("the hosts"), func() error {
			resp.HostChecks = h.hostChecks(ctx, p, provider.HostCheckRequest{Tier: tier, Edge: gate.Edge, Hostnames: req.GetHostCheckDomains()})
			return nil
		})
	}
	return resp, nil
}

type preflightSteps struct {
	spans *spanEvents
}

func (s preflightSteps) run(subject string, title progress.Title, do func() error) error {
	span := Span{ID: newSpanID(), Title: sanitizeTitle(title), Phase: progressv1.Phase_PHASE_CHECK, Subject: subject}
	return s.spans.run(span, func(*spanRun) error { return do() })
}

func (s preflightSteps) credentials(subject string, verify func() error) error {
	return s.run(subject, progress.Checking.Title("your credentials"), verify)
}

func bootstrapName(tier environmentv1.Tier) string {
	if tier == environmentv1.Tier_TIER_UNSPECIFIED {
		return "the Ocel bootstrap"
	}
	name, _ := decodeTier(tier)
	return "the " + string(name) + " bootstrap"
}

type bootstrapStatus struct {
	own             BootstrapStatus
	siblingPresent  bool
	knownSlugs      []string
	previewWildcard *contractv1.PreviewWildcard
}

func readBootstrap(ctx context.Context, p provider.Provider, gate Gate, tier environment.Tier, req *contractv1.PreflightRequest) (status bootstrapStatus, err error) {
	if status.own, err = gate.Status(ctx, tier); err != nil {
		return status, err
	}
	if !status.own.Present {
		sibling, err := gate.Status(ctx, tier.Sibling())
		status.siblingPresent = sibling.Present
		return status, err
	}
	if status.knownSlugs, err = listOtherSlugsIfUnrecorded(ctx, gate, tier, req.GetSlug()); err != nil {
		return status, err
	}
	if tier == environment.TierPreview {
		status.previewWildcard, err = recordedPreviewWildcard(ctx, p)
	}
	return status, err
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
	steps preflightSteps,
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
	var scope edge.CredentialIdentity
	err = steps.credentials(string(kind), func() (err error) {
		scope, err = verify(ctx)
		return err
	})
	if _, asked := provider.QuestionOf(err); asked {
		return false, provider.RefusalError(err)
	}
	if err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, CredentialProblemProto(provider.Vendor(kind), err))
		return true, nil
	}
	resp.Identity.EdgeScope = scope.Account
	return true, nil
}

func verifyDNSCredentials(
	ctx context.Context,
	steps preflightSteps,
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
	err = steps.credentials(string(vendor), func() error { return writer.VerifyCredentials(ctx) })
	if _, asked := provider.QuestionOf(err); asked {
		return provider.RefusalError(err)
	}
	if err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, CredentialProblemProto(vendor, err))
	}
	return nil
}

func verifyProjectPushAccess(ctx context.Context, steps preflightSteps, p provider.Provider, req *contractv1.PreflightRequest, resp *contractv1.PreflightResponse) error {
	project := req.GetProjectRegistry()
	if project.GetServer() == "" || len(req.GetContainers()) == 0 {
		return nil
	}
	return verifyPushAccess(ctx, steps, p, req.GetSlug(), provider.RegistryTarget{
		Server:    project.GetServer(),
		Namespace: project.GetNamespace(),
		Username:  project.GetUsername(),
		Password:  project.GetPassword(),
	}, req.GetContainers(), resp)
}

func verifyOwnPushAccess(ctx context.Context, steps preflightSteps, p provider.Provider, tier environment.Tier, req *contractv1.PreflightRequest, resp *contractv1.PreflightResponse) error {
	ensure := p.Hooks().EnsureImageRegistry
	if ensure == nil || req.GetProjectRegistry().GetServer() != "" || len(req.GetContainers()) == 0 {
		return nil
	}
	own, err := ensure(ctx, tier)
	if _, asked := provider.QuestionOf(err); asked {
		return provider.RefusalError(err)
	}
	if err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, CredentialProblemProto(p.Facts().Vendor, fmt.Errorf("resolve the registry this provider hosts: %w", err)))
		return nil
	}
	if !own.Named() {
		return nil
	}
	return verifyPushAccess(ctx, steps, p, req.GetSlug(), own, req.GetContainers(), resp)
}

func verifyPushAccess(ctx context.Context, steps preflightSteps, p provider.Provider, slug string, target provider.RegistryTarget, containers []*contractv1.ContainerApp, resp *contractv1.PreflightResponse) error {
	named := strings.TrimSuffix(target.Server+"/"+strings.Trim(target.Namespace, "/"), "/")
	err := steps.run(target.Server, progress.Checking.Title("push access to "+named), func() error {
		store, err := imageStoreFor(ctx, p, target)
		if err != nil || store == nil {
			return err
		}
		for _, container := range containers {
			if err := store.ProbePush(ctx, images.RegistryRepository(slug, container.GetApp())); err != nil {
				return err
			}
		}
		return nil
	})
	if _, asked := provider.QuestionOf(err); asked {
		return provider.RefusalError(err)
	}
	if err != nil {
		resp.CredentialProblems = append(resp.CredentialProblems, CredentialProblemProto(provider.Vendor(target.Server), err))
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
	state, err := (&edgeStateStore{keyValues: store, name: stackrecords.EdgeStackKey(tier, slug)}).read(ctx)
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
