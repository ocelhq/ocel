package providerserver

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
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
)

type environmentScope struct {
	tier      environment.Tier
	slug, env string
}

func newEnvironmentScope(spec provider.DeploySpec) environmentScope {
	return environmentScope{tier: spec.Tier, slug: spec.Slug, env: spec.Env}
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
	scope  environmentScope
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

func (l *environmentLeases) take(ctx context.Context, store keyvalue.Store, scope environmentScope, token string, holder stackrecords.LeaseHolder) (*environmentHold, error) {
	held, err := stackrecords.TakeEnvironmentLease(ctx, store, scope.tier, scope.slug, scope.env, token, holder, l.terms())
	if err != nil {
		return nil, err
	}
	return l.startHold(ctx, store, scope, token, holder, held), nil
}

func (l *environmentLeases) takeUnderNewToken(ctx context.Context, store keyvalue.Store, scope environmentScope, holder stackrecords.LeaseHolder) (*environmentHold, error) {
	token, err := stackrecords.NewEnvironmentLeaseToken()
	if err != nil {
		return nil, err
	}
	return l.take(ctx, store, scope, token, holder)
}

func (l *environmentLeases) keep(ctx context.Context, store keyvalue.Store, scope environmentScope, token string, holder stackrecords.LeaseHolder) (*environmentHold, error) {
	if err := stackrecords.RenewEnvironmentLease(ctx, store, scope.tier, scope.slug, scope.env, token, holder, l.terms()); err != nil {
		return nil, err
	}
	return l.startHold(ctx, store, scope, token, holder, true), nil
}

func (l *environmentLeases) startHold(ctx context.Context, store keyvalue.Store, scope environmentScope, token string, holder stackrecords.LeaseHolder, held bool) *environmentHold {
	leased, cancel := context.WithCancelCause(ctx)
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
		err := stackrecords.RenewEnvironmentLease(ctx, h.store, h.scope.tier, h.scope.slug, h.scope.env, h.token, h.holder, h.leases.terms())
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
				h.holder, h.scope.env, h.leases.ttl-h.leases.margin, h.holder, err))
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
	err := stackrecords.RenewEnvironmentLease(ctx, h.store, h.scope.tier, h.scope.slug, h.scope.env, h.token, h.holder, h.leases.terms())
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
	return stackrecords.ForgetEnvironmentLease(ctx, h.store, h.scope.tier, h.scope.slug, h.scope.env, h.token)
}

type environmentHolds []*environmentHold

func (l *environmentLeases) takeEach(ctx context.Context, store keyvalue.Store, scopes []environmentScope, holder stackrecords.LeaseHolder) (environmentHolds, error) {
	token, err := stackrecords.NewEnvironmentLeaseToken()
	if err != nil {
		return nil, err
	}
	var holds environmentHolds
	for _, scope := range scopes {
		hold, err := l.take(holds.context(ctx), store, scope, token, holder)
		if err != nil {
			_ = holds.release(ctx)
			return nil, err
		}
		holds = append(holds, hold)
	}
	return holds, nil
}

func (hs environmentHolds) context(ctx context.Context) context.Context {
	if len(hs) == 0 {
		return ctx
	}
	return hs[len(hs)-1].leased
}

func (hs environmentHolds) explain(err error) error {
	if err == nil {
		return nil
	}
	for _, hold := range hs {
		if lost := hold.readLoss(); lost != nil {
			return lost
		}
	}
	return err
}

func (hs environmentHolds) release(ctx context.Context) error {
	var errs []error
	for i := len(hs) - 1; i >= 0; i-- {
		errs = append(errs, hs[i].release(ctx))
	}
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
