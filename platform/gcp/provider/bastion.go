package gcp

import (
	"context"
	"fmt"
	"net/netip"
	"slices"

	"google.golang.org/api/googleapi"
	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
	"github.com/ocelhq/ocel/platform/gcp/provider/relay"
)

const (
	bastionImageName = "ocel-bastion"
	bastionImagePath = "/bastion"

	bastionConcurrency  = 250
	bastionMaxInstances = 2
	bastionCreateTries  = 2
)

var bastionImageTag = binaryImageTag(payloads.Bastion)

type bastion struct {
	clients        *gcloud.Clients
	deployAndRoute func(ctx context.Context, s run.Serving, progress progress.Log) (run.Release, error)
	pushBinary     func(ctx context.Context, tier environment.Tier, name, ref string, binary []byte, path string) error
	tearDown       func(ctx context.Context, service string, progress progress.Log) error
	grantInvoker   func(ctx context.Context, c *gcloud.Clients, service, member string) error
	prove          func(ctx context.Context, audience string) (envsource.IdentityProof, error)
	open           func(ctx context.Context, link relay.Link) (*relay.Forward, error)
	waited         func(ctx context.Context, attempt int) bool
}

func (b bastion) imageRef(tier environment.Tier) string {
	return b.clients.RepositoryPath(b.clients.Region, tier) + "/" + bastionImageName + ":" + bastionImageTag()
}

func (b bastion) audience(tier environment.Tier) string {
	return b.clients.Project() + "/" + b.clients.Bastion(tier)
}

func bastionDestinations() []relay.Destination {
	network := netip.MustParsePrefix(networkSubnetRange)
	return []relay.Destination{{Network: network, Port: postgresPort}, {Network: network, Port: defaultValkeyPort}}
}

func (b bastion) serving(tier environment.Tier, image string) run.Serving {
	return run.Serving{
		Service:     b.clients.Bastion(tier),
		Image:       image,
		Account:     b.clients.BastionAccountEmail(tier),
		Compute:     provider.ComputeServerless,
		Ingress:     run.IngressEverywhere,
		Concurrency: bastionConcurrency,
		Instances:   provider.Instances{Max: bastionMaxInstances},
		Timeout:     run.MaxRequestTimeout,
		Egress:      &run.PrivateEgress{Network: b.clients.NetworkPath(tier), Subnetwork: b.clients.SubnetworkPath(b.clients.Region, tier)},
		Audiences:   []string{b.audience(tier)},
		Env:         map[string]string{relay.AllowedEnv: relay.FormatDestinations(bastionDestinations())},
	}
}

func (b bastion) read(ctx context.Context, tier environment.Tier) (*runv2.GoogleCloudRunV2Service, error) {
	services, err := b.clients.Run()
	if err != nil {
		return nil, err
	}
	name := b.clients.Bastion(tier)
	found, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*runv2.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(b.clients.ServicePath(name)).Context(ctx).Do(call...)
	})
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the bastion %s: %w", name, err)
	}
	return found, nil
}

func (b bastion) provision(ctx context.Context, tier environment.Tier, progress progress.Log) (string, error) {
	var url string
	var err error
	for range bastionCreateTries {
		if url, err = b.provisionService(ctx, tier, progress); !gcloud.Taken(err) {
			break
		}
	}
	if err != nil {
		return "", err
	}
	principal, err := b.clients.Principal(ctx)
	if err != nil {
		return "", err
	}
	if err := b.grantInvoker(ctx, b.clients, b.clients.Bastion(tier), gcloud.MemberOf(principal)); err != nil {
		return "", err
	}
	if url == "" {
		return "", refusal.Refuse(refusal.CodeNotReady, "Cloud Run gave the bastion %s no URL, and a port forward connects to it", b.clients.Bastion(tier))
	}
	return url, nil
}

func (b bastion) provisionService(ctx context.Context, tier environment.Tier, progress progress.Log) (string, error) {
	current, err := b.read(ctx, tier)
	if err != nil {
		return "", err
	}
	image := b.imageRef(tier)
	desired := b.serving(tier, image)
	wanted, err := run.ServiceOf(desired)
	if err != nil {
		return "", err
	}
	if current != nil && sameBastion(current, wanted) && run.ServesLatest(current) {
		return current.Uri, nil
	}
	if current == nil {
		gcloud.EnsureProgress(progress).Say("Making the bastion " + desired.Service + " that forwards ports into the " + string(tier) + " tier's network")
	}
	if err := b.ensureAccount(ctx, tier); err != nil {
		return "", err
	}
	if current == nil || imageOf(current) != image {
		gcloud.EnsureProgress(progress).Say("Pushing the bastion image " + image)
		if err := b.pushBinary(ctx, tier, bastionImageName, image, payloads.Bastion(), bastionImagePath); err != nil {
			return "", err
		}
	}
	ran, err := b.deployAndRoute(ctx, desired, progress)
	return ran.Url, err
}

func (b bastion) ensureAccount(ctx context.Context, tier environment.Tier) error {
	account := b.clients.BastionAccount(tier)
	err := b.clients.CreateAccount(ctx, account,
		gcloud.Clipped("ocel bastion ("+b.clients.Namespace().String()+", "+string(tier)+")", gcloud.MaxDisplayNameBytes),
		"the identity the ocel bastion of the "+string(tier)+" tier runs as, which holds no role")
	if err != nil {
		return err
	}
	principal, err := b.clients.Principal(ctx)
	if err != nil {
		return err
	}
	return gcloud.UntilVisible(ctx, func() error {
		return gcloud.WrapAccountGrantError(gcloud.BindAccountRole(ctx, b.clients, account, runAsRole, gcloud.MemberOf(principal), true), b.clients.AppAccountsRolePath())
	})
}

func sameBastion(current, desired *runv2.GoogleCloudRunV2Service) bool {
	return sameServing(current, desired) &&
		current.IapEnabled == desired.IapEnabled &&
		slices.Equal(current.CustomAudiences, desired.CustomAudiences) &&
		sameVPCAccess(current.Template.VpcAccess, desired.Template.VpcAccess)
}

func sameVPCAccess(current, desired *runv2.GoogleCloudRunV2VpcAccess) bool {
	if current == nil || desired == nil {
		return current == desired
	}
	return current.Egress == desired.Egress &&
		slices.EqualFunc(current.NetworkInterfaces, desired.NetworkInterfaces, func(a, b *runv2.GoogleCloudRunV2NetworkInterface) bool {
			return run.RevisionName(a.Network) == run.RevisionName(b.Network) && run.RevisionName(a.Subnetwork) == run.RevisionName(b.Subnetwork)
		})
}

func (b bastion) remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	current, err := b.read(ctx, tier)
	if err != nil {
		return err
	}
	if current != nil {
		if err := b.tearDown(ctx, b.clients.Bastion(tier), progress); err != nil {
			return err
		}
	}
	return b.removeAccount(ctx, tier, progress)
}

func (b bastion) removeAccount(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	found, err := b.clients.AccountExists(ctx, b.clients.BastionAccount(tier))
	if err != nil || !found {
		return err
	}
	deleted, err := b.clients.DeleteAccount(ctx, b.clients.BastionAccount(tier))
	if deleted {
		gcloud.EnsureProgress(progress).Say("Deleted the " + b.clients.BastionAccount(tier) + " service account the bastion ran as")
	}
	return err
}
