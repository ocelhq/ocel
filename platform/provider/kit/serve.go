package kit

import (
	"context"
	"errors"
	"fmt"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	provider "github.com/ocelhq/ocel/platform/provider/contract"
)

type Server struct {
	p     provider.Provider
	facts provider.Facts
}

func New(p provider.Provider) *Server { return &Server{p: p, facts: p.Facts()} }

func Serve(ctx context.Context, p provider.Provider) error {
	s := New(p)
	_ = s
	<-ctx.Done()
	return ctx.Err()
}

type DeployResult struct {
	Plan        provider.Plan
	Links       []provider.Link
	PromotionID string
	AppURLs     map[string]string
	Edge        edge.StackState
}

func (s *Server) Preflight(ctx context.Context, spec provider.Spec) (provider.Plan, error) {
	if refusals := s.admit(spec); len(refusals) > 0 {
		return provider.Plan{Refusals: refusals, Stages: DeployStages()}, nil
	}
	prior, _, err := s.p.Records().Read(ctx, spec.Slug, spec.Class)
	if err != nil {
		return provider.Plan{}, err
	}
	plan, err := s.p.Deployer().Plan(ctx, spec, prior.Deploy)
	if err != nil {
		return provider.Plan{}, err
	}
	plan.Stages = append(DeployStages(), plan.Stages...)
	plan.Ready = len(plan.Refusals) == 0
	return plan, nil
}

func (s *Server) Deploy(ctx context.Context, spec provider.Spec, progress provider.Progress) (DeployResult, error) {
	var out DeployResult
	plan, err := s.Preflight(ctx, spec)
	if err != nil {
		return out, err
	}
	out.Plan = plan
	for _, st := range plan.Stages {
		progress.Stage(st.ID, st.Title, st.Parent)
	}
	if !plan.Ready {
		return out, refused(plan.Refusals)
	}

	prior, _, err := s.p.Records().Read(ctx, spec.Slug, spec.Class)
	if err != nil {
		return out, err
	}

	progress.Start(StageUpload)
	up, err := s.p.Deployer().Upload(ctx, spec, plan, progress)
	if err != nil {
		return out, fail(progress, StageUpload, err)
	}
	progress.Done(StageUpload)

	progress.Start(StageEdge)
	front, err := s.p.Edges().For(ctx, s.edgeKind(spec))
	if err != nil {
		return out, fail(progress, StageEdge, err)
	}
	stack, err := front.Reconcile(ctx, edge.StackSpec{Class: spec.Class, Slug: spec.Slug, Warn: func(w string) { progress.Say(StageEdge, w) }}, prior.Edge)
	if err != nil {
		return out, fail(progress, StageEdge, err)
	}
	progress.Done(StageEdge)

	progress.Start(StageProvision)
	dep, err := s.p.Deployer().Apply(ctx, spec, plan, up, prior.Deploy, progress)
	if err != nil {
		return out, fail(progress, StageProvision, err)
	}
	progress.Done(StageProvision)

	progress.Start(StageLinks)
	out.Links = dep.Links()
	for _, l := range out.Links {
		if err := s.p.Vars().SetLink(ctx, provider.VarScope{Slug: spec.Slug, Class: spec.Class, Environment: spec.Environment}, l.Resource, l); err != nil {
			return out, fail(progress, StageLinks, err)
		}
	}
	progress.Done(StageLinks)

	progress.Start(StagePromote)
	promotion := edge.Promotion{PromotionID: fmt.Sprintf("p-%d", time.Now().UnixNano()), Ts: time.Now().Unix(), Builds: map[string]string{}, Tag: spec.Tag}
	for _, rec := range dep.Records() {
		if err := stack.Ledger().PutStaged(ctx, rec); err != nil {
			return out, fail(progress, StagePromote, err)
		}
		promotion.Builds[rec.App] = rec.DeploymentID
	}
	if err := stack.Promote(ctx, promotion, edge.DefaultPointer); err != nil {
		return out, fail(progress, StagePromote, err)
	}
	progress.Done(StagePromote)

	out.PromotionID = promotion.PromotionID
	out.Edge = stack.State()
	next := provider.Record{Deploy: dep.State(), EdgeKind: s.edgeKind(spec), Edge: stack.State(), Hosts: prior.Hosts}
	return out, s.p.Records().Write(ctx, spec.Slug, spec.Class, next)
}

func (s *Server) Bootstrap(ctx context.Context, class edge.Class, features []string, progress provider.Progress) (provider.Bootstrapped, error) {
	for _, f := range features {
		if !contains(s.facts.Features, f) {
			return provider.Bootstrapped{}, fmt.Errorf("the %s provider has no %q feature; it knows %v", s.facts.Kind, f, s.facts.Features)
		}
	}
	progress.Stage(StageBootstrap, "Bootstrapping", "")
	progress.Start(StageBootstrap)
	out, err := s.p.Substrate().Bootstrap(ctx, class, features, progress)
	if err != nil {
		return out, fail(progress, StageBootstrap, err)
	}
	front, err := s.p.Edges().For(ctx, s.facts.DefaultEdge)
	if err != nil {
		return out, fail(progress, StageBootstrap, err)
	}
	if _, err := front.Bootstrap(ctx, class); err != nil {
		return out, fail(progress, StageBootstrap, err)
	}
	progress.Done(StageBootstrap)
	return out, nil
}

func (s *Server) DescribeBootstrap(ctx context.Context, class edge.Class) (provider.Bootstrapped, bool, error) {
	return s.p.Substrate().Describe(ctx, class)
}

func (s *Server) PlanTeardown(ctx context.Context, class edge.Class) ([]edge.Surface, error) {
	slugs, err := s.p.Records().Slugs(ctx, class)
	if err != nil {
		return nil, err
	}
	if len(slugs) > 0 {
		return nil, fmt.Errorf("%d projects still deployed: %v", len(slugs), slugs)
	}
	return s.p.Substrate().PlanTeardown(ctx, class)
}

func (s *Server) Teardown(ctx context.Context, class edge.Class, progress provider.Progress) error {
	if _, err := s.PlanTeardown(ctx, class); err != nil {
		return err
	}
	progress.Stage(StageTeardown, "Tearing down", "")
	progress.Start(StageTeardown)
	front, err := s.p.Edges().For(ctx, s.facts.DefaultEdge)
	if err != nil {
		return fail(progress, StageTeardown, err)
	}
	if err := front.Teardown(ctx, class); err != nil {
		return fail(progress, StageTeardown, err)
	}
	if err := s.p.Substrate().Teardown(ctx, class, progress); err != nil {
		return fail(progress, StageTeardown, err)
	}
	progress.Done(StageTeardown)
	return nil
}

func (s *Server) DestroyProject(ctx context.Context, slug string, class edge.Class, progress provider.Progress) error {
	rec, ok, err := s.p.Records().Read(ctx, slug, class)
	if err != nil || !ok {
		return err
	}
	progress.Stage(StageDestroy, "Destroying "+slug, "")
	progress.Start(StageDestroy)
	front, err := s.p.Edges().For(ctx, rec.EdgeKind)
	if err == nil {
		if stack, err := front.Open(rec.Edge); err == nil {
			if err := stack.Destroy(ctx); err != nil {
				return fail(progress, StageDestroy, err)
			}
		}
	}
	dep, err := s.p.Deployer().Open(rec.Deploy)
	if err != nil {
		return fail(progress, StageDestroy, err)
	}
	if err := dep.Destroy(ctx, progress); err != nil {
		return fail(progress, StageDestroy, err)
	}
	progress.Done(StageDestroy)
	return s.p.Records().Delete(ctx, slug, class)
}

func (s *Server) ListPromotions(ctx context.Context, slug string, class edge.Class) ([]edge.HistoryEntry, error) {
	stack, err := s.openEdge(ctx, slug, class)
	if err != nil {
		return nil, err
	}
	return stack.Ledger().History(ctx, edge.DefaultPointer)
}

func (s *Server) Rollback(ctx context.Context, slug string, class edge.Class, promotionID string) error {
	stack, err := s.openEdge(ctx, slug, class)
	if err != nil {
		return err
	}
	history, err := stack.Ledger().History(ctx, edge.DefaultPointer)
	if err != nil {
		return err
	}
	for _, h := range history {
		if h.PromotionID == promotionID {
			return stack.Promote(ctx, h.Promotion, edge.DefaultPointer)
		}
	}
	return fmt.Errorf("no promotion %q for %s", promotionID, slug)
}

func (s *Server) Prune(ctx context.Context, slug string, class edge.Class, keep int) (edge.PruneResult, error) {
	stack, err := s.openEdge(ctx, slug, class)
	if err != nil {
		return edge.PruneResult{}, err
	}
	return stack.Ledger().Prune(ctx, keep, edge.DefaultPointer)
}

func (s *Server) SetLink(ctx context.Context, scope provider.VarScope, resource string, link provider.Link) error {
	return s.p.Vars().SetLink(ctx, scope, resource, link)
}

func (s *Server) RemoveLink(ctx context.Context, scope provider.VarScope, resource string) error {
	return s.p.Vars().RemoveLink(ctx, scope, resource)
}

func (s *Server) ListLinks(ctx context.Context, scope provider.VarScope) ([]provider.Link, error) {
	return s.p.Vars().Links(ctx, scope)
}

func (s *Server) AddDomain(ctx context.Context, slug string, hostname string, progress provider.Progress) error {
	rec, ok, err := s.p.Records().Read(ctx, slug, edge.ClassProduction)
	if err != nil || !ok {
		return errors.Join(err, fmt.Errorf("%s is not deployed", slug))
	}
	stack, err := s.openEdge(ctx, slug, edge.ClassProduction)
	if err != nil {
		return err
	}
	certificate := ""
	if c, ok := s.p.(provider.Certifying); ok {
		progress.Stage(StageCertificate, "Issuing certificate", "")
		progress.Start(StageCertificate)
		cert, err := c.Certificates().Issue(ctx, []string{hostname})
		if err != nil {
			return fail(progress, StageCertificate, err)
		}
		certificate = cert.ID
		progress.Done(StageCertificate)
	}
	progress.Stage(StageBind, "Binding "+hostname, "")
	progress.Start(StageBind)
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: hostname, Certificate: certificate}); err != nil {
		return fail(progress, StageBind, err)
	}
	progress.Done(StageBind)
	rec.Edge = stack.State()
	return s.p.Records().Write(ctx, slug, edge.ClassProduction, rec)
}

func (s *Server) openEdge(ctx context.Context, slug string, class edge.Class) (edge.EdgeStack, error) {
	rec, ok, err := s.p.Records().Read(ctx, slug, class)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%s is not deployed", slug)
	}
	front, err := s.p.Edges().For(ctx, rec.EdgeKind)
	if err != nil {
		return nil, err
	}
	return front.Open(rec.Edge)
}

func (s *Server) edgeKind(spec provider.Spec) edge.Kind {
	if spec.Edge != "" {
		return spec.Edge
	}
	return s.facts.DefaultEdge
}

func (s *Server) admit(spec provider.Spec) []provider.Refusal {
	var out []provider.Refusal
	for _, r := range spec.Resources {
		if !containsKind(s.facts.Serves, r.Kind) {
			out = append(out, provider.Refusal{Subject: fmt.Sprintf("%s %q", r.Kind, r.Name),
				Reason: fmt.Sprintf("the %s provider does not provision %s", s.facts.Kind, r.Kind),
				Fix:    "drop the resource, or deploy to a provider that serves it"})
		}
	}
	if !containsEdge(s.p.Edges().Kinds(), s.edgeKind(spec)) {
		out = append(out, provider.Refusal{Subject: "edge", Reason: fmt.Sprintf("the %s provider cannot front deployments with the %q edge", s.facts.Kind, spec.Edge), Fix: fmt.Sprintf("use one of %v", s.p.Edges().Kinds())})
	}
	return out
}

func refused(rs []provider.Refusal) error {
	errs := make([]error, len(rs))
	for i, r := range rs {
		errs[i] = fmt.Errorf("%s: %s (%s)", r.Subject, r.Reason, r.Fix)
	}
	return errors.Join(errs...)
}

func fail(p provider.Progress, id provider.StageID, err error) error {
	p.Fail(id, err)
	return err
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func containsKind(xs []provider.ResourceKind, x provider.ResourceKind) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func containsEdge(xs []edge.Kind, x edge.Kind) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
