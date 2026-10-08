package deploy

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type infraProvisioning struct {
	providerProcess *providerprocess.Provider
	cfg             *project.Project
	env             *environmentv1.Environment
	aliasToken      string
	workerCeilings  []provider.WorkerCeiling
	provisions      bool
	dry             bool
	leaseToken      string
	renewal         time.Duration
	renewalTimeout  time.Duration

	sent      *contractv1.ProvisionInfraRequest
	attempted bool
	shipped   bool
	renewing  *leaseRenewal
}

type leaseRenewal struct {
	cancel context.CancelFunc
	done   chan struct{}
}

const (
	leaseRenewalInterval = 2 * time.Minute
	abandonTimeout       = 30 * time.Second
)

func newInfraProvisioning(providerProcess *providerprocess.Provider, env *environmentv1.Environment, facts preflightFacts, dry, prebuilt bool) *infraProvisioning {
	if prebuilt {
		return nil
	}
	return &infraProvisioning{
		providerProcess: providerProcess,
		cfg:             facts.project,
		env:             env,
		aliasToken:      facts.builtAlias,
		workerCeilings:  facts.workerCeilings,
		provisions:      !dry && env.GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		dry:             dry,
		renewal:         leaseRenewalInterval,
		renewalTimeout:  leaseRenewalInterval,
	}
}

func (i *infraProvisioning) provision(ctx context.Context, resources []declaration.Resource, inline []*bindingsv1.Binding) error {
	if i == nil || !i.provisions || len(resources) == 0 {
		return nil
	}
	assembled, err := i.assemble(resources)
	if err != nil {
		return err
	}
	if i.leaseToken == "" {
		if i.leaseToken, err = stackrecords.NewEnvironmentLeaseToken(); err != nil {
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
			return
		}
	}
}

func (i *infraProvisioning) stopRenewing() {
	if i == nil || i.renewing == nil {
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
	return i != nil && i.sent != nil
}

func (i *infraProvisioning) readLeaseToken() string {
	if !i.isProvisioned() {
		return ""
	}
	return i.leaseToken
}

func (i *infraProvisioning) markShipped() {
	if i != nil {
		i.shipped = true
	}
}

func (i *infraProvisioning) abandon(ctx context.Context, slug string) {
	i.stopRenewing()
	if i == nil || !i.attempted || i.shipped {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abandonTimeout)
	defer cancel()
	_ = i.providerProcess.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
		_, err := client.AbandonDeploy(ctx, &contractv1.AbandonDeployRequest{Slug: slug, Environment: i.env, LeaseToken: i.leaseToken})
		return err
	})
}
