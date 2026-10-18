package gcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	secretmanager "google.golang.org/api/secretmanager/v1"
)

const reasonPassphraseReplaced = "its latest version is disabled or destroyed, and a new passphrase replaces it: what was written under the old one opens again only if that version is enabled"

var secretKind = kind{
	noun:      "secret",
	terraform: tfSecretManagerSecret,
	parameter: true,
	properties: func(string, item) map[string]any {
		return map[string]any{"replication": map[string]any{"auto": map[string]any{}}}
	},
	survey: func(ctx context.Context, b bootstrap, _ environment.Tier, target item) (presence, error) {
		return b.secretPresence(ctx, target.Name)
	},
	make: func(ctx context.Context, b bootstrap, _ survey, target item) error {
		return b.makeSecret(ctx, target.Name)
	},
	take: func(ctx context.Context, b bootstrap, _ survey, target item) (time.Duration, error) {
		return 0, b.takeSecret(ctx, target.Name)
	},
}

const passphraseBytes = 32

func (b bootstrap) makeSecret(ctx context.Context, name string) error {
	service, err := b.clients.Secrets()
	if err != nil {
		return err
	}
	parent := "projects/" + b.clients.Project()
	_, err = googleretry.Attempted(ctx, service.Projects.Secrets.Create(parent, &secretmanager.Secret{
		Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
	}).SecretId(name).Context(ctx).Do)
	if err != nil && !gcloud.Taken(err) {
		return fmt.Errorf("create the %s secret: %w", name, err)
	}

	latest, err := b.readPassphrase(ctx, name)
	if err != nil {
		return err
	}
	if passphrasePresenceOf(latest).present {
		return nil
	}

	minted := make([]byte, passphraseBytes)
	if _, err := rand.Read(minted); err != nil {
		return fmt.Errorf("mint the passphrase %s stores: %w", name, err)
	}
	_, err = googleretry.Attempted(ctx, service.Projects.Secrets.AddVersion(gcloud.SecretPath(b.clients.Project(), name), &secretmanager.AddSecretVersionRequest{
		Payload: &secretmanager.SecretPayload{Data: base64.StdEncoding.EncodeToString(minted)},
	}).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("write the passphrase into %s: %w", name, err)
	}
	return nil
}

func (b bootstrap) takeSecret(ctx context.Context, name string) error {
	service, err := b.clients.Secrets()
	if err != nil {
		return err
	}
	if _, err := googleretry.Attempted(ctx, service.Projects.Secrets.Delete(gcloud.SecretPath(b.clients.Project(), name)).Context(ctx).Do); err != nil && !gcloud.Absent(err) {
		return fmt.Errorf("delete the %s secret: %w", name, err)
	}
	return nil
}

func (b bootstrap) secretPresence(ctx context.Context, name string) (presence, error) {
	service, err := b.clients.Secrets()
	if err != nil {
		return presence{}, err
	}
	_, err = googleretry.Attempted(ctx, service.Projects.Secrets.Get(gcloud.SecretPath(b.clients.Project(), name)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return presence{}, nil
	}
	if err != nil {
		return presence{}, fmt.Errorf("read the %s secret: %w", name, err)
	}
	latest, err := b.readPassphrase(ctx, name)
	if err != nil {
		return presence{}, err
	}
	return passphrasePresenceOf(latest), nil
}

func passphrasePresenceOf(latest *secretmanager.SecretVersion) presence {
	switch {
	case latest == nil:
		return presence{}
	case latest.State == gcloud.EnabledVersion:
		return presence{present: true}
	default:
		return presence{replaces: reasonPassphraseReplaced}
	}
}

func (b bootstrap) readPassphrase(ctx context.Context, name string) (*secretmanager.SecretVersion, error) {
	service, err := b.clients.Secrets()
	if err != nil {
		return nil, err
	}
	version, err := googleretry.Attempted(ctx, service.Projects.Secrets.Versions.Get(
		gcloud.SecretPath(b.clients.Project(), name)+"/versions/latest").Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read whether the %s secret has a passphrase: %w", name, err)
	}
	return version, nil
}
