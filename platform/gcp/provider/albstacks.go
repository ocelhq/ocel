package gcp

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const cloudStorageScheme = "gs"

type albStacks struct{ p *Provider }

type albProgram struct {
	run     alb.Program
	project string
}

func (a albProgram) Run(ctx *pulumi.Context, _ provider.StackSpec) error {
	return a.run(ctx, a.project)
}

func (s albStacks) Up(
	ctx context.Context,
	target alb.Target,
	program alb.Program,
	progress edge.Progress,
) (map[string]string, error) {
	automation, spec, err := s.opened(ctx, target, program)
	if err != nil {
		return nil, err
	}
	if _, err := automation.Run(ctx, spec, progress); err != nil {
		return nil, err
	}
	return outputsOf(ctx, automation, spec.Ref)
}

func (s albStacks) Destroy(ctx context.Context, target alb.Target, progress edge.Progress) error {
	automation, spec, err := s.opened(ctx, target, nil)
	if err != nil {
		return err
	}
	return automation.Destroy(ctx, spec.Ref, progress)
}

func (s albStacks) Outputs(ctx context.Context, target alb.Target) (map[string]string, error) {
	automation, spec, err := s.opened(ctx, target, nil)
	if err != nil {
		return nil, err
	}
	return outputsOf(ctx, automation, spec.Ref)
}

func outputsOf(ctx context.Context, automation *kitpulumi.Automation, ref provider.StackRef) (map[string]string, error) {
	outputs, err := automation.Outputs(ctx, ref, edge.DiscardProgress())
	if err != nil {
		return nil, err
	}
	read := make(map[string]string, len(outputs))
	for name, output := range outputs {
		if value, held := output.Value.(string); held {
			read[name] = value
		}
	}
	return read, nil
}

func (s albStacks) opened(
	ctx context.Context,
	target alb.Target,
	program alb.Program,
) (*kitpulumi.Automation, provider.StackSpec, error) {
	clients, err := s.p.stood(ctx)
	if err != nil {
		return nil, provider.StackSpec{}, err
	}
	passphrase, err := s.passphrase(ctx, clients, target.Class)
	if err != nil {
		return nil, provider.StackSpec{}, err
	}
	config, spec := s.config(clients, target, passphrase, program)
	return kitpulumi.New(config), spec, nil
}

func (s albStacks) config(
	clients *clients,
	target alb.Target,
	passphrase string,
	program alb.Program,
) (kitpulumi.Config, provider.StackSpec) {
	project := naming.PulumiProject(target.Prefix())
	config := kitpulumi.Config{
		Access: kitpulumi.Access{
			BackendURL: naming.StateBackendURL(cloudStorageScheme, clients.StateBucket(target.Class), project),
			Passphrase: passphrase,
			Project:    project,
			Env: map[string]string{
				"GOOGLE_PROJECT": clients.project,
				"GOOGLE_REGION":  clients.region,
			},
		},
	}
	if program != nil {
		config.Program = albProgram{run: program, project: clients.project}.Run
	}
	if target.Slug == "" {
		config.Refresh = refreshesTheFront
	}
	return config, provider.StackSpec{
		Ref:  provider.StackRef{Project: project, Class: target.Class, Name: naming.InfraStack(target.Name())},
		Kind: provider.StackInfra,
	}
}

func refreshesTheFront(provider.StackRef, kitpulumi.Op) bool { return true }

func (s albStacks) passphrase(ctx context.Context, clients *clients, class edge.Class) (string, error) {
	secrets, err := clients.Secrets()
	if err != nil {
		return "", err
	}
	secret := clients.PassphraseSecret(class)
	name := "projects/" + clients.project + "/secrets/" + secret + "/versions/latest"
	held, err := attempted(ctx, func(call ...googleapi.CallOption) (*secretmanager.AccessSecretVersionResponse, error) {
		return secrets.Projects.Secrets.Versions.Access(name).Context(ctx).Do(call...)
	})
	if err != nil {
		if absent(err) {
			return "", refusal.Refuse(refusal.CodeNotReady,
				"the %s edge keeps the state of the load balancer it stands up sealed under the %s secret, and this project holds none for class %s: run `ocel bootstrap` for this class first",
				alb.Kind, secret, class)
		}
		return "", fmt.Errorf("read the passphrase the %s edge's state is sealed with: %w", alb.Kind, err)
	}
	if held.Payload == nil {
		return "", fmt.Errorf("read the passphrase the %s edge's state is sealed with: %s holds a version with no payload", alb.Kind, secret)
	}
	passphrase, err := base64.StdEncoding.DecodeString(held.Payload.Data)
	if err != nil {
		return "", fmt.Errorf("read the passphrase the %s edge's state is sealed with: %w", alb.Kind, err)
	}
	return string(passphrase), nil
}

var _ alb.Stacks = albStacks{}
