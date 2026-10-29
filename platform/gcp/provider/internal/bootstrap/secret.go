package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	secretmanager "google.golang.org/api/secretmanager/v1"
)

const reasonPassphraseReplaced = "its latest version is disabled or destroyed"

var secretKind = KindOps{
	tagged:           secretTagged,
	taggedAtCreation: true,
	noun:             "secret",
	terraform:        gcloud.SecretManagerSecretType,
	parameter:        true,
	replaceable:      true,
	properties: func(string, Resource) map[string]any {
		return map[string]any{"replication": map[string]any{"auto": map[string]any{}}}
	},
	survey: func(ctx context.Context, b Engine, _ Survey, target Resource) (Presence, error) {
		return b.secretPresence(ctx, target.Name)
	},
	Make: func(ctx context.Context, b Engine, read Survey, target Resource) error {
		return b.makeSecret(ctx, read, target.Name)
	},
	Take: func(ctx context.Context, b Engine, _ Survey, target Resource) error {
		return b.takeSecret(ctx, target.Name)
	},
}

const passphraseBytes = 32

func (b Engine) makeSecret(ctx context.Context, read Survey, name string) error {
	service, err := b.Clients.Secrets()
	if err != nil {
		return err
	}
	if err := b.Clients.EnsureSecret(ctx, service, read.Tier, name); err != nil {
		return err
	}

	latest, err := b.readPassphrase(ctx, name)
	if err != nil {
		return err
	}
	if PassphrasePresenceOf(latest).Present {
		return nil
	}

	minted := make([]byte, passphraseBytes)
	if _, err := rand.Read(minted); err != nil {
		return fmt.Errorf("mint the passphrase %s stores: %w", name, err)
	}
	_, err = googleretry.Attempted(ctx, service.Projects.Secrets.AddVersion(gcloud.SecretPath(b.Clients.Project(), name), &secretmanager.AddSecretVersionRequest{
		Payload: &secretmanager.SecretPayload{Data: base64.StdEncoding.EncodeToString(minted)},
	}).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("write the passphrase into %s: %w", name, err)
	}
	return nil
}

func (b Engine) takeSecret(ctx context.Context, name string) error {
	service, err := b.Clients.Secrets()
	if err != nil {
		return err
	}
	if _, err := googleretry.Attempted(ctx, service.Projects.Secrets.Delete(gcloud.SecretPath(b.Clients.Project(), name)).Context(ctx).Do); err != nil && !gcloud.Absent(err) {
		return fmt.Errorf("delete the %s secret: %w", name, err)
	}
	return nil
}

func (b Engine) secretPresence(ctx context.Context, name string) (Presence, error) {
	service, err := b.Clients.Secrets()
	if err != nil {
		return Presence{}, err
	}
	_, err = googleretry.Attempted(ctx, service.Projects.Secrets.Get(gcloud.SecretPath(b.Clients.Project(), name)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return Presence{}, nil
	}
	if err != nil {
		return Presence{}, fmt.Errorf("read the %s secret: %w", name, err)
	}
	latest, err := b.readPassphrase(ctx, name)
	if err != nil {
		return Presence{}, err
	}
	return PassphrasePresenceOf(latest), nil
}

func PassphrasePresenceOf(latest *secretmanager.SecretVersion) Presence {
	switch {
	case latest == nil:
		return Presence{}
	case latest.State == gcloud.EnabledVersion:
		return Presence{Present: true}
	default:
		return Presence{Replaces: reasonPassphraseReplaced}
	}
}

func (b Engine) readPassphrase(ctx context.Context, name string) (*secretmanager.SecretVersion, error) {
	service, err := b.Clients.Secrets()
	if err != nil {
		return nil, err
	}
	version, err := googleretry.Attempted(ctx, service.Projects.Secrets.Versions.Get(
		gcloud.SecretPath(b.Clients.Project(), name)+"/versions/latest").Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read whether the %s secret has a passphrase: %w", name, err)
	}
	return version, nil
}
