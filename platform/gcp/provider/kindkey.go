package gcp

import (
	"context"
	"errors"
	"fmt"
	"path"
	"time"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	reasonKeyScheduled = "Cloud KMS destroys each version once the key's scheduled-destruction period passes, 30 days unless the key sets another"
	reasonKeyReplaced  = "its primary version is destroyed or scheduled for destruction, and a new primary version replaces it: what the tier sealed under the old one opens again only if that version is restored"
)

var keyKind = kind{
	noun:      "KMS key",
	terraform: tfKMSCryptoKey,
	survey: func(ctx context.Context, b bootstrap, _ environment.Tier, target item) (presence, error) {
		key, err := b.readKey(ctx, target.Name)
		return keyPresenceOf(key), err
	},
	make: func(ctx context.Context, b bootstrap, _ survey, target item) error {
		return b.makeKey(ctx, target.Name)
	},
	removal: func(read survey, target item) removal {
		taking := deleting(read, target)
		taking.reason, taking.item.Slow = reasonKeyScheduled, true
		return taking
	},
	take: func(ctx context.Context, b bootstrap, _ survey, target item) (time.Duration, error) {
		return b.takeKey(ctx, target.Name)
	},
	taken: func(r removal) string {
		if r.destroyAfter == 0 {
			return "Scheduled every version of " + r.item.phrase() + " for destruction, which Cloud KMS carries out after " + formatPeriod(defaultKeyDestroyAfter) + ", its default"
		}
		return "Scheduled every version of " + r.item.phrase() + " for destruction, which Cloud KMS carries out after " + formatPeriod(r.destroyAfter) + ", the key's own period"
	},
}

func (b bootstrap) makeKey(ctx context.Context, name string) error {
	client, err := b.clients.KMS()
	if err != nil {
		return err
	}
	_, err = googleretry.Called(ctx, func() (*kmspb.CryptoKey, error) {
		return client.CreateCryptoKey(ctx, &kmspb.CreateCryptoKeyRequest{
			Parent:      gcloud.KeyRingPath(b.clients),
			CryptoKeyId: name,
			CryptoKey:   &kmspb.CryptoKey{Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT},
		})
	})
	if status.Code(err) != codes.AlreadyExists {
		if err != nil {
			return fmt.Errorf("create the %s key: %w", name, err)
		}
		return nil
	}
	minted, err := googleretry.Called(ctx, func() (*kmspb.CryptoKeyVersion, error) {
		return client.CreateCryptoKeyVersion(ctx, &kmspb.CreateCryptoKeyVersionRequest{
			Parent:           gcloud.KeyPath(b.clients, name),
			CryptoKeyVersion: &kmspb.CryptoKeyVersion{},
		})
	})
	if err != nil {
		return fmt.Errorf("give the %s key a version to seal under again: %w", name, err)
	}
	if _, err := gcloud.Until(ctx, fmt.Sprintf("the material the %s key seals under", name),
		func() (*kmspb.CryptoKeyVersion, error) {
			return googleretry.Called(ctx, func() (*kmspb.CryptoKeyVersion, error) {
				return client.GetCryptoKeyVersion(ctx, &kmspb.GetCryptoKeyVersionRequest{Name: minted.GetName()})
			})
		},
		func(version *kmspb.CryptoKeyVersion) bool {
			return version.GetState() == kmspb.CryptoKeyVersion_ENABLED
		}); err != nil {
		return err
	}
	if _, err := googleretry.Called(ctx, func() (*kmspb.CryptoKey, error) {
		return client.UpdateCryptoKeyPrimaryVersion(ctx, &kmspb.UpdateCryptoKeyPrimaryVersionRequest{
			Name:               gcloud.KeyPath(b.clients, name),
			CryptoKeyVersionId: path.Base(minted.GetName()),
		})
	}); err != nil {
		return fmt.Errorf("point the %s key at the version it seals under now: %w", name, err)
	}
	return nil
}

func (b bootstrap) takeKey(ctx context.Context, name string) (destroyAfter time.Duration, err error) {
	client, err := b.clients.KMS()
	if err != nil {
		return 0, err
	}
	key, err := b.readKey(ctx, name)
	if err != nil {
		return 0, err
	}
	if key == nil {
		return 0, nil
	}
	versions := client.ListCryptoKeyVersions(ctx, &kmspb.ListCryptoKeyVersionsRequest{Parent: gcloud.KeyPath(b.clients, name)})
	var listed []*kmspb.CryptoKeyVersion
	for {
		version, err := versions.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("list the versions the %s key has: %w", name, err)
		}
		listed = append(listed, version)
	}
	for _, version := range destroyable(listed) {
		if _, err := googleretry.Called(ctx, func() (*kmspb.CryptoKeyVersion, error) {
			return client.DestroyCryptoKeyVersion(ctx, &kmspb.DestroyCryptoKeyVersionRequest{Name: version})
		}); err != nil {
			return 0, fmt.Errorf("schedule the %s key's material for destruction: %w", name, err)
		}
	}
	return key.GetDestroyScheduledDuration().AsDuration(), nil
}

func destroyable(versions []*kmspb.CryptoKeyVersion) []string {
	var named []string
	for _, version := range versions {
		switch version.GetState() {
		case kmspb.CryptoKeyVersion_ENABLED, kmspb.CryptoKeyVersion_DISABLED:
			named = append(named, version.GetName())
		}
	}
	return named
}

const defaultKeyDestroyAfter = 30 * 24 * time.Hour

func formatPeriod(d time.Duration) string {
	const day = 24 * time.Hour
	switch {
	case d == day:
		return "24 hours"
	case d%day == 0:
		return fmt.Sprintf("%d days", d/day)
	case d%time.Hour == 0:
		return fmt.Sprintf("%d hours", d/time.Hour)
	default:
		return d.String()
	}
}

func keyPresenceOf(key *kmspb.CryptoKey) presence {
	switch {
	case key == nil:
		return presence{}
	case usable(key.GetPrimary()):
		return presence{present: true}
	default:
		return presence{replaces: reasonKeyReplaced}
	}
}

func (b bootstrap) readKey(ctx context.Context, name string) (*kmspb.CryptoKey, error) {
	client, err := b.clients.KMS()
	if err != nil {
		return nil, err
	}
	key, err := googleretry.Called(ctx, func() (*kmspb.CryptoKey, error) {
		return client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: gcloud.KeyPath(b.clients, name)})
	})
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the %s key: %w", name, err)
	}
	return key, nil
}

func usable(version *kmspb.CryptoKeyVersion) bool {
	switch version.GetState() {
	case kmspb.CryptoKeyVersion_DESTROY_SCHEDULED, kmspb.CryptoKeyVersion_DESTROYED:
		return false
	default:
		return version != nil
	}
}
