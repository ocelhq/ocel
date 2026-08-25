package providerkit

import (
	"context"
	"errors"
	"fmt"
	"slices"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type projectRemoval struct {
	provider Provider
	front    edge.Edge
	stack    edge.EdgeStack
	store    stackStore
	state    EdgeStackState
	settle   settler

	slug    string
	class   Class
	scope   string
	infra   []naming.StackName
	apps    []naming.StackName
	pointer []string
}

func (h *handlers) openRemoval(ctx context.Context, req *contractv1.ProjectRequest) (*projectRemoval, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, err
	}
	if req.GetSlug() == "" {
		return nil, Refuse(CodeInvalid, "this call names no project, and a removal plan is drawn for one")
	}
	class, err := classOf(req.GetEnvironment().GetTier())
	if err != nil {
		return nil, err
	}
	scope, err := envScope(req.GetEnvironment())
	if err != nil {
		return nil, err
	}
	front, err := h.edgeFor(provider, req.GetEdge())
	if err != nil {
		return nil, err
	}
	writer, err := h.dnsFor(provider, req.GetEdge())
	if err != nil {
		return nil, err
	}
	removal := &projectRemoval{
		provider: provider,
		front:    front,
		settle:   newSettler(front, writer, req.GetEdge().GetDns().GetZone(), servedResolver{kind: front.Kind()}),
		store:    stackStore{records: provider.Records(), name: EdgeStackRecord(class, req.GetSlug())},
		slug:     req.GetSlug(),
		class:    class,
		scope:    scope,
	}
	if removal.state, err = removal.store.read(ctx); err != nil {
		return nil, err
	}
	if !removal.state.Edge.Empty() {
		if removal.stack, err = front.Open(removal.state.Edge); err != nil {
			return nil, err
		}
	}
	entries, err := ReadStacks(ctx, provider.Records(), class, req.GetSlug())
	if err != nil {
		return nil, err
	}
	removal.infra, removal.apps, removal.pointer = classifyStacks(entries, class)
	return removal, nil
}

func (h *handlers) PlanRemoveProject(ctx context.Context, req *contractv1.ProjectRequest) (*contractv1.ChangePlan, error) {
	removal, err := h.openRemoval(ctx, req)
	if err != nil {
		return nil, RefusalError(err)
	}
	return removal.plan(), nil
}

func (r *projectRemoval) plan() *contractv1.ChangePlan {
	plan := &contractv1.ChangePlan{EdgeKind: string(r.front.Kind()), Subject: r.slug}
	vendor := string(r.provider.Vendor())
	for _, stack := range r.apps {
		plan.Groups = append(plan.Groups, &contractv1.ChangeGroup{
			Kind:    StackGroupKind,
			Name:    vendor + "/" + stack.String(),
			Feature: stack.App,
			Action:  contractv1.Change_ACTION_DELETE,
			Reason:  "everything this release of " + stack.App + " stood up",
		})
	}
	for _, stack := range r.infra {
		plan.Groups = append(plan.Groups, &contractv1.ChangeGroup{
			Kind:    StackGroupKind,
			Name:    vendor + "/" + stack.String(),
			Feature: stack.Env,
			Action:  contractv1.Change_ACTION_DELETE,
			Reason:  "the resources every app in " + stack.Env + " links to",
			Slow:    true,
		})
	}
	for _, group := range r.front.ProjectRemovals(edge.ProjectScope{
		Slug:      r.slug,
		Class:     r.class,
		Hostnames: r.state.Hostnames(),
		Front:     r.state.Edge.Front,
	}) {
		plan.Groups = append(plan.Groups, edgeGroupProto(group))
	}
	plan.Groups = append(plan.Groups, r.recordGroups()...)
	plan.Groups = append(plan.Groups,
		&contractv1.ChangeGroup{
			Kind:   "variable values",
			Name:   r.slug,
			Action: contractv1.Change_ACTION_DELETE,
			Reason: "the values this project's apps read, and the links published beside them",
		},
		&contractv1.ChangeGroup{
			Kind:   "stored objects",
			Name:   r.slug,
			Action: contractv1.Change_ACTION_DELETE,
			Reason: "the artifacts, assets and cache entries every release of this project wrote",
		})
	return plan
}

func (r *projectRemoval) recordGroups() []*contractv1.ChangeGroup {
	var groups []*contractv1.ChangeGroup
	for _, rec := range r.state.WrittenRecords() {
		groups = append(groups, &contractv1.ChangeGroup{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: contractv1.Change_ACTION_DELETE,
			Reason: "ocel wrote it; it is removed only while its live value is still the one ocel wrote",
		})
	}
	for _, rec := range r.state.OwedRecords() {
		groups = append(groups, &contractv1.ChangeGroup{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: contractv1.Change_ACTION_KEEP,
			Reason: "you created it yourself; ocel never wrote it, so it is yours to remove",
		})
	}
	for _, cert := range r.state.Certificates() {
		groups = append(groups, certificateGroup(cert))
	}
	return groups
}

func certificateGroup(cert Certificate) *contractv1.ChangeGroup {
	if !cert.Requested {
		return &contractv1.ChangeGroup{
			Kind:   "certificate",
			Name:   cert.ID,
			Action: contractv1.Change_ACTION_KEEP,
			Reason: "you pinned it; ocel never requested it, so it is not ocel's to delete",
		}
	}
	return &contractv1.ChangeGroup{
		Kind:   "certificate",
		Name:   cert.ID,
		Action: contractv1.Change_ACTION_DELETE,
		Reason: "ocel requested it for a hostname this project serves, and nothing is left to serve",
	}
}

func (h *handlers) RemoveProject(ctx context.Context, req *contractv1.ProjectRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamed(ctx, stream, progressv1.Phase_PHASE_DELETING, func(_ *eventSender, report Reporter) error {
		removal, err := h.openRemoval(ctx, req)
		if err != nil {
			return err
		}
		return removal.run(ctx, report)
	})
}

func (r *projectRemoval) run(ctx context.Context, report Reporter) error {
	var errs []error

	if err := r.unbind(ctx, report); err != nil {
		errs = append(errs, err)
	}
	for _, stack := range append(slices.Clone(r.apps), r.infra...) {
		if err := r.destroy(ctx, stack, report); err != nil {
			errs = append(errs, err)
		}
	}
	written, held := r.state.PointerRecords(), r.state.Certificates()
	if err := r.tearDownEdge(ctx, report); err != nil {
		errs = append(errs, err)
	} else {
		if err := r.releaseRecords(ctx, written, report); err != nil {
			errs = append(errs, err)
		}
		if err := r.discardCertificates(ctx, held, report); err != nil {
			errs = append(errs, err)
		}
	}
	if err := r.purgeValues(ctx, report); err != nil {
		errs = append(errs, err)
	}
	if err := r.purgeObjects(ctx, report); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		report.Say("Leaving the project on record: the rerun reads its progress from what is still here")
		return err
	}
	return r.forget(ctx, report)
}

func (r *projectRemoval) unbind(ctx context.Context, report Reporter) error {
	if r.stack == nil || r.stack.State().Empty() {
		return nil
	}
	var errs []error
	for _, hostname := range r.stack.State().Bound {
		report.Say("Unbinding " + hostname + " from the edge")
		if err := r.stack.UnbindDomain(ctx, hostname); err != nil {
			errs = append(errs, fmt.Errorf("unbind %q before the origin it fronts is destroyed: %w", hostname, err))
		}
	}
	for _, pointer := range r.pointers() {
		report.Say(fmt.Sprintf("Removing pointer %q from the store", pointer))
		if _, err := r.stack.RemovePointer(ctx, pointer); err != nil {
			errs = append(errs, fmt.Errorf("remove pointer %q before the origin it points at is destroyed: %w", pointer, err))
		}
	}
	return errors.Join(errs...)
}

func (r *projectRemoval) pointers() []string {
	if r.class == ClassProduction {
		return []string{edge.DefaultPointer}
	}
	return r.pointer
}

func (r *projectRemoval) destroy(ctx context.Context, stack naming.StackName, report Reporter) error {
	report.Say("Destroying " + stack.String())
	ref := StackRef{Project: r.slug, Class: r.class, Name: stack}
	if err := r.provider.Releases().Destroy(ctx, ref, report); err != nil {
		return fmt.Errorf("destroy %s: %w", stack, err)
	}
	return ForgetStack(ctx, r.provider.Records(), r.class, r.slug, stack)
}

func (r *projectRemoval) tearDownEdge(ctx context.Context, report Reporter) error {
	if r.stack == nil || r.stack.State().Empty() {
		return nil
	}
	report.Say("Destroying what the edge stack owns")
	if err := r.stack.Destroy(ctx); err != nil {
		return fmt.Errorf("destroy the edge stack: %w", err)
	}
	r.state = EdgeStackState{}
	return r.store.write(ctx, r.state)
}

func (r *projectRemoval) releaseRecords(ctx context.Context, written []edge.Record, report Reporter) error {
	if err := r.settle.release(ctx, written, report.Say); err != nil {
		return fmt.Errorf("remove the DNS records pointing at what this project served: %w", err)
	}
	return nil
}

func (r *projectRemoval) discardCertificates(ctx context.Context, held []Certificate, report Reporter) error {
	var errs []error
	for _, cert := range held {
		if err := retireCertificate(ctx, r.provider, r.settle, cert, Certificate{}, report); err != nil {
			errs = append(errs, fmt.Errorf("discard the certificate ocel requested for this project: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (r *projectRemoval) purgeValues(ctx context.Context, report Reporter) error {
	report.Say("Removing the project's stored variable values")
	store := values.Store{Records: r.provider.Records(), Sealer: r.provider.Sealer()}
	if _, err := store.Purge(ctx, values.Scope{Project: r.slug, Class: r.class}); err != nil {
		return fmt.Errorf("remove %s's stored variable values: %w", r.slug, err)
	}
	return nil
}

func (r *projectRemoval) purgeObjects(ctx context.Context, report Reporter) error {
	var errs []error
	for _, env := range r.environments() {
		prefix := naming.Coordinate{Project: naming.Sanitize(r.slug), Env: env}.StoragePrefix()
		if err := r.provider.Artifacts().RemovePrefix(ctx, r.class, prefix, report); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", prefix, err))
		}
	}
	return errors.Join(errs...)
}

func (r *projectRemoval) environments() []string {
	envs := slices.Clone(r.pointer)
	if r.class == ClassProduction && !slices.Contains(envs, ProductionEnv) {
		envs = append(envs, ProductionEnv)
	}
	if r.scope != EveryPreview && !slices.Contains(envs, r.scope) {
		envs = append(envs, r.scope)
	}
	slices.Sort(envs)
	return envs
}

func (r *projectRemoval) forget(ctx context.Context, report Reporter) error {
	remaining, err := ReadStacks(ctx, r.provider.Records(), r.class, r.slug)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return nil
	}
	report.Say("Forgetting the project")
	if err := Forget(ctx, r.provider.Records(), EdgeStackRecord(r.class, r.slug)); err != nil {
		return err
	}
	return Forget(ctx, r.provider.Records(), ProjectRecord(r.class, r.slug))
}

func reclaim(
	ctx context.Context,
	provider Provider,
	slug string,
	class Class,
	targets []ReclaimTarget,
	report Reporter,
) error {
	var errs []error
	for _, target := range targets {
		report.Say("Reclaiming " + target.App + " " + target.Build.String())
		ref := StackRef{Project: slug, Class: class, Name: target.Stack}
		if err := provider.Releases().Destroy(ctx, ref, report); err != nil {
			errs = append(errs, fmt.Errorf("destroy %s: %w", target.Stack, err))
			continue
		}
		if err := ForgetStack(ctx, provider.Records(), class, slug, target.Stack); err != nil {
			errs = append(errs, err)
		}
		for _, prefix := range target.Prefixes {
			if err := provider.Artifacts().RemovePrefix(ctx, class, prefix, report); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", prefix, err))
			}
		}
	}
	return errors.Join(errs...)
}
