package providerkit

import (
	"context"
	"encoding/json"
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
	provider, gate, err := h.gate()
	if err != nil {
		return err
	}

	sender := newEventSender(ctx, stream.Send)
	defer func() { err = errors.Join(err, sender.close()) }()
	report := newReporter(sender, Stage{}, progressv1.Phase_PHASE_UNSPECIFIED)

	if err := applyBootstrap(ctx, provider, gate, class, req, report); err != nil {
		return sender.fail(refusalError(err))
	}
	sender.send(okResult())
	return nil
}

func applyBootstrap(ctx context.Context, provider Provider, gate Gate, class Class, req *contractv1.BootstrapRequest, report Reporter) error {
	catalogue := provider.Bootstrap().Catalogue()
	standing, err := gate.Standing(ctx, class)
	if err != nil {
		return err
	}
	if checkCompat(standing.Schema, standing.Present, BootstrapSchema) == needsCLIUpgrade {
		return schemaAhead(standing.Schema, class)
	}

	requested, err := featureClosure(catalogue, req.GetFeatures())
	if err != nil {
		return err
	}
	drop := featureDrop(catalogue, standing.Features, requested)
	if err := admitDrops(ctx, provider.Records(), drop, req.GetForce()); err != nil {
		return err
	}
	ordered, err := featureDeleteOrder(catalogue, drop)
	if err != nil {
		return err
	}

	autoHeal := standing.AutoHeal
	if req.AutoHeal != nil {
		autoHeal = req.GetAutoHeal()
	}
	if err := provider.Bootstrap().Apply(ctx, BootstrapRequest{
		Class:      class,
		Features:   requested,
		Drop:       ordered,
		Unattended: !req.GetAcceptReplacements(),
	}, report); err != nil {
		return err
	}
	if err := gate.RecordBootstrap(ctx, class, BootstrapState{AutoHeal: autoHeal}); err != nil {
		return err
	}
	return EnsureRecordSchema(ctx, provider.Records())
}

func admitDrops(ctx context.Context, records RecordStore, drop []string, force bool) error {
	if len(drop) == 0 || force {
		return nil
	}
	recorded, err := recordedFeatures(ctx, records)
	if err != nil {
		return err
	}
	dependents := projectsDependingOn(recorded, drop)
	if len(dependents) == 0 {
		return nil
	}
	return Refuse(CodeNotReady,
		"dropping %s would break %d project(s) already deployed here: %s — re-run with --force to drop it anyway, or leave the feature in the set",
		strings.Join(drop, ", "), len(dependents), strings.Join(dependents, ", "))
}

func recordedFeatures(ctx context.Context, records RecordStore) (map[string][]string, error) {
	held, err := records.List(ctx, ProjectsRecord())
	if err != nil {
		return nil, fmt.Errorf("read the projects deployed here: %w", err)
	}
	recorded := map[string][]string{}
	for _, record := range held {
		if len(record.Name) != 2 || len(record.Bytes) == 0 {
			continue
		}
		var project Project
		if err := json.Unmarshal(record.Bytes, &project); err != nil {
			return nil, fmt.Errorf("read %s's record: %w", record.Name, err)
		}
		recorded[record.Name[1]] = project.Features
	}
	return recorded, nil
}

func projectsDependingOn(recorded map[string][]string, dropped []string) []string {
	var out []string
	for project, features := range recorded {
		for _, name := range features {
			if slices.Contains(dropped, name) {
				out = append(out, project)
				break
			}
		}
	}
	slices.Sort(out)
	return out
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
		return nil, refusalError(err)
	}
	recorded := map[string][]string{}
	if req.GetWithDependents() {
		if recorded, err = recordedFeatures(ctx, provider.Records()); err != nil {
			return nil, refusalError(err)
		}
	}

	resp := &contractv1.DescribeBootstrapResponse{
		Bootstrap: bootstrapStatusProto(standing, h.session.writer, req.GetTier(), standing.Features),
	}
	for _, f := range provider.Bootstrap().Catalogue() {
		resp.Features = append(resp.Features, &contractv1.Feature{
			Name:       f.Name,
			Summary:    f.Summary,
			DependsOn:  f.DependsOn,
			Enabled:    slices.Contains(standing.Features, f.Name),
			Dependents: projectsDependingOn(recorded, []string{f.Name}),
		})
	}
	return resp, nil
}

func bootstrapStatusProto(standing Standing, writing Writer, tier environmentv1.Tier, required []string) *contractv1.BootstrapStatus {
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
	if err := vacant(ctx, gate, class); err != nil {
		return nil, refusalError(err)
	}
	surfaces, err := provider.Bootstrap().Removals(ctx, class)
	if err != nil {
		return nil, refusalError(err)
	}
	return &contractv1.RemovalPlan{
		EdgeKind: string(removalEdge(provider, req.GetEdge().GetKind())),
		Items:    removalItems(surfaces),
		Subject:  string(class),
	}, nil
}

func (h *handlers) RemoveBootstrap(ctx context.Context, req *contractv1.BootstrapScope, stream *connect.ServerStream[progressv1.OperationEvent]) (err error) {
	class, err := classOf(req.GetTier())
	if err != nil {
		return err
	}
	provider, gate, err := h.gate()
	if err != nil {
		return err
	}

	sender := newEventSender(ctx, stream.Send)
	defer func() { err = errors.Join(err, sender.close()) }()
	report := newReporter(sender, Stage{}, progressv1.Phase_PHASE_UNSPECIFIED)

	if err := removeBootstrap(ctx, provider, gate, class, report); err != nil {
		return sender.fail(refusalError(err))
	}
	sender.send(okResult())
	return nil
}

func removeBootstrap(ctx context.Context, provider Provider, gate Gate, class Class, report Reporter) error {
	if err := vacant(ctx, gate, class); err != nil {
		return err
	}
	if err := provider.Bootstrap().Remove(ctx, class, report); err != nil {
		return err
	}
	return Forget(ctx, provider.Records(), BootstrapRecord(class))
}

func vacant(ctx context.Context, gate Gate, class Class) error {
	occupancy, err := gate.Occupancy(ctx, class)
	if err != nil {
		return err
	}
	return occupancy.Refuse(class)
}

func removalEdge(provider Provider, requested string) edge.Kind {
	if requested != "" {
		return edge.Kind(requested)
	}
	return provider.Edges().Default()
}

func removalItems(surfaces []Removal) []*contractv1.RemovalItem {
	items := make([]*contractv1.RemovalItem, 0, len(surfaces))
	for _, surface := range surfaces {
		items = append(items, &contractv1.RemovalItem{
			Kind:   surface.Kind,
			Name:   surface.Name,
			Action: removalAction(surface.Action),
			Reason: surface.Reason,
			Slow:   surface.Slow,
		})
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
	tier, err := credentialTierOf(req.GetTier())
	if err != nil {
		return nil, err
	}
	document, err := provider.Credentials().Policy(tier)
	if err != nil {
		return nil, refusalError(err)
	}
	return &contractv1.CredentialPolicyResponse{Document: document}, nil
}

func credentialTierOf(tier contractv1.CredentialTier) (CredentialTier, error) {
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
