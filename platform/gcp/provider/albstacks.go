package gcp

import (
	"context"
	"encoding/base64"
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
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const cloudStorageScheme = "gs"

type albStacks struct{ p *Provider }

func (s albStacks) Up(
	ctx context.Context,
	target alb.Target,
	program alb.Program,
	progress progress.Log,
) (map[string]string, error) {
	automation, ref, err := s.opened(ctx, target)
	if err != nil {
		return nil, err
	}
	if _, err := automation.Run(ctx, ref, program, progress); err != nil {
		return nil, err
	}
	return outputsOf(ctx, automation, ref)
}

func (s albStacks) Destroy(ctx context.Context, target alb.Target, progress progress.Log) error {
	automation, ref, err := s.opened(ctx, target)
	if err != nil {
		return err
	}
	return automation.Destroy(ctx, ref, progress)
}

func (s albStacks) Outputs(ctx context.Context, target alb.Target) (map[string]string, error) {
	automation, ref, err := s.opened(ctx, target)
	if err != nil {
		return nil, err
	}
	return outputsOf(ctx, automation, ref)
}

func outputsOf(ctx context.Context, automation *pulumi.Automation[alb.Program], ref provider.StackRef) (map[string]string, error) {
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

func (s albStacks) opened(ctx context.Context, target alb.Target) (*pulumi.Automation[alb.Program], provider.StackRef, error) {
	clients, err := s.p.openClients(ctx)
	if err != nil {
		return nil, provider.StackRef{}, err
	}
	passphrase, err := s.passphrase(ctx, clients, target.Tier)
	if err != nil {
		return nil, provider.StackRef{}, err
	}
	config, ref := s.config(clients, target, passphrase)
	return pulumi.New(config), ref, nil
}

func (s albStacks) config(clients *clients, target alb.Target, passphrase string) (pulumi.Config[alb.Program], provider.StackRef) {
	project := naming.PulumiProject(target.Prefix())
	config := pulumi.Config[alb.Program]{
		Backend: pulumi.Backend{
			URL:        naming.StateBackendURL(cloudStorageScheme, clients.StateBucket(target.Tier), project),
			Passphrase: passphrase,
			Project:    project,
			Env: map[string]string{
				"GOOGLE_PROJECT": clients.project,
				"GOOGLE_REGION":  clients.region,
			},
		},
		Program: func(ctx *sdk.Context, program alb.Program) error { return program(ctx, clients.project) },
	}
	if target.Slug == "" {
		config.Refresh = refreshesTheFront
	}
	return config, provider.StackRef{Project: project, Tier: target.Tier, Name: naming.InfraStack(target.Name())}
}

func refreshesTheFront(provider.StackRef, pulumi.Operation) bool { return true }

func (s albStacks) passphrase(ctx context.Context, clients *clients, tier environment.Tier) (string, error) {
	secrets, err := clients.Secrets()
	if err != nil {
		return "", err
	}
	secret := clients.PassphraseSecret(tier)
	name := "projects/" + clients.project + "/secrets/" + secret + "/versions/latest"
	version, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*secretmanager.AccessSecretVersionResponse, error) {
		return secrets.Projects.Secrets.Versions.Access(name).Context(ctx).Do(call...)
	})
	if err != nil {
		if absent(err) {
			return "", refusal.Refuse(refusal.CodeNotReady,
				"the %s edge keeps the state of the load balancer it provisions sealed under the %s secret, and this project has none for tier %s: run `ocel bootstrap` for this tier first",
				alb.Kind, secret, tier)
		}
		return "", fmt.Errorf("read the passphrase the %s edge's state is sealed with: %w", alb.Kind, err)
	}
	if version.Payload == nil {
		return "", fmt.Errorf("read the passphrase the %s edge's state is sealed with: %s has a version with no payload", alb.Kind, secret)
	}
	passphrase, err := base64.StdEncoding.DecodeString(version.Payload.Data)
	if err != nil {
		return "", fmt.Errorf("read the passphrase the %s edge's state is sealed with: %w", alb.Kind, err)
	}
	return string(passphrase), nil
}

var _ alb.Stacks = albStacks{}
