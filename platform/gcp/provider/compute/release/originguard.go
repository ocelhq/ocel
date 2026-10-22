package release

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/originsecret"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const OriginGuardValues = "the origin secret its edge presents"

func IsReachedByEdgeCode(spec provider.StackSpec) bool {
	app := spec.App
	return run.FactsOf(spec.Edge).RunsCode && app != nil && (app.Compute == provider.ComputeContainer || len(app.Functions) > 0)
}

func (p *Release) readOriginSecret(ctx context.Context, spec provider.StackSpec) (originsecret.Secret, error) {
	if !IsReachedByEdgeCode(spec) {
		return originsecret.Secret{}, nil
	}
	c, err := p.OpenClients(ctx)
	if err != nil {
		return originsecret.Secret{}, err
	}
	return cloudflareorigin.RequireOriginSecret(ctx, c, spec.Ref.Tier, spec.Edge.Kind())
}

func ReadAddress(spec provider.StackSpec, what string, ran run.Release) (string, error) {
	if !IsReachedByEdgeCode(spec) {
		return ran.Url, nil
	}
	if ran.TagURL == "" {
		return "", fmt.Errorf("%s is served behind the %s worker, which reaches the revision this release tags, and Cloud Run reported no URL for its tag", what, spec.Edge.Kind())
	}
	return ran.TagURL, nil
}

func NewOriginGuardEnv(secret originsecret.Secret) map[string]string {
	if !secret.Present() {
		return nil
	}
	env := map[string]string{edge.OriginFrontedVar: "1", edge.OriginSecretVar: secret.Current}
	if secret.Rotating() {
		env[edge.OriginSecretPreviousVar] = secret.Previous
	}
	return env
}
