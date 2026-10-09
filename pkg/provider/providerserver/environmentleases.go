package providerserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	environmentLeaseTTL            = 5 * time.Minute
	environmentLeaseRenewal        = 2 * time.Minute
	environmentLeaseRetry          = 15 * time.Second
	environmentLeaseMargin         = time.Minute
	environmentLeaseReleaseTimeout = 30 * time.Second
	environmentLeaseWatch          = 30 * time.Second
	environmentLeaseConcurrency    = 8
)

type environmentScope struct {
	tier      environment.Tier
	slug, env string
}

func newEnvironmentScope(spec provider.DeploySpec) environmentScope {
	return environmentScope{tier: spec.Tier, slug: spec.Slug, env: spec.Env}
}

func (s environmentScope) take(ctx context.Context, store keyvalue.Store, token string, holder stackrecords.LeaseHolder, terms stackrecords.LeaseTerms) (bool, error) {
	return stackrecords.TakeEnvironmentLease(ctx, store, s.tier, s.slug, s.env, token, holder, terms)
}

func (s environmentScope) renew(ctx context.Context, store keyvalue.Store, token string, holder stackrecords.LeaseHolder, terms stackrecords.LeaseTerms) error {
	return stackrecords.RenewEnvironmentLease(ctx, store, s.tier, s.slug, s.env, token, holder, terms)
}

func (s environmentScope) forget(ctx context.Context, store keyvalue.Store, token string) error {
	return stackrecords.ForgetEnvironmentLease(ctx, store, s.tier, s.slug, s.env, token)
}

func (s environmentScope) describe() string { return s.env }

func (s environmentScope) lease() provider.Lease {
	return provider.Lease{Tier: s.tier, Project: s.slug, Env: s.env}
}

type projectScope struct {
	tier environment.Tier
	slug string
}

func (s projectScope) take(ctx context.Context, store keyvalue.Store, token string, holder stackrecords.LeaseHolder, terms stackrecords.LeaseTerms) (bool, error) {
	return stackrecords.TakeProjectLease(ctx, store, s.tier, s.slug, token, holder, terms)
}

func (s projectScope) renew(ctx context.Context, store keyvalue.Store, token string, holder stackrecords.LeaseHolder, terms stackrecords.LeaseTerms) error {
	return stackrecords.RenewProjectLease(ctx, store, s.tier, s.slug, token, holder, terms)
}

func (s projectScope) forget(ctx context.Context, store keyvalue.Store, token string) error {
	return stackrecords.ForgetProjectLease(ctx, store, s.tier, s.slug, token)
}

func (s projectScope) describe() string { return s.slug + " in " + string(s.tier) }

func (s projectScope) lease() provider.Lease { return provider.Lease{Tier: s.tier, Project: s.slug} }

type leaseSubject interface {
	take(ctx context.Context, store keyvalue.Store, token string, holder stackrecords.LeaseHolder, terms stackrecords.LeaseTerms) (bool, error)
	renew(ctx context.Context, store keyvalue.Store, token string, holder stackrecords.LeaseHolder, terms stackrecords.LeaseTerms) error
	forget(ctx context.Context, store keyvalue.Store, token string) error
	describe() string
	lease() provider.Lease
}

type environmentLeases struct {
	ttl, renewal, retry, margin time.Duration

	now   func() time.Time
	after func(time.Duration) <-chan time.Time
}

func newEnvironmentLeases() *environmentLeases {
	return &environmentLeases{
		ttl:     environmentLeaseTTL,
		renewal: environmentLeaseRenewal,
		retry:   environmentLeaseRetry,
		margin:  environmentLeaseMargin,
		now:     time.Now,
		after:   time.After,
	}
}

func (l *environmentLeases) terms() stackrecords.LeaseTerms {
	return stackrecords.LeaseTerms{TTL: l.ttl, Watch: environmentLeaseWatch, Now: l.now, Wait: l.wait}
}

func (l *environmentLeases) wait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.after(d):
		return nil
	}
}

type environmentHold struct {
	leases *environmentLeases
	store  keyvalue.Store
	scope  leaseSubject
	token  string
	holder stackrecords.LeaseHolder
	held   bool

	leased context.Context
	cancel context.CancelCauseFunc
	stop   chan struct{}
	done   chan struct{}
	ended  sync.Once

	loss     sync.Once
	lost     chan struct{}
	lostWith error
}

func (l *environmentLeases) take(ctx context.Context, store keyvalue.Store, scope leaseSubject, token string, holder stackrecords.LeaseHolder) (*environmentHold, error) {
	held, err := scope.take(ctx, store, token, holder, l.terms())
	if err != nil {
		return nil, err
	}
	return l.startHold(ctx, store, scope, token, holder, held), nil
}

func (l *environmentLeases) takeUnderNewToken(ctx context.Context, store keyvalue.Store, scope leaseSubject, holder stackrecords.LeaseHolder) (*environmentHold, error) {
	token, err := stackrecords.NewEnvironmentLeaseToken()
	if err != nil {
		return nil, err
	}
	return l.take(ctx, store, scope, token, holder)
}

func (l *environmentLeases) keep(ctx context.Context, store keyvalue.Store, scope leaseSubject, token string, holder stackrecords.LeaseHolder) (*environmentHold, error) {
	if err := scope.renew(ctx, store, token, holder, l.terms()); err != nil {
		return nil, err
	}
	return l.startHold(ctx, store, scope, token, holder, true), nil
}

func (l *environmentLeases) startHold(ctx context.Context, store keyvalue.Store, scope leaseSubject, token string, holder stackrecords.LeaseHolder, held bool) *environmentHold {
	leased, cancel := context.WithCancelCause(provider.WithLease(ctx, scope.lease()))
	h := &environmentHold{
		leases: l, store: store, scope: scope, token: token, holder: holder, held: held,
		leased: leased, cancel: cancel,
		stop: make(chan struct{}), done: make(chan struct{}), lost: make(chan struct{}),
	}
	go h.renew()
	return h
}

func (h *environmentHold) renew() {
	defer close(h.done)
	renewed, wait := h.leases.now(), h.leases.renewal
	for {
		select {
		case <-h.stop:
			return
		case <-h.leased.Done():
			return
		case <-h.leases.after(wait):
		}
		started := h.leases.now()
		ctx, cancel := context.WithTimeout(h.leased, h.leases.retry)
		err := h.scope.renew(ctx, h.store, h.token, h.holder, h.leases.terms())
		cancel()
		switch {
		case err == nil:
			renewed, wait = started, h.leases.renewal
		case isBusy(err):
			h.lose(err)
			return
		case h.leases.now().Sub(renewed) >= h.leases.ttl-h.leases.margin:
			h.lose(refusal.Refuse(refusal.CodeBusy,
				"this %s could not renew its lease on %s for %s, so another deploy may take the lease over, and this %s stopped: run it again (the last renewal failed with: %v)",
				h.holder, h.scope.describe(), h.leases.ttl-h.leases.margin, h.holder, err))
			return
		default:
			wait = h.leases.retry
		}
	}
}

func isBusy(err error) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == refusal.CodeBusy
}

func (h *environmentHold) lose(err error) {
	h.loss.Do(func() {
		h.lostWith = err
		close(h.lost)
		h.cancel(err)
	})
}

func (h *environmentHold) readLoss() error {
	select {
	case <-h.lost:
		return h.lostWith
	default:
		return nil
	}
}

func (h *environmentHold) context(ctx context.Context) context.Context {
	if h == nil {
		return ctx
	}
	return h.leased
}

func (h *environmentHold) explain(err error) error {
	if h == nil || err == nil {
		return err
	}
	if lost := h.readLoss(); lost != nil {
		return lost
	}
	return err
}

func (h *environmentHold) confirm(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if lost := h.readLoss(); lost != nil {
		return lost
	}
	err := h.scope.renew(ctx, h.store, h.token, h.holder, h.leases.terms())
	if isBusy(err) {
		h.lose(err)
	}
	return err
}

func (h *environmentHold) end() {
	if h == nil {
		return
	}
	h.ended.Do(func() { close(h.stop) })
	<-h.done
	h.cancel(nil)
}

func (h *environmentHold) release(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.end()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), environmentLeaseReleaseTimeout)
	defer cancel()
	return h.scope.forget(ctx, h.store, h.token)
}

func (l *environmentLeases) sayRunOutWatch(leased map[string]stackrecords.EnvironmentLease, progress progress.Log) {
	now, runOut := l.now(), 0
	for _, lease := range leased {
		switch {
		case lease.Token == "":
		case now.Before(time.Unix(lease.ExpiresAt, 0)):
			return
		default:
			runOut++
		}
	}
	if runOut == 0 {
		return
	}
	environments := "environments"
	if runOut == 1 {
		environments = "environment"
	}
	progress.Say(fmt.Sprintf("Waiting until %s to take over the leases interrupted runs left on %d %s, in case one still renews its lease",
		now.Add(l.ttl).UTC().Format("15:04 MST"), runOut, environments))
}

type environmentHolds struct {
	held   []*environmentHold
	leased context.Context
	cancel context.CancelCauseFunc
}

func (l *environmentLeases) takeEach(ctx context.Context, store keyvalue.Store, token string, scopes []leaseSubject, holder stackrecords.LeaseHolder) (environmentHolds, error) {
	leased, cancel := context.WithCancelCause(ctx)
	holds := environmentHolds{held: make([]*environmentHold, len(scopes)), leased: leased, cancel: cancel}
	bounded := newBoundedStore(store, environmentLeaseConcurrency)
	var group errgroup.Group
	for i, scope := range scopes {
		group.Go(func() error {
			hold, err := l.take(leased, bounded, scope, token, holder)
			if err != nil {
				cancel(err)
				return err
			}
			context.AfterFunc(hold.leased, func() { cancel(context.Cause(hold.leased)) })
			holds.held[i] = hold
			return nil
		})
	}
	if group.Wait() != nil {
		err := context.Cause(leased)
		_ = holds.release(ctx)
		return environmentHolds{}, err
	}
	for _, hold := range holds.held {
		holds.leased = provider.WithLease(holds.leased, hold.scope.lease())
	}
	return holds, nil
}

func (hs environmentHolds) context() context.Context {
	return hs.leased
}

func (hs environmentHolds) explain(err error) error {
	if err == nil {
		return nil
	}
	for _, hold := range hs.held {
		if lost := hold.readLoss(); lost != nil {
			return lost
		}
	}
	return err
}

func (hs environmentHolds) release(ctx context.Context) error {
	var errs []error
	for i := len(hs.held) - 1; i >= 0; i-- {
		errs = append(errs, hs.held[i].release(ctx))
	}
	hs.cancel(nil)
	return errors.Join(errs...)
}

func (h *handlers) takeEnvironment(ctx context.Context, spec provider.DeploySpec, token string) (*environmentHold, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	return h.leases.take(ctx, p.KeyValues(), newEnvironmentScope(spec), token, stackrecords.LeaseDeploy)
}

func (h *handlers) claimEnvironment(ctx context.Context, spec provider.DeploySpec, provisionedUnder string) (*environmentHold, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	if provisionedUnder == "" {
		return h.leases.takeUnderNewToken(ctx, p.KeyValues(), newEnvironmentScope(spec), stackrecords.LeaseDeploy)
	}
	return h.leases.keep(ctx, p.KeyValues(), newEnvironmentScope(spec), provisionedUnder, stackrecords.LeaseDeploy)
}

func (h *handlers) readLeaseScope(slug string, env *environmentv1.Environment) (provider.Provider, environmentScope, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, environmentScope{}, err
	}
	tier, err := decodeTier(env.GetTier())
	if err != nil {
		return nil, environmentScope{}, err
	}
	name, err := envName(env)
	if err != nil {
		return nil, environmentScope{}, provider.RefusalError(err)
	}
	return p, environmentScope{tier: tier, slug: slug, env: name}, nil
}

func (h *handlers) RenewDeployLease(ctx context.Context, req *contractv1.RenewDeployLeaseRequest) (*contractv1.RenewDeployLeaseResponse, error) {
	p, scope, err := h.readLeaseScope(req.GetSlug(), req.GetEnvironment())
	if err != nil {
		return nil, err
	}
	if err := stackrecords.RenewEnvironmentLease(ctx, p.KeyValues(), scope.tier, scope.slug, scope.env, req.GetLeaseToken(), stackrecords.LeaseDeploy, h.leases.terms()); err != nil {
		return nil, provider.RefusalError(err)
	}
	return &contractv1.RenewDeployLeaseResponse{}, nil
}

func (h *handlers) AbandonDeploy(ctx context.Context, req *contractv1.AbandonDeployRequest) (*contractv1.AbandonDeployResponse, error) {
	p, scope, err := h.readLeaseScope(req.GetSlug(), req.GetEnvironment())
	if err != nil {
		return nil, err
	}
	if err := stackrecords.ForgetEnvironmentLease(ctx, p.KeyValues(), scope.tier, scope.slug, scope.env, req.GetLeaseToken()); err != nil {
		return nil, provider.RefusalError(err)
	}
	return &contractv1.AbandonDeployResponse{}, nil
}

type boundedStore struct {
	keyvalue.Store
	slots chan struct{}
}

func newBoundedStore(store keyvalue.Store, calls int) *boundedStore {
	return &boundedStore{Store: store, slots: make(chan struct{}, calls)}
}

func (s *boundedStore) acquire(ctx context.Context) error {
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *boundedStore) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	if err := s.acquire(ctx); err != nil {
		return keyvalue.Entry{}, err
	}
	defer func() { <-s.slots }()
	return s.Store.Read(ctx, key)
}

func (s *boundedStore) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer func() { <-s.slots }()
	return s.Store.Write(ctx, entry)
}

func (s *boundedStore) WritePair(ctx context.Context, first, second keyvalue.Entry) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-s.slots }()
	return s.Store.WritePair(ctx, first, second)
}

func (s *boundedStore) Remove(ctx context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-s.slots }()
	return s.Store.Remove(ctx, key, expected)
}

func (s *boundedStore) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { <-s.slots }()
	return s.Store.List(ctx, in, under...)
}
