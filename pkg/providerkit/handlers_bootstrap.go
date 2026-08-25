package providerkit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func classOf(tier environmentv1.Tier) (Class, error) {
	switch tier {
	case environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_UNSPECIFIED:
		return ClassProduction, nil
	case environmentv1.Tier_TIER_PREVIEW:
		return ClassPreview, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"there is no %s bootstrap; a bootstrap is either production or preview",
			strings.ToLower(strings.TrimPrefix(tier.String(), "TIER_"))))
	}
}

func (h *handlers) gate(requested string) (Provider, Gate, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, Gate{}, err
	}
	kind := edgeKind(provider, requested)
	bootstrapper, err := provider.Bootstrap(kind)
	if err != nil {
		return nil, Gate{}, RefusalError(err)
	}
	return provider, Gate{
		Bootstrapper: bootstrapper,
		Records:      provider.Records(),
		Writer:       h.session.writer,
		Edge:         kind,
	}, nil
}

func (h *handlers) Bootstrap(ctx context.Context, req *contractv1.BootstrapRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	class, err := classOf(req.GetTier())
	if err != nil {
		return err
	}
	_, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return err
	}

	return streamed(ctx, stream, progressv1.Phase_PHASE_UNSPECIFIED, func(_ *eventSender, report Reporter) error {
		return gate.Apply(ctx, class, applyRequestOf(req), report)
	})
}

func applyRequestOf(req *contractv1.BootstrapRequest) ApplyRequest {
	return ApplyRequest{
		Features:           req.GetFeatures(),
		Remove:             req.GetRemove(),
		Force:              req.GetForce(),
		AutoHeal:           req.AutoHeal,
		AcceptReplacements: req.GetAcceptReplacements(),
	}
}

func (h *handlers) PlanBootstrap(ctx context.Context, req *contractv1.PlanBootstrapRequest) (*contractv1.PlanBootstrapResponse, error) {
	class, err := classOf(req.GetTier())
	if err != nil {
		return nil, err
	}
	if err := sameClass(class, req); err != nil {
		return nil, err
	}
	if err := sameEdge(req); err != nil {
		return nil, err
	}
	provider, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}
	standing, err := gate.Standing(ctx, class)
	if err != nil {
		return nil, RefusalError(err)
	}
	recorded := map[string][]string{}
	if req.GetWithDependents() {
		if recorded, err = gate.RecordedFeatures(ctx, class); err != nil {
			return nil, RefusalError(err)
		}
	}

	resp := &contractv1.PlanBootstrapResponse{
		Bootstrap: BootstrapStatusProto(standing, h.session.writer, req.GetTier(), standing.Features),
	}
	for _, f := range gate.Bootstrapper.Catalogue() {
		resp.Features = append(resp.Features, &contractv1.Feature{
			Name:       f.Name,
			Summary:    f.Summary,
			DependsOn:  f.DependsOn,
			Enabled:    slices.Contains(standing.Features, f.Name),
			Dependents: ProjectsDependingOn(recorded, []string{f.Name}),
		})
	}
	if req.GetIntent() == nil {
		return resp, nil
	}
	plan, err := gate.Plan(ctx, class, applyRequestOf(req.GetIntent()))
	if err != nil {
		return nil, RefusalError(err)
	}
	resp.Plan = ChangePlanProto(plan, string(class), string(edgeKind(provider, req.GetEdge().GetKind())))
	return resp, nil
}

func sameClass(class Class, req *contractv1.PlanBootstrapRequest) error {
	if req.GetIntent() == nil {
		return nil
	}
	intended, err := classOf(req.GetIntent().GetTier())
	if err != nil {
		return err
	}
	if intended == class {
		return nil
	}
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
		"this asks what would change in the %s bootstrap while carrying an intent aimed at the %s one; a plan answers for one bootstrap",
		class, intended))
}

func sameEdge(req *contractv1.PlanBootstrapRequest) error {
	if req.GetIntent() == nil {
		return nil
	}
	asked, intended := req.GetEdge().GetKind(), req.GetIntent().GetEdge().GetKind()
	if asked == intended {
		return nil
	}
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
		"this asks what would change behind the %s edge while carrying an intent aimed at the %s one; a plan answers for one edge",
		namedEdge(asked), namedEdge(intended)))
}

func namedEdge(kind string) string {
	if kind == "" {
		return "default"
	}
	return kind
}

func ChangePlanProto(plan BootstrapPlan, subject, kind string) *contractv1.ChangePlan {
	out := &contractv1.ChangePlan{Subject: subject, EdgeKind: kind}
	for _, group := range plan.Groups {
		out.Groups = append(out.Groups, GroupProto(group))
	}
	return out
}

func GroupProto(group ChangeGroup) *contractv1.ChangeGroup {
	rendered := &contractv1.ChangeGroup{
		Kind:    group.Kind,
		Name:    group.Name,
		Feature: group.Feature,
		Action:  planAction(group.Action),
		Reason:  group.Reason,
		Slow:    group.Slow,
	}
	for _, change := range group.Changes {
		rendered.Changes = append(rendered.Changes, &contractv1.Change{
			Kind:   change.Kind,
			Name:   change.Name,
			Action: planAction(change.Action),
			Reason: change.Reason,
			Slow:   change.Slow,
		})
	}
	return rendered
}

func planAction(action ChangeAction) contractv1.Change_Action {
	switch action {
	case ActionCreate:
		return contractv1.Change_ACTION_CREATE
	case ActionUpdate:
		return contractv1.Change_ACTION_UPDATE
	case ActionReplace:
		return contractv1.Change_ACTION_REPLACE
	case ActionDelete:
		return contractv1.Change_ACTION_DELETE
	case ActionDisableThenDelete:
		return contractv1.Change_ACTION_DISABLE_THEN_DELETE
	case ActionKeep:
		return contractv1.Change_ACTION_KEEP
	default:
		return contractv1.Change_ACTION_UNSPECIFIED
	}
}

func BootstrapStatusProto(standing Standing, writing Writer, tier environmentv1.Tier, required []string) *contractv1.BootstrapStatus {
	status := &contractv1.BootstrapStatus{
		Tier:           tier,
		Present:        standing.Present,
		Schema:         uint32(standing.Schema),
		RequiredSchema: BootstrapSchema,
		AutoHeal:       standing.AutoHeal,
		Writer:         writing.String(),
		Downgrade:      standing.Downgrade(writing),
	}
	for _, stack := range standing.Stacks {
		status.Stacks = append(status.Stacks, &contractv1.BootstrapStack{
			Name:          stack.Name,
			Feature:       stack.Feature,
			Present:       stack.Present,
			Schema:        stack.Schema,
			DigestCurrent: stack.DigestCurrent,
			WrittenBy:     stack.Writer,
			Required:      stack.Feature == "" || slices.Contains(required, stack.Feature),
		})
	}
	return status
}

func (h *handlers) PlanRemoveBootstrap(ctx context.Context, req *contractv1.BootstrapScope) (*contractv1.ChangePlan, error) {
	class, err := classOf(req.GetTier())
	if err != nil {
		return nil, err
	}
	provider, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}
	if err := gate.Vacant(ctx, class); err != nil {
		return nil, RefusalError(err)
	}
	plan, err := gate.Bootstrapper.PlanRemoval(ctx, class)
	if err != nil {
		return nil, RefusalError(err)
	}
	return ChangePlanProto(plan, string(class), string(edgeKind(provider, req.GetEdge().GetKind()))), nil
}

func (h *handlers) RemoveBootstrap(ctx context.Context, req *contractv1.BootstrapScope, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	class, err := classOf(req.GetTier())
	if err != nil {
		return err
	}
	_, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return err
	}

	return streamed(ctx, stream, progressv1.Phase_PHASE_UNSPECIFIED, func(_ *eventSender, report Reporter) error {
		return gate.Remove(ctx, class, report)
	})
}

func edgeKind(provider Provider, requested string) edge.Kind {
	if requested != "" {
		return edge.Kind(requested)
	}
	return provider.Edges().Default()
}

func (h *handlers) GetCredentialPolicy(_ context.Context, req *contractv1.CredentialPolicyRequest) (*contractv1.CredentialPolicyResponse, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, err
	}
	tier, err := CredentialTierOf(req.GetTier())
	if err != nil {
		return nil, err
	}
	document, err := provider.Credentials().Policy(tier)
	if err != nil {
		return nil, RefusalError(err)
	}
	return &contractv1.CredentialPolicyResponse{Document: document}, nil
}

func CredentialTierOf(tier contractv1.CredentialTier) (CredentialTier, error) {
	switch tier {
	case contractv1.CredentialTier_CREDENTIAL_TIER_BOOTSTRAP:
		return TierBootstrap, nil
	case contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY:
		return TierDeploy, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New(
			"a credential policy is rendered for the bootstrap tier or the deploy tier; this request named neither"))
	}
}
