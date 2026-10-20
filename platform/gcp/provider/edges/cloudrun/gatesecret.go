package cloudrun

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
	GateReaderRole      = "roles/secretmanager.secretAccessor"
	latestSecretVersion = "latest"
	gateValues          = "the gate it mounts"
)

func gateMount(secret string) run.SecretMount {
	return run.SecretMount{Name: gateVolume, Secret: secret, Dir: gateDir, File: gateFile}
}

func WriteGate(ctx context.Context, c *gcloud.Clients, tier environment.Tier, slug string, held *gate.Gate) error {
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	name, err := c.GateSecret(tier, slug)
	if err != nil {
		return err
	}
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

func ReadGate(ctx context.Context, c *gcloud.Clients, tier environment.Tier, slug string) (*gate.Gate, bool, error) {
	service, err := c.Secrets()
	if err != nil {
		return nil, false, err
	}
	name, err := c.GateSecret(tier, slug)
	if err != nil {
		return nil, false, err
	}
	current, found, err := c.ReadSecretVersion(ctx, service, name, latestSecretVersion)
	if err != nil || !found {
		return nil, false, err
	}
	held, err := gate.DecodeMounted(current)
	return held, true, err
}

func DeleteGate(ctx context.Context, c *gcloud.Clients, tier environment.Tier, slug string) error {
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	name, err := c.GateSecret(tier, slug)
	if err != nil {
		return err
	}
	return c.DeleteSecret(ctx, service, name)
}

func GateGrants(c *gcloud.Clients, spec provider.StackSpec, member string) ([]grants.Grant, error) {
	if !mountsGateOnAnyService(spec) {
		return nil, nil
	}
	name, err := c.GateSecret(spec.Ref.Tier, spec.Ref.Project)
	if err != nil {
		return nil, err
	}
	return []grants.Grant{grants.On(grants.Secret, name, GateReaderRole, member)}, nil
}

func mountsGateOnAnyService(spec provider.StackSpec) bool {
	if !mountsGate(spec) {
		return false
	}
	return spec.App.Compute == provider.ComputeContainer || len(spec.App.Functions) == 0 ||
		slices.ContainsFunc(spec.App.Functions, func(fn provider.FunctionSpec) bool { return !IgnoresGate(spec, fn) })
}

var gateFrameworks = []string{buildoutput.FrameworkNode, buildoutput.FrameworkNext, buildoutput.FrameworkSvelteKit}

type GatedServing struct {
	env    map[string]string
	mounts []run.SecretMount
}

func (g GatedServing) hasGate() bool { return len(g.env) > 0 }

func ProvisionGate(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, inEnv bool) (GatedServing, error) {
	if !mountsGate(spec) {
		return GatedServing{}, nil
	}
	tier, slug := spec.Ref.Tier, spec.Ref.Project
	held, found, err := ReadGate(ctx, c, tier, slug)
	switch {
	case err != nil:
		return GatedServing{}, fmt.Errorf("read the %s gate of %s from the secret its services mount: %w", tier, slug, err)
	case !found:
		return GatedServing{}, fmt.Errorf("the secret the %s services of %s mount does not exist, so a service mounting it would answer every request with 503: deploy again, which writes the secret before any service mounts it", tier, slug)
	case held == nil && IsGated(spec):
		return GatedServing{}, fmt.Errorf("the secret the %s services of %s mount holds no gate, so they would serve this protected deploy to anyone: deploy again", tier, slug)
	}
	if inEnv {
		// HACK: floci-gcp runs no secret volume, so an emulated service takes its gate, or the withdrawn marker, from its environment and a change of gate rolls a revision; drop gateInEnv once floci-gcp mounts secret volumes (#1605).
		encoded, err := gate.EncodeMounted(held)
		if err != nil {
			return GatedServing{}, err
		}
		return GatedServing{env: map[string]string{gate.MountedInlineVar: string(encoded)}}, nil
	}
	name, err := c.GateSecret(tier, slug)
	if err != nil {
		return GatedServing{}, err
	}
	mount := gateMount(name)
	return GatedServing{
		env:    map[string]string{gate.MountedFileVar: mount.Dir + "/" + mount.File},
		mounts: []run.SecretMount{mount},
	}, nil
}

func ApplyGate(ctx context.Context, c *gcloud.Clients, g GatedServing, what string, served *run.Serving) error {
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
	values, err := run.MergeValues(what, gateValues, served.Env, env)
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

func IsGated(spec provider.StackSpec) bool {
	return spec.App.Gate != nil
}

func IgnoresGate(spec provider.StackSpec, fn provider.FunctionSpec) bool {
	return mountsGate(spec) && !slices.Contains(gateFrameworks, fn.Framework.Name)
}

func RefuseGateOnFunction(spec provider.StackSpec, fn provider.FunctionSpec) error {
	if !IsGated(spec) || !IgnoresGate(spec, fn) {
		return nil
	}
	tier := spec.Ref.Tier
	return refusal.Refuse(refusal.CodeUnsupported,
		"%s is a %s function on Cloud Run with nothing in front of it, and a function of any framework but %v ignores the gate, so it would serve this protected %s deploy to anyone: "+
			"front the %s tier with an edge that gates it, or turn protection.%s off",
		fn.Name, fn.Framework.Name, gateFrameworks, tier, tier, tier)
}

type GateSecrets struct {
	OpenClients func(context.Context) (*gcloud.Clients, error)
}

func (s GateSecrets) Publish(ctx context.Context, tier environment.Tier, slug string, held *gate.Gate) error {
	c, err := s.OpenClients(ctx)
	if err != nil {
		return err
	}
	return WriteGate(ctx, c, tier, slug, held)
}

func (s GateSecrets) Remove(ctx context.Context, tier environment.Tier, slug string) error {
	c, err := s.OpenClients(ctx)
	if err != nil {
		return err
	}
	return DeleteGate(ctx, c, tier, slug)
}
