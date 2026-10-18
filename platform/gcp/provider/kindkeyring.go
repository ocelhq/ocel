package gcp

import (
	"context"
	"fmt"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const reasonRingKept = "Google never deletes a key ring, so this one outlives every bootstrap that used it"

var keyRingKind = kind{
	noun:      "KMS key ring",
	terraform: tfKMSKeyRing,
	survey: func(ctx context.Context, b bootstrap, _ environment.Tier, _ item) (presence, error) {
		return presenceFrom(b.keyRingExists(ctx))
	},
	make: func(ctx context.Context, b bootstrap, _ survey, _ item) error {
		return b.makeKeyRing(ctx)
	},
	removal: func(_ survey, target item) removal { return kept(target, reasonRingKept) },
}

func (b bootstrap) makeKeyRing(ctx context.Context) error {
	client, err := b.clients.KMS()
	if err != nil {
		return err
	}
	_, err = googleretry.Called(ctx, func() (*kmspb.KeyRing, error) {
		return client.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{
			Parent:    fmt.Sprintf("projects/%s/locations/%s", b.clients.Project(), b.clients.Region),
			KeyRingId: b.clients.KeyRing(),
			KeyRing:   &kmspb.KeyRing{},
		})
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("create the %s key ring: %w", b.clients.KeyRing(), err)
	}
	return nil
}

func (b bootstrap) keyRingExists(ctx context.Context) (bool, error) {
	client, err := b.clients.KMS()
	if err != nil {
		return false, err
	}
	if _, err := googleretry.Called(ctx, func() (*kmspb.KeyRing, error) {
		return client.GetKeyRing(ctx, &kmspb.GetKeyRingRequest{Name: gcloud.KeyRingPath(b.clients)})
	}); err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, fmt.Errorf("read the %s key ring: %w", b.clients.KeyRing(), err)
	}
	return true, nil
}
