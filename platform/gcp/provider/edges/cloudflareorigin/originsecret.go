package cloudflareorigin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/originsecret"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func ReadOriginSecret(ctx context.Context, c *gcloud.Clients, tier environment.Tier, kind edge.Kind) (originsecret.Secret, bool, error) {
	service, err := c.Secrets()
	if err != nil {
		return originsecret.Secret{}, false, err
	}
	return readOriginSecret(ctx, c, service, c.OriginSecret(tier, kind))
}

func RequireOriginSecret(ctx context.Context, c *gcloud.Clients, tier environment.Tier, kind edge.Kind) (originsecret.Secret, error) {
	secret, found, err := ReadOriginSecret(ctx, c, tier, kind)
	if err != nil {
		return originsecret.Secret{}, err
	}
	if !found {
		return originsecret.Secret{}, refusal.Refuse(refusal.CodeNotReady,
			"the %s edge reaches each release of tier %s by presenting the tier's origin secret, and the bootstrap stored none in %s: run `%s` again",
			kind, tier, c.OriginSecret(tier, kind), provider.BootstrapCommand(tier))
	}
	return secret, nil
}

func EnsureOriginSecret(ctx context.Context, c *gcloud.Clients, tier environment.Tier, kind edge.Kind, now time.Time, runProgress progress.Log) error {
	say := gcloud.EnsureProgress(runProgress)
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	name := c.OriginSecret(tier, kind)
	secret, found, err := readOriginSecret(ctx, c, service, name)
	if err != nil {
		return err
	}
	if !found {
		return mintOriginSecret(ctx, c, service, tier, name, now, say)
	}
	renewed, renewal, err := secret.Renew(now)
	if err != nil {
		return err
	}
	if !renewal.Changed() {
		say.Debug(fmt.Sprintf("Reused the existing %s origin secret", tier))
		return nil
	}
	added, err := writeOriginSecret(ctx, c, service, name, renewed)
	if err != nil {
		return err
	}
	if err := c.DestroyVersionsBefore(ctx, service, name, added); err != nil {
		return err
	}
	if renewal.Retired {
		say.Warn(fmt.Sprintf("Retired the %s origin secret a rotation replaced: a release not re-deployed since no longer answers its edge", tier))
	}
	if renewal.Rotated {
		say.Warn(fmt.Sprintf("Rotated the %s origin secret, which was older than %d days: re-deploy each project in the tier so its releases accept the new one; the first bootstrap after %d days retires the old one",
			tier, int(originsecret.MaxAge.Hours()/24), int(originsecret.Grace.Hours()/24)))
	}
	return nil
}

func mintOriginSecret(ctx context.Context, c *gcloud.Clients, service *secretmanager.Service, tier environment.Tier, name string, now time.Time, say progress.Log) error {
	if err := c.EnsureSecret(ctx, service, tier, name); err != nil {
		return err
	}
	if _, found, err := readOriginSecret(ctx, c, service, name); err != nil || found {
		return err
	}
	minted, err := originsecret.New(now)
	if err != nil {
		return err
	}
	if _, err := writeOriginSecret(ctx, c, service, name, minted); err != nil {
		return err
	}
	say.Say(fmt.Sprintf("Minted a new %s origin secret", tier))
	return nil
}

func readOriginSecret(ctx context.Context, c *gcloud.Clients, service *secretmanager.Service, name string) (originsecret.Secret, bool, error) {
	payload, found, err := c.ReadSecretVersion(ctx, service, name, latestVersion)
	if err != nil || !found {
		return originsecret.Secret{}, false, err
	}
	secret, err := originsecret.Parse(payload)
	if err != nil {
		return originsecret.Secret{}, true, refusal.Refuse(refusal.CodeInvalid,
			"the %s secret contains something other than the origin secret bootstrap writes. Delete the secret and re-run bootstrap to mint a fresh one, then re-deploy every project in the tier",
			name)
	}
	return secret, true, nil
}

func writeOriginSecret(ctx context.Context, c *gcloud.Clients, service *secretmanager.Service, name string, secret originsecret.Secret) (*secretmanager.SecretVersion, error) {
	payload, err := json.Marshal(secret)
	if err != nil {
		return nil, fmt.Errorf("marshal the %s secret: %w", name, err)
	}
	return c.AddSecretVersion(ctx, service, name, payload)
}
