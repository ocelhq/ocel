package deploy

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/pkg/lease"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

type infraProvisioning struct {
	providerProcess *providerprocess.Provider
	cfg             *project.Project
	env             *environmentv1.Environment
	aliasToken      string
	mintedAlias     string
	workerCeilings  []provider.WorkerCeiling
	leaseToken      string
	renewal         time.Duration
	renewalTimeout  time.Duration

	sent      *contractv1.ProvisionInfraRequest
	attempted bool
	shipped   bool
	renewing  *leaseRenewal
	loseLease context.CancelCauseFunc
}

type leaseLostError struct {
	refusal error
}

func (e *leaseLostError) Error() string {
	return "the provider refused to renew this deploy's lease: " + e.refusal.Error()
}

func (e *leaseLostError) Unwrap() error { return e.refusal }

type leaseRenewal struct {
	cancel context.CancelFunc
	done   chan struct{}
}

const (
	leaseRenewalInterval = 2 * time.Minute
	abandonTimeout       = 30 * time.Second
)

func newInfraProvisioning(providerProcess *providerprocess.Provider, env *environmentv1.Environment, facts preflightFacts) *infraProvisioning {
	return &infraProvisioning{
		providerProcess: providerProcess,
		cfg:             facts.project,
		env:             env,
		aliasToken:      facts.builtAlias,
		mintedAlias:     facts.mintedAlias,
		workerCeilings:  facts.workerCeilings,
		renewal:         leaseRenewalInterval,
		renewalTimeout:  leaseRenewalInterval,
	}
}

func (i *infraProvisioning) provision(ctx context.Context, resources []declaration.Resource, inline []*bindingsv1.Binding) error {
	if len(resources) == 0 {
		return nil
	}
	assembled, err := i.assemble(resources)
	if err != nil {
		return err
	}
	if i.leaseToken == "" {
		if i.leaseToken, err = lease.NewToken(); err != nil {
			return err
		}
	}
	req := &contractv1.ProvisionInfraRequest{
		Manifest:       assembled,
		Environment:    i.env,
		Edge:           i.cfg.EdgeSelection(),
		InlineBindings: inline,
		AliasToken:     i.aliasToken,
		LeaseToken:     i.leaseToken,
	}
	if proto.Equal(req, i.sent) {
		return nil
	}
	i.attempted = true
	if _, err := providerprocess.Stream(ctx, i.providerProcess, "ProvisionInfra", req, contractv1connect.ProviderServiceClient.ProvisionInfra); err != nil {
		return err
	}
	i.sent = req
	i.startRenewing()
	return nil
}

func (i *infraProvisioning) startRenewing() {
	if i.renewing != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	i.renewing = &leaseRenewal{cancel: cancel, done: make(chan struct{})}
	go i.renew(ctx, i.renewing.done)
}

func (i *infraProvisioning) renew(renewing context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(i.renewal)
	defer ticker.Stop()
	req := &contractv1.RenewDeployLeaseRequest{Slug: i.cfg.Slug, Environment: i.env, LeaseToken: i.leaseToken}
	for {
		select {
		case <-renewing.Done():
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(renewing, i.renewalTimeout)
		err := i.providerProcess.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
			_, err := client.RenewDeployLease(ctx, req)
			return err
		})
		cancel()
		if _, refused := provider.RefusedCode(err); refused {
			if i.loseLease != nil {
				i.loseLease(&leaseLostError{refusal: err})
			}
			return
		}
	}
}

func (i *infraProvisioning) holdLease(ctx context.Context) (context.Context, context.CancelFunc) {
	held, cancel := context.WithCancelCause(ctx)
	i.loseLease = cancel
	return held, func() { cancel(nil) }
}

func (i *infraProvisioning) lostLease(held context.Context, err error) error {
	var lost *leaseLostError
	if err != nil && errors.As(context.Cause(held), &lost) {
		return lost
	}
	return err
}

func (i *infraProvisioning) stopRenewing() {
	if i.renewing == nil {
		return
	}
	i.renewing.cancel()
	<-i.renewing.done
	i.renewing = nil
}

func (i *infraProvisioning) assemble(resources []declaration.Resource) (*contractv1.Manifest, error) {
	return manifest.AssembleInfra(manifest.InfraInput{
		Project:        i.cfg,
		Tier:           i.env.GetTier(),
		Resources:      resources,
		WorkerCeilings: i.workerCeilings,
	})
}

func (i *infraProvisioning) isProvisioned() bool {
	return i.sent != nil
}

func (i *infraProvisioning) readLeaseToken() string {
	if !i.isProvisioned() {
		return ""
	}
	return i.leaseToken
}

func (i *infraProvisioning) markShipped() {
	i.shipped = true
}

func (i *infraProvisioning) release(ctx context.Context, slug string) error {
	i.stopRenewing()
	if i.shipped {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abandonTimeout)
	defer cancel()
	if i.attempted {
		_ = i.providerProcess.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
			_, err := client.AbandonDeploy(ctx, &contractv1.AbandonDeployRequest{Slug: slug, Environment: i.env, LeaseToken: i.leaseToken})
			return err
		})
	}
	return forgetUnclaimedPreviewAlias(ctx, i.providerProcess, slug, i.env, i.mintedAlias)
}
