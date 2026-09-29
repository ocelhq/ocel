package providerserver

import (
	"context"
	"errors"
	"fmt"
	"slices"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type projectRemoval struct {
	*sharedStack
	provider provider.Provider
	store    edgeStateStore
	state    stackrecords.EdgeState
	cutover  dnsCutover

	slug    string
	tier    environment.Tier
	scope   string
	infra   []naming.StackName
	apps    []naming.StackName
	pointer []string
}

func (h *handlers) openRemoval(ctx context.Context, req *contractv1.ProjectRequest) (*projectRemoval, error) {
	vendor, err := h.session.use()
	if err != nil {
		return nil, err
	}
	if req.GetSlug() == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "this call names no project, and a removal plan is drawn for one")
	}
	tier, err := decodeTier(req.GetEnvironment().GetTier())
	if err != nil {
		return nil, err
	}
	scope, err := envScope(req.GetEnvironment())
	if err != nil {
		return nil, err
	}
	store := edgeStateStore{keyValues: vendor.KeyValues(), name: stackrecords.EdgeStackKey(tier, req.GetSlug())}
	state, err := store.read(ctx)
	if err != nil {
		return nil, err
	}
	front, err := h.removalEdge(vendor, state, req.GetEdge())
	if err != nil {
		return nil, err
	}
	writer, err := dnsFor(vendor, front, req.GetEdge())
	if err != nil {
		return nil, err
	}
	shared, err := openSharedStack(vendor, front, tier, req.GetSlug())
	if err != nil {
		return nil, err
	}
	removal := &projectRemoval{
		sharedStack: shared,
		provider:    vendor,
		cutover:     newDNSCutover(front, writer, req.GetEdge().GetDns().GetZone(), vendor.Liveness()),
		store:       store,
		state:       state,
		slug:        req.GetSlug(),
		tier:        tier,
		scope:       scope,
	}
	if !removal.state.Edge.Empty() {
		stack, err := front.Open(removal.state.Edge)
		if err != nil {
			return nil, err
		}
		removal.setEdgeStack(stack)
	}
	entries, err := stackrecords.List(ctx, vendor.KeyValues(), tier, req.GetSlug())
	if err != nil {
		return nil, err
	}
	removal.infra, removal.apps, removal.pointer = classifyStacks(entries, tier)
	return removal, nil
}

func (h *handlers) PlanRemoveProject(ctx context.Context, req *contractv1.ProjectRequest) (*planv1.ChangePlan, error) {
	removal, err := h.openRemoval(ctx, req)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	plan, err := removal.plan()
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	return plan, nil
}

func (r *projectRemoval) plan() (*planv1.ChangePlan, error) {
	plan := &planv1.ChangePlan{EdgeKind: string(r.front.Kind()), Subject: r.slug}
	vendor := string(r.provider.Facts().Vendor)
	for _, stack := range r.apps {
		plan.Groups = append(plan.Groups, &planv1.ChangeGroup{
			Kind:    provider.StackGroupKind,
			Name:    vendor + "/" + stack.String(),
			Feature: stack.App,
			Action:  planv1.Change_ACTION_DELETE,
			Reason:  "everything this release of " + stack.App + " provisioned",
		})
	}
	for _, stack := range r.infra {
		plan.Groups = append(plan.Groups, &planv1.ChangeGroup{
			Kind:    provider.StackGroupKind,
			Name:    vendor + "/" + stack.String(),
			Feature: stack.Env,
			Action:  planv1.Change_ACTION_DELETE,
			Reason:  "the resources every app in " + stack.Env + " binds to",
			Slow:    true,
		})
	}
	for _, group := range r.front.ProjectRemovals(edge.ProjectScope{
		Slug:      r.slug,
		Tier:      r.tier,
		Hostnames: r.state.Hostnames(),
		Front:     r.state.Edge.Front,
	}) {
		converted, err := edgeGroupProto(group)
		if err != nil {
			return nil, err
		}
		plan.Groups = append(plan.Groups, converted)
	}
	plan.Groups = append(plan.Groups, r.recordGroups()...)
	plan.Groups = append(plan.Groups,
		&planv1.ChangeGroup{
			Kind:   "variable values",
			Name:   r.slug,
			Action: planv1.Change_ACTION_DELETE,
			Reason: "the values this project's apps read, and the bindings published beside them",
		},
		&planv1.ChangeGroup{
			Kind:   "stored objects",
			Name:   r.slug,
			Action: planv1.Change_ACTION_DELETE,
			Reason: "the artifacts, assets and cache entries every release of this project wrote",
		})
	return plan, nil
}

func (r *projectRemoval) recordGroups() []*planv1.ChangeGroup {
	var groups []*planv1.ChangeGroup
	for _, rec := range r.state.WrittenRecords() {
		groups = append(groups, &planv1.ChangeGroup{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: planv1.Change_ACTION_DELETE,
			Reason: "ocel wrote it; it is removed only while its live value is still the one ocel wrote",
		})
	}
	for _, rec := range r.state.ManualRecords() {
		groups = append(groups, &planv1.ChangeGroup{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: planv1.Change_ACTION_KEEP,
			Reason: "you created it yourself; ocel never wrote it, so it is yours to remove",
		})
	}
	for _, cert := range r.state.Certificates() {
		groups = append(groups, certificateGroup(cert))
	}
	return groups
}

func certificateGroup(cert provider.Certificate) *planv1.ChangeGroup {
	if !cert.Requested {
		return &planv1.ChangeGroup{
			Kind:   "certificate",
			Name:   cert.ID,
			Action: planv1.Change_ACTION_KEEP,
			Reason: "ocel never requested it, so it is not ocel's to delete: whoever placed it is who removes it",
		}
	}
	return &planv1.ChangeGroup{
		Kind:   "certificate",
		Name:   cert.ID,
		Action: planv1.Change_ACTION_DELETE,
		Reason: "ocel requested it for a hostname this project serves, and nothing is left to serve",
	}
}

func (h *handlers) RemoveProject(ctx context.Context, req *contractv1.ProjectRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	unit := UnitStage(naming.UnitEnvironment, req.GetSlug(), removalTitle(req.GetEnvironment()), progressv1.Phase_PHASE_DESTROY)
	return streamed(ctx, stream, unit, func(_ *eventStream, progress progress.Progress) error {
		removal, err := h.openRemoval(ctx, req)
		if err != nil {
			return err
		}
		if err := removal.refuseIfPlanGrew(req.GetConsented()); err != nil {
			return err
		}
		return removal.run(ctx, progress)
	})
}

func removalTitle(env *environmentv1.Environment) string {
	switch {
	case env.GetTier() != environmentv1.Tier_TIER_PREVIEW:
		return "Destroying the production footprint"
	case env.GetIdentity() == EveryPreview:
		return "Destroying the footprint of every preview"
	default:
		return "Destroying the footprint of preview " + env.GetIdentity()
	}
}

func (r *projectRemoval) refuseIfPlanGrew(consented *planv1.ChangePlan) error {
	if len(consented.GetGroups()) == 0 {
		return nil
	}
	current, err := r.plan()
	if err != nil {
		return err
	}
	shown, err := PlanFromProto(consented)
	if err != nil {
		return err
	}
	drawn, err := PlanFromProto(current)
	if err != nil {
		return err
	}
	return bootstrapplan.RefuseUnconsentedChanges(shown, drawn)
}

func (r *projectRemoval) run(ctx context.Context, progress progress.Progress) error {
	var errs []error

	if err := r.unbind(ctx, progress); err != nil {
		errs = append(errs, err)
	}
	stacks := append(slices.Clone(r.apps), r.infra...)
	for i, stack := range stacks {
		progress.Say(fmt.Sprintf("Destroying stack %s (%d of %d)", stack, i+1, len(stacks)))
		if err := r.destroyStack(ctx, stack, progress); err != nil {
			errs = append(errs, err)
		}
	}
	if err := r.destroyUnrecordedHolders(ctx, progress); err != nil {
		errs = append(errs, err)
	}
	written, certificates := r.state.PointerRecords(), r.state.Certificates()
	if err := r.tearDownEdge(ctx, progress); err != nil {
		errs = append(errs, err)
	} else {
		if err := r.releaseRecords(ctx, written, progress); err != nil {
			errs = append(errs, err)
		}
		if err := r.discardCertificates(ctx, certificates, progress); err != nil {
			errs = append(errs, err)
		}
	}
	if err := r.purgeValues(ctx, progress); err != nil {
		errs = append(errs, err)
	}
	if err := r.purgeObjects(ctx, progress); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		progress.Say(fmt.Sprintf("Keeping %s on record in %s: a rerun reads its progress from what is still here", r.slug, r.tier))
		return err
	}
	return r.forgetProjectIfEmpty(ctx, progress)
}

func (r *projectRemoval) unbind(ctx context.Context, runProgress progress.Progress) error {
	stack := r.edgeStack()
	if stack == nil || stack.State().Empty() {
		return nil
	}
	var errs []error
	for _, hostname := range stack.State().Bound {
		runProgress.Say(fmt.Sprintf("Unbinding %s from the %s edge", hostname, r.front.Kind()))
		if err := progress.Heeded(stack.UnbindDomain(ctx, hostname), runProgress); err != nil {
			errs = append(errs, fmt.Errorf("unbind %q before the origin it fronts is destroyed: %w", hostname, err))
		}
	}
	for _, pointer := range r.pointers() {
		runProgress.Say(fmt.Sprintf("Removing the %s routing pointer from the %s edge", pointer, r.front.Kind()))
		if _, err := r.removePointer(ctx, pointer, runProgress); err != nil {
			errs = append(errs, fmt.Errorf("remove pointer %q before the origin it points at is destroyed: %w", pointer, err))
		}
	}
	return errors.Join(errs...)
}

func (r *projectRemoval) pointers() []string {
	if r.tier == environment.TierProduction {
		return []string{router.DefaultPointer}
	}
	return r.pointer
}

func (r *projectRemoval) destroyStack(ctx context.Context, stack naming.StackName, progress progress.Progress) error {
	ref := provider.StackRef{Project: r.slug, Tier: r.tier, Name: stack}
	if err := r.provider.Stacks().Destroy(ctx, ref, progress); err != nil {
		return fmt.Errorf("destroy %s: %w", stack, err)
	}
	return stackrecords.Forget(ctx, r.provider.KeyValues(), r.tier, r.slug, stack)
}

func (r *projectRemoval) destroyUnrecordedHolders(ctx context.Context, progress progress.Progress) error {
	restored, err := resources.RecordUnrecordedHolders(ctx, r.provider.KeyValues(), r.tier, r.slug,
		func(stack naming.StackName) bool { return isOfTier(stack, r.tier) })
	if err != nil {
		return err
	}
	var errs []error
	for _, stack := range restored {
		progress.Say(fmt.Sprintf("Destroying stack %s: its record was gone, and it still holds what its releases share", stack))
		if err := r.destroyStack(ctx, stack, progress); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *projectRemoval) tearDownEdge(ctx context.Context, progress progress.Progress) error {
	if stack := r.edgeStack(); stack == nil || stack.State().Empty() {
		return nil
	}
	progress.Say(fmt.Sprintf("Destroying the %s edge stack of %s", r.front.Kind(), r.slug))
	if err := r.destroy(ctx); err != nil {
		return fmt.Errorf("destroy the edge stack: %w", err)
	}
	r.state = stackrecords.EdgeState{}
	return r.store.write(ctx, r.state)
}

func (r *projectRemoval) releaseRecords(ctx context.Context, written []edge.Record, progress progress.Progress) error {
	if err := r.cutover.release(ctx, written, progress.Say); err != nil {
		return fmt.Errorf("remove the DNS records pointing at what this project served: %w", err)
	}
	return nil
}

func (r *projectRemoval) discardCertificates(ctx context.Context, certificates []provider.Certificate, progress progress.Progress) error {
	var errs []error
	for _, cert := range certificates {
		if err := discardCertificateAndRecords(ctx, r.provider, r.cutover, cert, provider.Certificate{}, progress); err != nil {
			errs = append(errs, fmt.Errorf("discard the certificate ocel requested for this project: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (r *projectRemoval) purgeValues(ctx context.Context, progress progress.Progress) error {
	progress.Say(fmt.Sprintf("Removing the stored variable values of %s in %s", r.slug, r.tier))
	store := envvars.Store{KeyValues: r.provider.KeyValues(), Cipher: r.provider.Cipher()}
	if err := envsource.ForgetProject(ctx, store, r.tier, r.slug); err != nil {
		return fmt.Errorf("forget %s's env source: %w", r.slug, err)
	}
	if _, err := store.Purge(ctx, envvars.Scope{Project: r.slug, Tier: r.tier}); err != nil {
		return fmt.Errorf("remove %s's stored variable values: %w", r.slug, err)
	}
	return nil
}

func (r *projectRemoval) purgeObjects(ctx context.Context, progress progress.Progress) error {
	var errs []error
	for _, env := range r.environments() {
		prefix := naming.Coordinate{Project: naming.Sanitize(r.slug), Env: env}.StoragePrefix()
		if err := r.provider.Artifacts().RemovePrefix(ctx, r.tier, prefix, progress); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", prefix, err))
		}
	}
	return errors.Join(errs...)
}

func (r *projectRemoval) environments() []string {
	envs := slices.Clone(r.pointer)
	if r.tier == environment.TierProduction && !slices.Contains(envs, stackrecords.ProductionEnv) {
		envs = append(envs, stackrecords.ProductionEnv)
	}
	if r.scope != EveryPreview && !slices.Contains(envs, r.scope) {
		envs = append(envs, r.scope)
	}
	slices.Sort(envs)
	return envs
}

func (r *projectRemoval) forgetProjectIfEmpty(ctx context.Context, progress progress.Progress) error {
	remaining, err := stackrecords.List(ctx, r.provider.KeyValues(), r.tier, r.slug)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return nil
	}
	progress.Say(fmt.Sprintf("Forgetting %s in %s: nothing of it is left", r.slug, r.tier))
	if err := keyvalue.Forget(ctx, r.provider.KeyValues(), stackrecords.EdgeStackKey(r.tier, r.slug)); err != nil {
		return err
	}
	return keyvalue.Forget(ctx, r.provider.KeyValues(), stackrecords.ProjectKey(r.tier, r.slug))
}
