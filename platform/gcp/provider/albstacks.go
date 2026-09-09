package gcp

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const pulumiProjectName = "ocel-alb"

const cloudStorageScheme = "gs"

type albStacks struct{ p *Provider }

type albProgram struct{ run alb.Program }

func (a albProgram) Run(ctx *pulumi.Context, _ providerkit.StackPlan) error { return a.run(ctx) }

func (s albStacks) Up(
	ctx context.Context,
	class edge.Class,
	stack string,
	program alb.Program,
	report edge.Reporter,
) (map[string]string, error) {
	adapter, plan, err := s.at(ctx, class, stack, program)
	if err != nil {
		return nil, err
	}
	if _, err := adapter.Run(ctx, plan, report); err != nil {
		return nil, err
	}
	return outputsOf(ctx, adapter, plan.Ref)
}

func (s albStacks) Destroy(ctx context.Context, class edge.Class, stack string, report edge.Reporter) error {
	adapter, plan, err := s.at(ctx, class, stack, nil)
	if err != nil {
		return err
	}
	return adapter.Destroy(ctx, plan.Ref, report)
}

func (s albStacks) Outputs(ctx context.Context, class edge.Class, stack string) (map[string]string, error) {
	adapter, plan, err := s.at(ctx, class, stack, nil)
	if err != nil {
		return nil, err
	}
	return outputsOf(ctx, adapter, plan.Ref)
}

func outputsOf(ctx context.Context, adapter *kitpulumi.Adapter, ref providerkit.StackRef) (map[string]string, error) {
	outputs, err := adapter.Outputs(ctx, ref, edge.DiscardReporter())
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

func (s albStacks) at(
	ctx context.Context,
	class edge.Class,
	stack string,
	program alb.Program,
) (*kitpulumi.Adapter, providerkit.StackPlan, error) {
	passphrase, err := s.passphrase(ctx, class)
	if err != nil {
		return nil, providerkit.StackPlan{}, err
	}
	config := kitpulumi.Config{
		Access: kitpulumi.Access{
			BackendURL: naming.StateBackendURL(cloudStorageScheme, s.p.Names().StateBucket(class), pulumiProjectName),
			Passphrase: passphrase,
			Project:    pulumiProjectName,
			Env: map[string]string{
				"GOOGLE_PROJECT": s.p.options.Project,
				"GOOGLE_REGION":  s.p.options.Region,
			},
		},
	}
	if program != nil {
		config.Program = albProgram{run: program}
	}
	return kitpulumi.New(config), providerkit.StackPlan{
		Ref:  providerkit.StackRef{Project: pulumiProjectName, Class: class, Name: naming.InfraStack(stack)},
		Kind: providerkit.StackInfra,
	}, nil
}

func (s albStacks) passphrase(ctx context.Context, class edge.Class) (string, error) {
	secrets, err := s.p.clients.Secrets()
	if err != nil {
		return "", err
	}
	secret := s.p.Names().PassphraseSecret(class)
	name := "projects/" + s.p.options.Project + "/secrets/" + secret + "/versions/latest"
	held, err := attempted(ctx, func(call ...googleapi.CallOption) (*secretmanager.AccessSecretVersionResponse, error) {
		return secrets.Projects.Secrets.Versions.Access(name).Context(ctx).Do(call...)
	})
	if err != nil {
		if absent(err) {
			return "", providerkit.Refuse(providerkit.CodeNotReady,
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
