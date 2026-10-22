package bootstrap

import (
	"context"
	"fmt"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const ReasonRingKept = "Google never deletes a key ring"

var keyRingKind = KindOps{
	noun:      "KMS key ring",
	terraform: cost.KMSKeyRingType,
	survey: func(ctx context.Context, b Engine, _ Survey, _ Resource) (Presence, error) {
		return presenceFrom(b.keyRingExists(ctx))
	},
	Make: func(ctx context.Context, b Engine, _ Survey, _ Resource) error {
		return b.makeKeyRing(ctx)
	},
	removal: func(_ Survey, target Resource) Removal { return kept(target, ReasonRingKept) },
}

func (b Engine) makeKeyRing(ctx context.Context) error {
	client, err := b.Clients.KMS()
	if err != nil {
		return err
	}
	_, err = googleretry.Called(ctx, func() (*kmspb.KeyRing, error) {
		return client.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{
			Parent:    fmt.Sprintf("projects/%s/locations/%s", b.Clients.Project(), b.Clients.Region),
			KeyRingId: b.Clients.KeyRing(),
			KeyRing:   &kmspb.KeyRing{},
		})
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("create the %s key ring: %w", b.Clients.KeyRing(), err)
	}
	return nil
}

func (b Engine) keyRingExists(ctx context.Context) (bool, error) {
	client, err := b.Clients.KMS()
	if err != nil {
		return false, err
	}
	if _, err := googleretry.Called(ctx, func() (*kmspb.KeyRing, error) {
		return client.GetKeyRing(ctx, &kmspb.GetKeyRingRequest{Name: gcloud.KeyRingPath(b.Clients)})
	}); err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, fmt.Errorf("read the %s key ring: %w", b.Clients.KeyRing(), err)
	}
	return true, nil
}
