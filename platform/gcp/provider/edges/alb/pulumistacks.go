package alb

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const cloudStorageScheme = "gs"

type PulumiStacks struct {
	OpenClients func(context.Context) (*gcloud.Clients, error)
	SkipChecks  bool
}

func (s PulumiStacks) Up(
	ctx context.Context,
	target Target,
	program Program,
	progress progress.Log,
) (map[string]string, error) {
	automation, ref, err := s.opened(ctx, target)
	if err != nil {
		return nil, err
	}
	if _, err := automation.Run(ctx, ref, program, provider.Lease{}, progress); err != nil {
		return nil, err
	}
	return outputsOf(ctx, automation, ref)
}

func (s PulumiStacks) Destroy(ctx context.Context, target Target, progress progress.Log) error {
	automation, ref, err := s.opened(ctx, target)
	if errors.As(err, new(noPassphrase)) {
		return nil
	}
	if err != nil {
		return err
	}
	return automation.Destroy(ctx, ref, provider.Lease{}, progress)
}

func (s PulumiStacks) Outputs(ctx context.Context, target Target) (map[string]string, error) {
	automation, ref, err := s.opened(ctx, target)
	if errors.As(err, new(noPassphrase)) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return outputsOf(ctx, automation, ref)
}

func outputsOf(ctx context.Context, automation *pulumi.Automation[Program], ref provider.StackRef) (map[string]string, error) {
	outputs, err := automation.Outputs(ctx, ref, progress.Discard())
	if err != nil {
		return nil, err
	}
	read := make(map[string]string, len(outputs))
	for name, output := range outputs {
		if value, ok := output.Value.(string); ok {
			read[name] = value
		}
	}
	return read, nil
}

func (s PulumiStacks) opened(ctx context.Context, target Target) (*pulumi.Automation[Program], provider.StackRef, error) {
	clients, err := s.OpenClients(ctx)
	if err != nil {
		return nil, provider.StackRef{}, err
	}
	passphrase, err := s.passphrase(ctx, clients, target.Tier)
	if err != nil {
		return nil, provider.StackRef{}, err
	}
	buckets, err := clients.TierBuckets(ctx, target.Tier, naming.StorageState)
	if err != nil {
		return nil, provider.StackRef{}, err
	}
	config, ref := s.config(clients, buckets[naming.StorageState], target, passphrase)
	return pulumi.New(config), ref, nil
}

func (s PulumiStacks) config(clients *gcloud.Clients, stateBucket string, target Target, passphrase string) (pulumi.Config[Program], provider.StackRef) {
	project := naming.PulumiProject(target.Prefix())
	config := pulumi.Config[Program]{
		Backend: pulumi.Backend{
			URL:        naming.StateBackendURL(cloudStorageScheme, stateBucket, project),
			Passphrase: passphrase,
			Project:    project,
			Env: map[string]string{
				"GOOGLE_PROJECT": clients.Project(),
				"GOOGLE_REGION":  clients.Region,
			},
		},
		Program: func(ctx *sdk.Context, program Program) error {
			if err := program.Patches.Install(ctx); err != nil {
				return err
			}
			return program.Run(ctx, clients.Project())
		},
	}
	if target.Slug == "" {
		config.Refresh = s.refreshesTheFront
	}
	return config, provider.StackRef{Project: project, Tier: target.Tier, Name: naming.InfraStack(target.Name())}
}

func ListTierProjects(tier environment.Tier) []string {
	return []string{
		naming.PulumiProject(LoadBalancerStack(tier)),
		naming.PulumiProject(GateStack(tier)),
	}
}

func (s PulumiStacks) refreshesTheFront(_ provider.StackRef, op pulumi.Operation) bool {
	return op != pulumi.OperationDestroy || !s.SkipChecks
}

func (s PulumiStacks) passphrase(ctx context.Context, clients *gcloud.Clients, tier environment.Tier) (string, error) {
	secrets, err := clients.Secrets()
	if err != nil {
		return "", err
	}
	secret := clients.PassphraseSecret(tier)
	name := "projects/" + clients.Project() + "/secrets/" + secret + "/versions/latest"
	version, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*secretmanager.AccessSecretVersionResponse, error) {
		return secrets.Projects.Secrets.Versions.Access(name).Context(ctx).Do(call...)
	})
	if err != nil {
		if gcloud.Absent(err) {
			return "", noPassphrase{refusal.Refusal{Code: refusal.CodeNotReady, Message: fmt.Sprintf(
				"the %s edge keeps the state of the load balancer it provisions sealed under the %s secret, and this project has none for tier %s: run `ocel bootstrap` for this tier first",
				Kind, secret, tier)}}
		}
		return "", fmt.Errorf("read the passphrase the %s edge's state is sealed with: %w", Kind, err)
	}
	if version.Payload == nil {
		return "", fmt.Errorf("read the passphrase the %s edge's state is sealed with: %s has a version with no payload", Kind, secret)
	}
	passphrase, err := base64.StdEncoding.DecodeString(version.Payload.Data)
	if err != nil {
		return "", fmt.Errorf("read the passphrase the %s edge's state is sealed with: %w", Kind, err)
	}
	return string(passphrase), nil
}

type noPassphrase struct{ refusal.Refusal }

func (n noPassphrase) Unwrap() error { return n.Refusal }

var _ Stacks = PulumiStacks{}
