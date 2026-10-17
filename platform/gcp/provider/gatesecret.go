package gcp

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"

	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const (
	gateDir             = "/var/run/ocel/gate"
	gateFile            = "gate"
	gateVolume          = "gate"
	gateReaderRole      = "roles/secretmanager.secretAccessor"
	latestSecretVersion = "latest"
)

func gateMount(secret string) secretMount {
	return secretMount{name: gateVolume, secret: secret, dir: gateDir, file: gateFile}
}

func (c *clients) writeGate(ctx context.Context, tier environment.Tier, slug string, held *gate.Gate) error {
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	name := c.GateSecret(tier, slug)
	written, err := gate.EncodeMounted(held)
	if err != nil {
		return err
	}
	current, found, err := c.readSecretVersion(ctx, service, name, latestSecretVersion)
	if err != nil {
		return err
	}
	if !found {
		if err := c.ensureSecret(ctx, service, name); err != nil {
			return err
		}
		if current, found, err = c.readSecretVersion(ctx, service, name, latestSecretVersion); err != nil {
			return err
		}
	}
	if found && bytes.Equal(current, written) {
		return nil
	}
	added, err := c.addSecretVersion(ctx, service, name, written)
	if err != nil {
		return err
	}
	return c.destroyVersionsBeforePrevious(ctx, service, name, added)
}

func (c *clients) readGate(ctx context.Context, tier environment.Tier, slug string) (*gate.Gate, bool, error) {
	service, err := c.Secrets()
	if err != nil {
		return nil, false, err
	}
	current, found, err := c.readSecretVersion(ctx, service, c.GateSecret(tier, slug), latestSecretVersion)
	if err != nil || !found {
		return nil, false, err
	}
	held, err := gate.DecodeMounted(current)
	return held, true, err
}

func (c *clients) deleteGate(ctx context.Context, tier environment.Tier, slug string) error {
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	return c.deleteSecret(ctx, service, c.GateSecret(tier, slug))
}

func (c *clients) bindGateReader(ctx context.Context, tier environment.Tier, slug, member string, granting bool) (bool, error) {
	service, err := c.Secrets()
	if err != nil {
		return false, err
	}
	path := secretPath(c.project, c.GateSecret(tier, slug))
	var refused error
	for attempt := range bindAttempts {
		if attempt > 0 && !googleretry.Waited(ctx, attempt) {
			return false, ctx.Err()
		}
		policy, err := googleretry.Attempted(ctx, service.Projects.Secrets.GetIamPolicy(path).Context(ctx).Do)
		if absent(err) && !granting {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read who may read the %s gate of %s: %w", tier, slug, err)
		}
		bindings, changed := boundSecretMember(policy.Bindings, gateReaderRole, member, granting)
		if !changed {
			return false, nil
		}
		policy.Bindings = bindings
		_, refused = googleretry.Attempted(ctx, service.Projects.Secrets.SetIamPolicy(path,
			&secretmanager.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do)
		if refused == nil {
			return true, nil
		}
		if !stale(refused) {
			break
		}
	}
	return false, fmt.Errorf("update who may read the %s gate of %s: %w", tier, slug, refused)
}

func (c *clients) grantGateRead(ctx context.Context, account string, tier environment.Tier, slug string) error {
	return untilVisible(ctx, func() error {
		_, err := c.bindGateReader(ctx, tier, slug, "serviceAccount:"+account, true)
		return err
	})
}

var gateFrameworks = []string{buildoutput.FrameworkNode, buildoutput.FrameworkNext, buildoutput.FrameworkSvelteKit}

type gatedServing struct {
	env    map[string]string
	mounts []secretMount
}

func (g gatedServing) hasGate() bool { return len(g.env) > 0 }

func (p *Provider) provisionGate(ctx context.Context, c *clients, spec provider.StackSpec, account string) (gatedServing, error) {
	if !mountsGate(spec) {
		return gatedServing{}, nil
	}
	tier, slug := spec.Ref.Tier, spec.Ref.Project
	held, found, err := c.readGate(ctx, tier, slug)
	switch {
	case err != nil:
		return gatedServing{}, fmt.Errorf("read the %s gate of %s from the secret its services mount: %w", tier, slug, err)
	case !found:
		return gatedServing{}, fmt.Errorf("the secret the %s services of %s mount does not exist, so a service mounting it would answer every request with 503: deploy again, which writes the secret before any service mounts it", tier, slug)
	case held == nil && isGated(spec):
		return gatedServing{}, fmt.Errorf("the secret the %s services of %s mount holds no gate, so they would serve this protected deploy to anyone: deploy again", tier, slug)
	}
	if err := c.grantGateRead(ctx, account, tier, slug); err != nil {
		return gatedServing{}, fmt.Errorf("let %s read the %s gate of %s: %w", account, tier, slug, err)
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
		env:    map[string]string{gate.MountedFileVar: mount.dir + "/" + mount.file},
		mounts: []secretMount{mount},
	}, nil
}

func (c *clients) applyGate(ctx context.Context, g gatedServing, what string, served *serving) error {
	if !g.hasGate() {
		return nil
	}
	env := maps.Clone(g.env)
	if served.compute == provider.ComputeContainer {
		token, err := c.readProbeToken(ctx, served.service)
		if err != nil {
			return err
		}
		if token == "" {
			if token, err = newProbeToken(); err != nil {
				return err
			}
		}
		served.probeToken = token
		env[originguard.ProbeTokenVar] = token
	}
	values, err := mergeValues(what, gateValues, served.env, env)
	if err != nil {
		return err
	}
	served.env = values
	served.mounts = append(served.mounts, g.mounts...)
	return nil
}

func mountsGate(spec provider.StackSpec) bool {
	return factsOf(spec.Edge).GatesAtOrigin
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
	return c.writeGate(ctx, tier, slug, held)
}

func (s gateSecrets) Remove(ctx context.Context, tier environment.Tier, slug string) error {
	c, err := s.p.openClients(ctx)
	if err != nil {
		return err
	}
	return c.deleteGate(ctx, tier, slug)
}
