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

func (h *handlers) gate() (Provider, Gate, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, Gate{}, err
	}
	return provider, Gate{
		Bootstrapper: provider.Bootstrap(),
		Records:      provider.Records(),
		Writer:       h.session.writer,
	}, nil
}

func (h *handlers) Bootstrap(ctx context.Context, req *contractv1.BootstrapRequest, stream *connect.ServerStream[progressv1.OperationEvent]) (err error) {
	class, err := classOf(req.GetTier())
	if err != nil {
		return err
	}
	_, gate, err := h.gate()
	if err != nil {
		return err
	}

	sender := newEventSender(ctx, stream.Send)
	defer func() { err = errors.Join(err, sender.close()) }()
	report := newReporter(sender, Stage{}, progressv1.Phase_PHASE_UNSPECIFIED)

	if err := gate.Apply(ctx, class, applyRequestOf(req), report); err != nil {
		return sender.fail(RefusalError(err))
	}
	sender.send(okResult())
	return nil
}

func applyRequestOf(req *contractv1.BootstrapRequest) ApplyRequest {
	return ApplyRequest{
		Features:           req.GetFeatures(),
		Force:              req.GetForce(),
		AutoHeal:           req.AutoHeal,
		AcceptReplacements: req.GetAcceptReplacements(),
	}
}

func (h *handlers) DescribeBootstrap(ctx context.Context, req *contractv1.DescribeBootstrapRequest) (*contractv1.DescribeBootstrapResponse, error) {
	class, err := classOf(req.GetTier())
	if err != nil {
		return nil, err
	}
	provider, gate, err := h.gate()
	if err != nil {
		return nil, err
	}
	standing, err := gate.Standing(ctx, class)
	if err != nil {
		return nil, RefusalError(err)
	}
	recorded := map[string][]string{}
	if req.GetWithDependents() {
		if recorded, err = gate.RecordedFeatures(ctx); err != nil {
			return nil, RefusalError(err)
		}
	}

	resp := &contractv1.DescribeBootstrapResponse{
		Bootstrap: BootstrapStatusProto(standing, h.session.writer, req.GetTier(), standing.Features),
	}
	for _, f := range provider.Bootstrap().Catalogue() {
		resp.Features = append(resp.Features, &contractv1.Feature{
			Name:       f.Name,
			Summary:    f.Summary,
			DependsOn:  f.DependsOn,
			Enabled:    slices.Contains(standing.Features, f.Name),
			Dependents: ProjectsDependingOn(recorded, []string{f.Name}),
		})
	}
	return resp, nil
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

func (h *handlers) PlanRemoveBootstrap(ctx context.Context, req *contractv1.BootstrapScope) (*contractv1.RemovalPlan, error) {
	class, err := classOf(req.GetTier())
	if err != nil {
		return nil, err
	}
	provider, gate, err := h.gate()
	if err != nil {
		return nil, err
	}
	if err := gate.Vacant(ctx, class); err != nil {
		return nil, RefusalError(err)
	}
	surfaces, err := provider.Bootstrap().Removals(ctx, class)
	if err != nil {
		return nil, RefusalError(err)
	}
	return &contractv1.RemovalPlan{
		EdgeKind: string(removalEdge(provider, req.GetEdge().GetKind())),
		Items:    RemovalItems(surfaces),
		Subject:  string(class),
	}, nil
}

func (h *handlers) RemoveBootstrap(ctx context.Context, req *contractv1.BootstrapScope, stream *connect.ServerStream[progressv1.OperationEvent]) (err error) {
	class, err := classOf(req.GetTier())
	if err != nil {
		return err
	}
	_, gate, err := h.gate()
	if err != nil {
		return err
	}

	sender := newEventSender(ctx, stream.Send)
	defer func() { err = errors.Join(err, sender.close()) }()
	report := newReporter(sender, Stage{}, progressv1.Phase_PHASE_UNSPECIFIED)

	if err := gate.Remove(ctx, class, report); err != nil {
		return sender.fail(RefusalError(err))
	}
	sender.send(okResult())
	return nil
}

func removalEdge(provider Provider, requested string) edge.Kind {
	if requested != "" {
		return edge.Kind(requested)
	}
	return provider.Edges().Default()
}

func RemovalItems(surfaces []Removal) []*contractv1.RemovalItem {
	items := make([]*contractv1.RemovalItem, 0, len(surfaces))
	for _, surface := range surfaces {
		items = append(items, surfaceItem(surface))
	}
	return items
}

func removalAction(action edge.SurfaceAction) contractv1.RemovalItem_Action {
	switch action {
	case edge.SurfaceDelete:
		return contractv1.RemovalItem_ACTION_DELETE
	case edge.SurfaceDisableThenDelete:
		return contractv1.RemovalItem_ACTION_DISABLE_THEN_DELETE
	case edge.SurfaceKeep:
		return contractv1.RemovalItem_ACTION_KEEP
	default:
		return contractv1.RemovalItem_ACTION_UNSPECIFIED
	}
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
