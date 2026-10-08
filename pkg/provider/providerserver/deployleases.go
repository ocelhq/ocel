package providerserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	deployLeaseTTL     = 5 * time.Minute
	deployLeaseRenewal = 2 * time.Minute
	deployLeaseRelease = 30 * time.Second
)

type environmentScope struct {
	tier      environment.Tier
	slug, env string
}

func scopeOf(spec provider.DeploySpec) environmentScope {
	return environmentScope{tier: spec.Tier, slug: spec.Slug, env: spec.Env}
}

type deployLeases struct {
	ttl, renewal time.Duration

	mu       sync.Mutex
	renewing map[string]*leaseRenewal
}

type leaseRenewal struct {
	stop chan struct{}
	done chan struct{}
}

func newDeployLeases() *deployLeases {
	return &deployLeases{ttl: deployLeaseTTL, renewal: deployLeaseRenewal, renewing: map[string]*leaseRenewal{}}
}

func newLeaseToken() (string, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("mint a deploy lease token: %w", err)
	}
	return hex.EncodeToString(token), nil
}

func (l *deployLeases) hold(ctx context.Context, store keyvalue.Store, scope environmentScope, token string) error {
	if err := stackrecords.TakeDeployLease(ctx, store, scope.tier, scope.slug, scope.env, token, time.Now(), l.ttl); err != nil {
		return err
	}
	name := leaseName(scope, token)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.renewing[name] != nil {
		return nil
	}
	renewal := &leaseRenewal{stop: make(chan struct{}), done: make(chan struct{})}
	l.renewing[name] = renewal
	go l.renew(store, scope, token, renewal)
	return nil
}

func (l *deployLeases) renew(store keyvalue.Store, scope environmentScope, token string, renewal *leaseRenewal) {
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
		err := stackrecords.TakeDeployLease(ctx, store, scope.tier, scope.slug, scope.env, token, time.Now(), l.ttl)
		cancel()
		var refused refusal.Refusal
		if errors.As(err, &refused) && refused.Code == refusal.CodeBusy {
			return
		}
	}
}

func (l *deployLeases) release(ctx context.Context, store keyvalue.Store, scope environmentScope, token string) {
	name := leaseName(scope, token)
	l.mu.Lock()
	renewal := l.renewing[name]
	delete(l.renewing, name)
	l.mu.Unlock()
	if renewal != nil {
		close(renewal.stop)
		<-renewal.done
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deployLeaseRelease)
	defer cancel()
	_ = stackrecords.ForgetDeployLease(ctx, store, scope.tier, scope.slug, scope.env, token)
}

func leaseName(scope environmentScope, token string) string {
	return stackrecords.DeployLeaseKey(scope.tier, scope.slug, scope.env).String() + "|" + token
}

func (h *handlers) holdEnvironment(ctx context.Context, spec provider.DeploySpec, token string) error {
	p, err := h.session.use()
	if err != nil {
		return err
	}
	return h.leases.hold(ctx, p.KeyValues(), scopeOf(spec), token)
}

func (h *handlers) releaseEnvironment(ctx context.Context, spec provider.DeploySpec, token string) {
	if p, err := h.session.use(); err == nil {
		h.leases.release(ctx, p.KeyValues(), scopeOf(spec), token)
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
