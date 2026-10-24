package variables

import (
	"context"
	"fmt"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	reasonDatabaseRemoved   = "it keeps the project's values, runs and tag records, and the project leaves the tier"
	reasonProjectKeyRemoved = "the project's values were sealed under it; Cloud KMS destroys each version once the key's scheduled-destruction period passes"
)

type ProjectRuns func(ctx context.Context, tier environment.Tier, project string) error

type projectStores struct {
	keyExists    func(ctx context.Context, name string) (bool, error)
	takeDatabase func(ctx context.Context, tier environment.Tier, id string) error
	takeRuns     ProjectRuns
	takeKey      func(ctx context.Context, name string) error
}

func storesOf(b bootstrap.Engine, takeRuns ProjectRuns) projectStores {
	return projectStores{
		keyExists: func(ctx context.Context, name string) (bool, error) {
			if on, err := bootstrap.Keys.HasAPIsOn(ctx, b); err != nil || !on {
				return false, err
			}
			client, err := b.Clients.KMS()
			if err != nil {
				return false, err
			}
			_, err = googleretry.Called(ctx, func() (*kmspb.CryptoKey, error) {
				return client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: gcloud.KeyPath(b.Clients, name)})
			})
			if status.Code(err) == codes.NotFound {
				return false, nil
			}
			if err != nil {
				return false, fmt.Errorf("read the %s key: %w", name, err)
			}
			return true, nil
		},
		takeDatabase: b.TakeTierDatabase,
		takeRuns:     takeRuns,
		takeKey: func(ctx context.Context, name string) error {
			if on, err := bootstrap.Keys.HasAPIsOn(ctx, b); err != nil || !on {
				return err
			}
			return b.TakeKey(ctx, name)
		},
	}
}

func PlanRemoveProject(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string) ([]provider.Change, error) {
	return planRemoveProject(ctx, b, storesOf(b, nil), tier, project)
}

func RemoveProject(ctx context.Context, b bootstrap.Engine, takeRuns ProjectRuns, tier environment.Tier, project string, log progress.Log) error {
	return removeProject(ctx, b, storesOf(b, takeRuns), tier, project, log)
}

func planRemoveProject(ctx context.Context, b bootstrap.Engine, stores projectStores, tier environment.Tier, project string) ([]provider.Change, error) {
	var changes []provider.Change
	recorded, found, err := ports.ReadProjectDatabaseRecord(ctx, b.Records, tier, project)
	if err != nil {
		return nil, err
	}
	if found && recorded != b.Clients.SharedDatabase(tier) {
		changes = append(changes, deleted(bootstrap.KindDatabase, recorded, reasonDatabaseRemoved))
	}
	if b.Clients.Emulated() {
		return changes, nil
	}
	key := ports.ProjectKey(tier, project)
	if held, err := stores.keyExists(ctx, key); err != nil {
		return nil, err
	} else if held {
		changes = append(changes, deleted(bootstrap.KindKey, key, reasonProjectKeyRemoved))
	}
	return changes, nil
}

func removeProject(ctx context.Context, b bootstrap.Engine, stores projectStores, tier environment.Tier, project string, log progress.Log) error {
	if err := takeProjectDatabase(ctx, b, stores, tier, project, log); err != nil {
		return err
	}
	if b.Clients.Emulated() {
		return nil
	}
	key := ports.ProjectKey(tier, project)
	gcloud.EnsureProgress(log).Say(fmt.Sprintf("Scheduling every version of the %s key, which sealed the values of %s in %s, for destruction", key, project, tier))
	return stores.takeKey(ctx, key)
}

func deleted(kind bootstrap.Kind, name, reason string) provider.Change {
	return provider.Change{Kind: string(kind), Name: name, Action: provider.ActionDelete, Reason: reason}
}
