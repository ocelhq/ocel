package providerserver

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	environmentLeaseTTL            = 5 * time.Minute
	environmentLeaseRenewal        = 2 * time.Minute
	environmentLeaseReleaseTimeout = 30 * time.Second
)

type environmentScope struct {
	tier      environment.Tier
	slug, env string
}

func newEnvironmentScope(spec provider.DeploySpec) environmentScope {
	return environmentScope{tier: spec.Tier, slug: spec.Slug, env: spec.Env}
}

type environmentLeases struct {
	ttl, renewal time.Duration

	mu       sync.Mutex
	renewing map[heldLease]*leaseRenewal
}

type leaseRenewal struct {
	stop chan struct{}
	done chan struct{}
}

func newEnvironmentLeases() *environmentLeases {
	return &environmentLeases{ttl: environmentLeaseTTL, renewal: environmentLeaseRenewal, renewing: map[heldLease]*leaseRenewal{}}
}

func (l *environmentLeases) hold(ctx context.Context, store keyvalue.Store, scope environmentScope, token string) (taken bool, err error) {
	if err := stackrecords.TakeEnvironmentLease(ctx, store, scope.tier, scope.slug, scope.env, token, time.Now(), l.ttl); err != nil {
		return false, err
	}
	held := heldLease{scope: scope, token: token}
	l.mu.Lock()
	defer l.mu.Unlock()
	if renewal := l.renewing[held]; renewal != nil && !renewal.ended() {
		return false, nil
	}
	renewal := &leaseRenewal{stop: make(chan struct{}), done: make(chan struct{})}
	l.renewing[held] = renewal
	go l.renew(store, scope, token, renewal)
	return true, nil
}

func (l *environmentLeases) renew(store keyvalue.Store, scope environmentScope, token string, renewal *leaseRenewal) {
	defer close(renewal.done)
	ticker := time.NewTicker(l.renewal)
	defer ticker.Stop()
	for {
		select {
		case <-renewal.stop:
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), l.renewal)
		err := stackrecords.RenewEnvironmentLease(ctx, store, scope.tier, scope.slug, scope.env, token, time.Now(), l.ttl)
		cancel()
		var refused refusal.Refusal
		if errors.As(err, &refused) && refused.Code == refusal.CodeBusy {
			return
		}
	}
}

func (r *leaseRenewal) ended() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func (l *environmentLeases) confirm(ctx context.Context, store keyvalue.Store, scope environmentScope, token string) error {
	return stackrecords.RenewEnvironmentLease(ctx, store, scope.tier, scope.slug, scope.env, token, time.Now(), l.ttl)
}

func (l *environmentLeases) release(ctx context.Context, store keyvalue.Store, scope environmentScope, token string) {
	held := heldLease{scope: scope, token: token}
	l.mu.Lock()
	renewal := l.renewing[held]
	delete(l.renewing, held)
	l.mu.Unlock()
	if renewal != nil {
		close(renewal.stop)
		<-renewal.done
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), environmentLeaseReleaseTimeout)
	defer cancel()
	_ = stackrecords.ForgetEnvironmentLease(ctx, store, scope.tier, scope.slug, scope.env, token)
}

type heldLease struct {
	scope environmentScope
	token string
}

func (h *handlers) holdEnvironment(ctx context.Context, spec provider.DeploySpec, token string) (taken bool, err error) {
	p, err := h.session.use()
	if err != nil {
		return false, err
	}
	return h.leases.hold(ctx, p.KeyValues(), newEnvironmentScope(spec), token)
}

func (h *handlers) releaseEnvironment(ctx context.Context, spec provider.DeploySpec, token string) {
	if p, err := h.session.use(); err == nil {
		h.leases.release(ctx, p.KeyValues(), newEnvironmentScope(spec), token)
	}
}

func (h *handlers) AbandonDeploy(ctx context.Context, req *contractv1.AbandonDeployRequest) (*contractv1.AbandonDeployResponse, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	tier, err := decodeTier(req.GetEnvironment().GetTier())
	if err != nil {
		return nil, err
	}
	env, err := envName(req.GetEnvironment())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	h.leases.release(ctx, p.KeyValues(), environmentScope{tier: tier, slug: req.GetSlug(), env: env}, req.GetLeaseToken())
	return &contractv1.AbandonDeployResponse{}, nil
}
