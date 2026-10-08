package providerserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
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
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
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

	images provider.ImageStore
}

func (h *handlers) openRemoval(ctx context.Context, req *contractv1.ProjectRequest) (*projectRemoval, error) {
	vendor, err := h.session.use()
	if err != nil {
		return nil, err
	}
	if req.GetSlug() == "" {
		return nil, errUnnamedProject
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
	removal.restoreRouterStates(state)
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
	scope := edge.ProjectScope{
		Slug:      r.slug,
		Tier:      r.tier,
		Hostnames: r.state.Hostnames(),
		Address:   r.state.Edge.Address,
	}
	groups := r.front.ProjectRemovals(scope)
	for _, kind := range r.listRouterKinds() {
		origin, err := r.findRouterOrigin(kind)
		if err != nil {
			return nil, err
		}
		if origin == nil {
			continue
		}
		routed := scope
		routed.Hostnames = slices.DeleteFunc(slices.Clone(scope.Hostnames), func(hostname string) bool { return r.state.Host(hostname).Router != kind })
		groups = append(groups, origin.PlanProjectRemoval(routed)...)
	}
	for _, group := range groups {
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
	root := RootSpan(naming.SpanEnvironment, req.GetSlug(), removalTitle(req.GetEnvironment()), progressv1.Phase_PHASE_DESTROY)
	return streamed(ctx, stream, root, func(_ *eventStream, progress progress.Log) error {
		if req.GetSlug() == "" {
			return errUnnamedProject
		}
		vendor, err := h.session.use()
		if err != nil {
			return err
		}
		tier, err := decodeTier(req.GetEnvironment().GetTier())
		if err != nil {
			return err
		}
		token, err := stackrecords.NewEnvironmentLeaseToken()
		if err != nil {
			return err
		}
		project, err := h.leases.takeEach(ctx, vendor.KeyValues(), token, []leaseSubject{projectScope{tier: tier, slug: req.GetSlug()}}, stackrecords.LeaseRemoval)
		if err != nil {
			return err
		}
		defer func() { _ = project.release(ctx) }()
		removal, err := h.openRemoval(project.context(ctx), req)
		if err != nil {
			return project.explain(err)
		}
		if err := removal.refuseIfPlanGrew(req.GetConsented()); err != nil {
			return project.explain(err)
		}
		removed, err := removal.listRemovedEnvironments(project.context(ctx))
		if err != nil {
			return project.explain(err)
		}
		environments, err := h.leases.takeEach(project.context(ctx), vendor.KeyValues(), token, removed, stackrecords.LeaseRemoval)
		if err != nil {
			return project.explain(err)
		}
		defer func() { _ = environments.release(ctx) }()
		holds := append(slices.Clone(project), environments...)
		removal.images = removalImages(ctx, removal.provider, req.GetProjectRegistry(), progress)
		return holds.explain(removal.run(environments.context(project.context(ctx)), progress))
	})
}

func (r *projectRemoval) listRemovedEnvironments(ctx context.Context) ([]leaseSubject, error) {
	envs := r.environments()
	leased, err := stackrecords.ListEnvironmentLeases(ctx, r.provider.KeyValues(), r.tier, r.slug)
	if err != nil {
		return nil, err
	}
	envs = append(envs, leased...)
	metas, err := stackrecords.EnvironmentMetas(ctx, r.provider.KeyValues(), r.tier, r.slug)
	if err != nil {
		return nil, err
	}
	envs = append(envs, slices.Collect(maps.Keys(metas))...)
	slices.Sort(envs)
	scopes := make([]leaseSubject, 0, len(envs))
	for _, env := range slices.Compact(envs) {
		scopes = append(scopes, environmentScope{tier: r.tier, slug: r.slug, env: env})
	}
	return scopes, nil
}

func removalTitle(env *environmentv1.Environment) progress.Title {
	switch {
	case env.GetTier() != environmentv1.Tier_TIER_PREVIEW:
		return progress.Destroying.Title("the production footprint")
	case env.GetIdentity() == EveryPreview:
		return progress.Destroying.Title("the footprint of every preview")
	default:
		return progress.Destroying.Title("the footprint of preview " + env.GetIdentity())
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

func (r *projectRemoval) run(ctx context.Context, progress progress.Log) error {
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
	originCertificates := r.originCertificates()
	if err := r.tearDownEdge(ctx, progress); err != nil {
		errs = append(errs, err)
	} else {
		for _, id := range originCertificates {
			revokeOriginCertificate(ctx, r.front, id, progress)
		}
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

func (r *projectRemoval) originCertificates() []string {
	var ids []string
	for _, hostname := range r.state.Hostnames() {
		if id := r.state.Host(hostname).OriginCertificateID; id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (r *projectRemoval) unbind(ctx context.Context, runProgress progress.Log) error {
	stack := r.edgeStack()
	if stack == nil || stack.State().Empty() {
		return nil
	}
	var errs []error
	for _, hostname := range stack.State().Bound {
		runProgress.Say(fmt.Sprintf("Unbinding %s from %s", hostname, describeFront(r.front.Kind())))
		if err := progress.ReportWarning(runProgress, stack.UnbindDomain(ctx, hostname)); err != nil {
			errs = append(errs, fmt.Errorf("unbind %q before the origin it fronts is destroyed: %w", hostname, err))
		}
	}
	for _, pointer := range r.pointers() {
		removal := router.PointerRemoval{Pointer: pointer}
		if r.tier == environment.TierPreview {
			var err error
			if removal, err = readPreviewRemoval(ctx, r.provider.KeyValues(), r.slug, pointer); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		runProgress.Say(fmt.Sprintf("Removing the %s routing pointer", pointer))
		if _, err := r.removePointer(ctx, removal, runProgress); err != nil {
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

func (r *projectRemoval) destroyStack(ctx context.Context, stack naming.StackName, progress progress.Log) error {
	ref := provider.StackRef{Project: r.slug, Tier: r.tier, Name: stack}
	if err := r.provider.Stacks().Destroy(ctx, ref, r.images, progress); err != nil {
		return fmt.Errorf("destroy %s: %w", stack, err)
	}
	return stackrecords.Forget(ctx, r.provider.KeyValues(), r.tier, r.slug, stack)
}

func (r *projectRemoval) destroyUnrecordedHolders(ctx context.Context, progress progress.Log) error {
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

func (r *projectRemoval) tearDownEdge(ctx context.Context, progress progress.Log) error {
	if stack := r.edgeStack(); stack == nil || stack.State().Empty() {
		return nil
	}
	progress.Say(fmt.Sprintf("Destroying the stack that serves %s through %s", r.slug, describeFront(r.front.Kind())))
	if err := r.destroy(ctx); err != nil {
		return fmt.Errorf("destroy the edge stack: %w", err)
	}
	r.state = stackrecords.EdgeState{}
	return r.store.write(ctx, r.state)
}

func (r *projectRemoval) releaseRecords(ctx context.Context, written []edge.Record, progress progress.Log) error {
	if err := r.cutover.release(ctx, written, progress.Say); err != nil {
		return fmt.Errorf("remove the DNS records pointing at what this project served: %w", err)
	}
	return nil
}

func (r *projectRemoval) discardCertificates(ctx context.Context, certificates []provider.Certificate, progress progress.Log) error {
	var errs []error
	for _, cert := range certificates {
		if err := discardCertificateAndRecords(ctx, r.provider, r.cutover, cert, provider.Certificate{}, progress); err != nil {
			errs = append(errs, fmt.Errorf("discard the certificate ocel requested for this project: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (r *projectRemoval) purgeValues(ctx context.Context, progress progress.Log) error {
	progress.Say(fmt.Sprintf("Removing the stored variable values of %s in %s", r.slug, r.tier))
	store := variablestore.Store{KeyValues: r.provider.KeyValues(), Cipher: r.provider.Cipher()}
	if err := envsource.ForgetProject(ctx, store, r.tier, r.slug); err != nil {
		return fmt.Errorf("forget %s's env source: %w", r.slug, err)
	}
	if _, err := store.Purge(ctx, variablestore.Scope{Project: r.slug, Tier: r.tier}); err != nil {
		return fmt.Errorf("remove %s's stored variable values: %w", r.slug, err)
	}
	return nil
}

func (r *projectRemoval) purgeObjects(ctx context.Context, progress progress.Log) error {
	var errs []error
	for _, isrPrefix := range r.listISRPrefixes() {
		if err := r.provider.Artifacts().RemovePrefix(ctx, r.tier, isrPrefix, progress); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", isrPrefix, err))
		}
	}
	for _, env := range r.environments() {
		prefix := naming.Coordinate{Project: naming.Sanitize(r.slug), Env: env}.StoragePrefix()
		if err := r.provider.Artifacts().RemovePrefix(ctx, r.tier, prefix, progress); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", prefix, err))
		}
	}
	return errors.Join(errs...)
}

func (r *projectRemoval) listISRPrefixes() []string {
	prefixes := make([]string, 0, len(r.apps))
	for _, stack := range r.apps {
		coordinate := naming.Coordinate{Project: naming.Sanitize(r.slug), Env: stack.Env, App: stack.App, Release: stack.Release}
		prefixes = append(prefixes, coordinate.ISRPrefix())
	}
	slices.Sort(prefixes)
	return slices.Compact(prefixes)
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

func (r *projectRemoval) forgetProjectIfEmpty(ctx context.Context, progress progress.Log) error {
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
