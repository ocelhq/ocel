package providerserver

import (
	"context"
	"errors"
	"slices"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func (h *handlers) gate(requested string) (provider.Provider, Gate, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, Gate{}, err
	}
	kind := edgeKind(p, requested)
	bootstrap, err := p.Bootstrap(kind)
	if err != nil {
		return nil, Gate{}, provider.RefusalError(err)
	}
	return p, Gate{
		Bootstrap: bootstrap,
		KeyValues: p.KeyValues(),
		WrittenBy: h.session.writer,
		Edge:      kind,
		Statuses:  &h.session.bootstrapStatuses,
	}, nil
}

func (h *handlers) Bootstrap(ctx context.Context, req *contractv1.BootstrapRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	tier, err := decodeTier(req.GetTier())
	if err != nil {
		return err
	}
	p, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return err
	}
	intent := applyRequestOf(req)

	return streamResult(ctx, stream, func(sender *eventStream) (*progressv1.OperationEvent, error) {
		plan, err := PlanFromProto(req.GetConsented())
		if err != nil {
			return nil, err
		}
		if req.GetConsented() == nil {
			status, err := gate.Status(ctx, tier)
			if err != nil {
				return nil, provider.RefusalError(err)
			}
			if plan, err = gate.PlanFrom(ctx, status, intent); err != nil {
				return nil, provider.RefusalError(err)
			}
			sender.send(planEvent(ChangePlanProto(plan, string(tier), string(edgeKind(p, req.GetEdge().GetKind())))))
		}
		if req.GetDry() {
			return okResult(), nil
		}
		root := RootSpan(naming.SpanEnvironment, string(tier), bootstrapTitle(progress.Updating, plan), progressv1.Phase_PHASE_PROVISION)
		err = inSpan(sender, root,
			func(_ *eventStream, progress progress.Log) error {
				return gate.Apply(ctx, plan, tier, intent, progress)
			})
		if err != nil {
			return nil, err
		}
		return okResult(), nil
	})
}

func applyRequestOf(req *contractv1.BootstrapRequest) ApplyRequest {
	return ApplyRequest{
		Features:           req.GetFeatures(),
		Remove:             req.GetRemove(),
		Force:              req.GetForce(),
		RepairOnDeploy:     req.RepairOnDeploy,
		AcceptReplacements: req.GetAcceptReplacements(),
	}
}

func (h *handlers) DescribeBootstrap(ctx context.Context, req *contractv1.DescribeBootstrapRequest) (*contractv1.DescribeBootstrapResponse, error) {
	tier, err := decodeTier(req.GetTier())
	if err != nil {
		return nil, err
	}
	_, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}
	status, err := gate.Status(ctx, tier)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	recorded := map[string][]string{}
	if req.GetWithDependents() {
		if recorded, err = gate.RecordedFeatures(ctx, tier); err != nil {
			return nil, provider.RefusalError(err)
		}
	}

	resp := &contractv1.DescribeBootstrapResponse{
		Bootstrap: BootstrapStatusProto(status, h.session.writer, req.GetTier(), status.Features),
	}
	for _, f := range gate.Bootstrap.Catalogue() {
		resp.Features = append(resp.Features, &contractv1.Feature{
			Name:       f.Name,
			Summary:    f.Summary,
			DependsOn:  f.DependsOn,
			Edges:      featureEdges(f.Edges),
			Enabled:    slices.Contains(status.Features, f.Name),
			Dependents: ProjectsDependingOn(recorded, []string{f.Name}),
		})
	}
	return resp, nil
}

func featureEdges(kinds []edge.Kind) []string {
	var edges []string
	for _, kind := range kinds {
		edges = append(edges, string(kind))
	}
	return edges
}

func ChangePlanProto(plan provider.Plan, subject, kind string) *planv1.ChangePlan {
	out := &planv1.ChangePlan{Subject: subject, EdgeKind: kind}
	for _, group := range plan.Groups {
		out.Groups = append(out.Groups, GroupProto(group))
	}
	return out
}

func GroupProto(group provider.ChangeGroup) *planv1.ChangeGroup {
	rendered := &planv1.ChangeGroup{
		Kind:    group.Kind,
		Name:    group.Name,
		Feature: group.Feature,
		Action:  provider.ActionProto(group.Action),
		Reason:  group.Reason,
		Slow:    group.Slow,
	}
	for _, change := range group.Changes {
		rendered.Changes = append(rendered.Changes, &planv1.Change{
			Kind:   change.Kind,
			Name:   change.Name,
			Action: provider.ActionProto(change.Action),
			Reason: change.Reason,
			Slow:   change.Slow,
		})
	}
	return rendered
}

func PlanFromProto(shown *planv1.ChangePlan) (provider.Plan, error) {
	plan := provider.Plan{}
	for _, group := range shown.GetGroups() {
		action, err := changeAction(group.GetAction())
		if err != nil {
			return provider.Plan{}, err
		}
		converted := provider.ChangeGroup{
			Kind:    group.GetKind(),
			Name:    group.GetName(),
			Feature: group.GetFeature(),
			Action:  action,
			Reason:  group.GetReason(),
			Slow:    group.GetSlow(),
		}
		for _, change := range group.GetChanges() {
			if action, err = changeAction(change.GetAction()); err != nil {
				return provider.Plan{}, err
			}
			converted.Changes = append(converted.Changes, provider.Change{
				Kind:   change.GetKind(),
				Name:   change.GetName(),
				Action: action,
				Reason: change.GetReason(),
				Slow:   change.GetSlow(),
			})
		}
		plan.Groups = append(plan.Groups, converted)
	}
	return plan, nil
}

func changeAction(drawn planv1.Change_Action) (provider.ChangeAction, error) {
	action, known := provider.ActionFromProto(drawn)
	if !known {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"this plan names %s, an action this provider cannot perform; draw the plan again and consent to what it shows now",
			drawn)
	}
	return action, nil
}

func BootstrapStatusProto(current BootstrapStatus, writing provider.WrittenBy, tier environmentv1.Tier, required []string) *contractv1.BootstrapStatus {
	status := &contractv1.BootstrapStatus{
		Tier:           tier,
		Present:        current.Present,
		RepairOnDeploy: current.RepairOnDeploy,
		Writer:         writing.String(),
		Downgrade:      current.Downgrade(writing),
		Unfinished:     current.Unfinished,
	}
	for _, stack := range current.Stacks {
		status.Stacks = append(status.Stacks, &contractv1.BootstrapStack{
			Name:          stack.Name,
			Feature:       stack.Feature,
			Present:       stack.Present,
			DigestCurrent: stack.DigestCurrent,
			WrittenBy:     stack.WrittenBy,
			Required:      stack.Feature == "" || slices.Contains(required, stack.Feature),
			ReadError:     stack.ReadError,
		})
	}
	for _, feature := range required {
		if !slices.ContainsFunc(current.Stacks, func(stack provider.BootstrapStack) bool { return stack.Feature == feature }) {
			status.Stacks = append(status.Stacks, &contractv1.BootstrapStack{Feature: feature, Required: true})
		}
	}
	return status
}

func (h *handlers) PlanRemoveBootstrap(ctx context.Context, req *contractv1.BootstrapScope) (*planv1.ChangePlan, error) {
	tier, err := decodeTier(req.GetTier())
	if err != nil {
		return nil, err
	}
	_, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return nil, err
	}
	if err := gate.RefuseIfInUse(ctx, tier); err != nil {
		return nil, provider.RefusalError(err)
	}
	plan, err := gate.Bootstrap.PlanRemove(ctx, tier)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	return ChangePlanProto(plan, string(tier), soleRaisedEdge(plan)), nil
}

func soleRaisedEdge(plan provider.Plan) string {
	var kinds []edge.Kind
	for _, group := range plan.Groups {
		if group.Kind != edge.EdgeGroupKind {
			continue
		}
		if kind, ok := edge.EdgeGroupKindOf(group.Name); ok && !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) != 1 {
		return ""
	}
	return string(kinds[0])
}

func (h *handlers) RemoveBootstrap(ctx context.Context, req *contractv1.BootstrapScope, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	tier, err := decodeTier(req.GetTier())
	if err != nil {
		return err
	}
	_, gate, err := h.gate(req.GetEdge().GetKind())
	if err != nil {
		return err
	}

	shown, shownErr := PlanFromProto(req.GetConsented())
	root := RootSpan(naming.SpanEnvironment, string(tier), bootstrapTitle(progress.Removing, shown), progressv1.Phase_PHASE_DESTROY)
	return streamed(ctx, stream, root, func(_ *eventStream, progress progress.Log) error {
		if shownErr != nil {
			return shownErr
		}
		return gate.Remove(ctx, shown, tier, progress)
	})
}

func bootstrapTitle(verb progress.Verb, plan provider.Plan) progress.Title {
	var stacks []string
	for _, group := range plan.Groups {
		if group.Action.Writes() {
			stacks = append(stacks, group.Name)
		}
	}
	if len(stacks) == 0 {
		return verb.Title("the bootstrap")
	}
	return verb.Title(namedList("bootstrap stack", "bootstrap stacks", stacks))
}

func edgeKind(p provider.Provider, requested string) edge.Kind {
	if requested != "" {
		return edge.Kind(requested)
	}
	return p.Facts().DefaultEdge
}

func isServedEdge(p provider.Provider, kind edge.Kind) bool {
	return kind == p.Facts().DefaultEdge || slices.Contains(p.Facts().Edges, kind)
}

func (h *handlers) GetCredentialPermissions(_ context.Context, req *contractv1.CredentialPermissionsRequest) (*contractv1.CredentialPermissionsResponse, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	purpose, err := CredentialPurposeOf(req.GetPurpose())
	if err != nil {
		return nil, err
	}
	tier, err := decodeRequiredTier(req.GetTier())
	if err != nil {
		return nil, err
	}
	document, err := p.Credentials().Permissions(purpose, tier)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	groups := []*contractv1.CredentialGroup{{
		Heading:  document.Heading,
		Document: document.Document,
	}}

	kind := edgeKind(p, req.GetEdge().GetKind())
	if !isServedEdge(p, kind) {
		return &contractv1.CredentialPermissionsResponse{Groups: groups}, nil
	}
	front, err := p.Edges().Open(kind, req.GetEdge().GetOptions().AsMap())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if document := front.Hooks().DescribeCredentialPermissions; document != nil {
		documented, err := document(purpose)
		if err != nil {
			return nil, provider.RefusalError(err)
		}
		groups = append(groups, &contractv1.CredentialGroup{
			Heading:  documented.Heading,
			Document: documented.Document,
		})
	}
	return &contractv1.CredentialPermissionsResponse{Groups: groups}, nil
}

func CredentialPurposeOf(purpose contractv1.CredentialPurpose) (edge.CredentialPurpose, error) {
	switch purpose {
	case contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_BOOTSTRAP:
		return edge.PurposeBootstrap, nil
	case contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_DEPLOY:
		return edge.PurposeDeploy, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New(
			"credential permissions are rendered for bootstrap or deploy credentials; this request named neither"))
	}
}
