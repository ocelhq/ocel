package gcp

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"

	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
)

const (
	previewGateDir        = "/var/run/ocel/preview-gate"
	previewGateFile       = "gate"
	previewGateVolume     = "preview-gate"
	previewGateReaderRole = "roles/secretmanager.secretAccessor"
	latestSecretVersion   = "latest"
)

func previewGateMount(secret string) secretMount {
	return secretMount{name: previewGateVolume, secret: secret, dir: previewGateDir, file: previewGateFile}
}

func (c *clients) writePreviewGate(ctx context.Context, slug string, gate *previewgate.Gate) error {
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	name := c.PreviewGateSecret(slug)
	written, err := previewgate.EncodeMounted(gate)
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

func (c *clients) readPreviewGate(ctx context.Context, slug string) (*previewgate.Gate, bool, error) {
	service, err := c.Secrets()
	if err != nil {
		return nil, false, err
	}
	current, found, err := c.readSecretVersion(ctx, service, c.PreviewGateSecret(slug), latestSecretVersion)
	if err != nil || !found {
		return nil, false, err
	}
	gate, err := previewgate.DecodeMounted(current)
	return gate, true, err
}

func (c *clients) deletePreviewGate(ctx context.Context, slug string) error {
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	return c.deleteSecret(ctx, service, c.PreviewGateSecret(slug))
}

func (c *clients) bindPreviewGateReader(ctx context.Context, slug, member string, granting bool) (bool, error) {
	service, err := c.Secrets()
	if err != nil {
		return false, err
	}
	path := secretPath(c.project, c.PreviewGateSecret(slug))
	var refused error
	for attempt := range bindAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return false, ctx.Err()
		}
		policy, err := attempted(ctx, service.Projects.Secrets.GetIamPolicy(path).Context(ctx).Do)
		if absent(err) && !granting {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read who may read the preview gate of %s: %w", slug, err)
		}
		bindings, changed := boundSecretMember(policy.Bindings, previewGateReaderRole, member, granting)
		if !changed {
			return false, nil
		}
		policy.Bindings = bindings
		_, refused = attempted(ctx, service.Projects.Secrets.SetIamPolicy(path,
			&secretmanager.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do)
		if refused == nil {
			return true, nil
		}
		if !stale(refused) {
			break
		}
	}
	return false, fmt.Errorf("update who may read the preview gate of %s: %w", slug, refused)
}

func (c *clients) grantPreviewGateRead(ctx context.Context, account, slug string) error {
	return untilVisible(ctx, func() error {
		_, err := c.bindPreviewGateReader(ctx, slug, "serviceAccount:"+account, true)
		return err
	})
}

var previewGateFrameworks = []string{buildoutput.FrameworkNode, buildoutput.FrameworkNext, buildoutput.FrameworkSvelteKit}

type gatedServing struct {
	env    map[string]string
	mounts []secretMount
}

func (g gatedServing) hasGate() bool { return len(g.env) > 0 }

func (p *Provider) provisionPreviewGate(ctx context.Context, c *clients, spec provider.StackSpec, account string) (gatedServing, error) {
	if !mountsPreviewGate(spec) {
		return gatedServing{}, nil
	}
	slug := spec.Ref.Project
	gate, found, err := c.readPreviewGate(ctx, slug)
	switch {
	case err != nil:
		return gatedServing{}, fmt.Errorf("read the preview gate of %s from the secret its services mount: %w", slug, err)
	case !found:
		return gatedServing{}, fmt.Errorf("the secret the previews of %s mount does not exist, so a service mounting it would answer every request with 503: deploy the preview again, which writes the secret before any service mounts it", slug)
	case gate == nil && isGatedPreview(spec):
		return gatedServing{}, fmt.Errorf("the secret the previews of %s mount holds no gate, so its services would serve this protected preview to anyone: deploy the preview again", slug)
	}
	if err := c.grantPreviewGateRead(ctx, account, slug); err != nil {
		return gatedServing{}, fmt.Errorf("let %s read the preview gate of %s: %w", account, slug, err)
	}
	if p.gateInEnv {
		// HACK: floci-gcp runs no secret volume, so an emulated service takes its gate, or the withdrawn marker, from its environment and a change of gate rolls a revision; drop gateInEnv once floci-gcp mounts secret volumes (#1605).
		encoded, err := previewgate.EncodeMounted(gate)
		if err != nil {
			return gatedServing{}, err
		}
		return gatedServing{env: map[string]string{previewgate.MountedInlineVar: string(encoded)}}, nil
	}
	mount := previewGateMount(c.PreviewGateSecret(slug))
	return gatedServing{
		env:    map[string]string{previewgate.MountedFileVar: mount.dir + "/" + mount.file},
		mounts: []secretMount{mount},
	}, nil
}

func (c *clients) applyPreviewGate(ctx context.Context, g gatedServing, what string, served *serving) error {
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
	values, err := mergeValues(what, previewGateValues, served.env, env)
	if err != nil {
		return err
	}
	served.env = values
	served.mounts = append(served.mounts, g.mounts...)
	return nil
}

func mountsPreviewGate(spec provider.StackSpec) bool {
	return spec.Ref.Tier == environment.TierPreview && factsOf(spec.Edge).GatesPreviewsAtOrigin
}

func isGatedPreview(spec provider.StackSpec) bool {
	return spec.App.PreviewGate != nil && spec.Ref.Tier == environment.TierPreview
}

func ignoresPreviewGate(spec provider.StackSpec, fn provider.FunctionSpec) bool {
	return mountsPreviewGate(spec) && !slices.Contains(previewGateFrameworks, fn.Framework.Name)
}

func refuseGateOnFunction(spec provider.StackSpec, fn provider.FunctionSpec) error {
	if !isGatedPreview(spec) || !ignoresPreviewGate(spec, fn) {
		return nil
	}
	return refusal.Refuse(refusal.CodeUnsupported,
		"%s is a %s function on Cloud Run with nothing in front of it, and a function of any framework but %v ignores the preview gate, so it would serve this protected preview to anyone: "+
			"front previews with an edge that gates them, or turn previews.protection off",
		fn.Name, fn.Framework.Name, previewGateFrameworks)
}

type previewGateSecrets struct{ p *Provider }

func (s previewGateSecrets) Publish(ctx context.Context, slug string, gate *previewgate.Gate) error {
	c, err := s.p.openClients(ctx)
	if err != nil {
		return err
	}
	return c.writePreviewGate(ctx, slug, gate)
}

func (s previewGateSecrets) Remove(ctx context.Context, slug string) error {
	c, err := s.p.openClients(ctx)
	if err != nil {
		return err
	}
	return c.deletePreviewGate(ctx, slug)
}
