package gcp

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const (
	gateDir             = "/var/run/ocel/gate"
	gateFile            = "gate"
	gateVolume          = "gate"
	gateReaderRole      = "roles/secretmanager.secretAccessor"
	latestSecretVersion = "latest"
)

func gateMount(secret string) run.SecretMount {
	return run.SecretMount{Name: gateVolume, Secret: secret, Dir: gateDir, File: gateFile}
}

func writeGate(ctx context.Context, c *gcloud.Clients, tier environment.Tier, slug string, held *gate.Gate) error {
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	name := c.GateSecret(tier, slug)
	written, err := gate.EncodeMounted(held)
	if err != nil {
		return err
	}
	current, found, err := c.ReadSecretVersion(ctx, service, name, latestSecretVersion)
	if err != nil {
		return err
	}
	if !found {
		if err := c.EnsureSecret(ctx, service, name); err != nil {
			return err
		}
		if current, found, err = c.ReadSecretVersion(ctx, service, name, latestSecretVersion); err != nil {
			return err
		}
	}
	if found && bytes.Equal(current, written) {
		return nil
	}
	added, err := c.AddSecretVersion(ctx, service, name, written)
	if err != nil {
		return err
	}
	return c.DestroyVersionsBeforePrevious(ctx, service, name, added)
}

func readGate(ctx context.Context, c *gcloud.Clients, tier environment.Tier, slug string) (*gate.Gate, bool, error) {
	service, err := c.Secrets()
	if err != nil {
		return nil, false, err
	}
	current, found, err := c.ReadSecretVersion(ctx, service, c.GateSecret(tier, slug), latestSecretVersion)
	if err != nil || !found {
		return nil, false, err
	}
	held, err := gate.DecodeMounted(current)
	return held, true, err
}

func deleteGate(ctx context.Context, c *gcloud.Clients, tier environment.Tier, slug string) error {
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	return c.DeleteSecret(ctx, service, c.GateSecret(tier, slug))
}

func gateGrants(c *gcloud.Clients, spec provider.StackSpec, member string) []grants.Grant {
	if !mountsGateOnAnyService(spec) {
		return nil
	}
	return []grants.Grant{grants.On(grants.Secret, c.GateSecret(spec.Ref.Tier, spec.Ref.Project), gateReaderRole, member)}
}

func mountsGateOnAnyService(spec provider.StackSpec) bool {
	if !mountsGate(spec) {
		return false
	}
	return spec.App.Compute == provider.ComputeContainer || len(spec.App.Functions) == 0 ||
		slices.ContainsFunc(spec.App.Functions, func(fn provider.FunctionSpec) bool { return !ignoresGate(spec, fn) })
}

var gateFrameworks = []string{buildoutput.FrameworkNode, buildoutput.FrameworkNext, buildoutput.FrameworkSvelteKit}

type gatedServing struct {
	env    map[string]string
	mounts []run.SecretMount
}

func (g gatedServing) hasGate() bool { return len(g.env) > 0 }

func (p *Provider) provisionGate(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec) (gatedServing, error) {
	if !mountsGate(spec) {
		return gatedServing{}, nil
	}
	tier, slug := spec.Ref.Tier, spec.Ref.Project
	held, found, err := readGate(ctx, c, tier, slug)
	switch {
	case err != nil:
		return gatedServing{}, fmt.Errorf("read the %s gate of %s from the secret its services mount: %w", tier, slug, err)
	case !found:
		return gatedServing{}, fmt.Errorf("the secret the %s services of %s mount does not exist, so a service mounting it would answer every request with 503: deploy again, which writes the secret before any service mounts it", tier, slug)
	case held == nil && isGated(spec):
		return gatedServing{}, fmt.Errorf("the secret the %s services of %s mount holds no gate, so they would serve this protected deploy to anyone: deploy again", tier, slug)
	}
	if p.gateInEnv {
		// HACK: floci-gcp runs no secret volume, so an emulated service takes its gate, or the withdrawn marker, from its environment and a change of gate rolls a revision; drop gateInEnv once floci-gcp mounts secret volumes (#1605).
		encoded, err := gate.EncodeMounted(held)
		if err != nil {
			return gatedServing{}, err
		}
		return gatedServing{env: map[string]string{gate.MountedInlineVar: string(encoded)}}, nil
	}
	mount := gateMount(c.GateSecret(tier, slug))
	return gatedServing{
		env:    map[string]string{gate.MountedFileVar: mount.Dir + "/" + mount.File},
		mounts: []run.SecretMount{mount},
	}, nil
}

func applyGate(ctx context.Context, c *gcloud.Clients, g gatedServing, what string, served *run.Serving) error {
	if !g.hasGate() {
		return nil
	}
	env := maps.Clone(g.env)
	if served.Compute == provider.ComputeContainer {
		token, err := readProbeToken(ctx, c, served.Service)
		if err != nil {
			return err
		}
		if token == "" {
			if token, err = newProbeToken(); err != nil {
				return err
			}
		}
		served.ProbeToken = token
		env[originguard.ProbeTokenVar] = token
	}
	values, err := mergeValues(what, gateValues, served.Env, env)
	if err != nil {
		return err
	}
	served.Env = values
	served.Mounts = append(served.Mounts, g.mounts...)
	return nil
}

func mountsGate(spec provider.StackSpec) bool {
	return run.FactsOf(spec.Edge).GatesAtOrigin
}

func isGated(spec provider.StackSpec) bool {
	return spec.App.Gate != nil
}

func ignoresGate(spec provider.StackSpec, fn provider.FunctionSpec) bool {
	return mountsGate(spec) && !slices.Contains(gateFrameworks, fn.Framework.Name)
}

func refuseGateOnFunction(spec provider.StackSpec, fn provider.FunctionSpec) error {
	if !isGated(spec) || !ignoresGate(spec, fn) {
		return nil
	}
	tier := spec.Ref.Tier
	return refusal.Refuse(refusal.CodeUnsupported,
		"%s is a %s function on Cloud Run with nothing in front of it, and a function of any framework but %v ignores the gate, so it would serve this protected %s deploy to anyone: "+
			"front the %s tier with an edge that gates it, or turn protection.%s off",
		fn.Name, fn.Framework.Name, gateFrameworks, tier, tier, tier)
}

type gateSecrets struct{ p *Provider }

func (s gateSecrets) Publish(ctx context.Context, tier environment.Tier, slug string, held *gate.Gate) error {
	c, err := s.p.openClients(ctx)
	if err != nil {
		return err
	}
	return writeGate(ctx, c, tier, slug, held)
}

func (s gateSecrets) Remove(ctx context.Context, tier environment.Tier, slug string) error {
	c, err := s.p.openClients(ctx)
	if err != nil {
		return err
	}
	return deleteGate(ctx, c, tier, slug)
}
