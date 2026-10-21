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
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	reasonRunsDatabaseRemoved   = "it keeps the project's runs and tag records, and the project leaves the tier"
	reasonValuesDatabaseRemoved = "it keeps the project's values, which this removal purges, and the project leaves the tier"
	reasonProjectKeyRemoved     = "the project's values were sealed under it; Cloud KMS destroys each version once the key's scheduled-destruction period passes"
)

type projectStores struct {
	databaseExists func(ctx context.Context, id string) (bool, error)
	keyExists      func(ctx context.Context, name string) (bool, error)
	takeDatabase   func(ctx context.Context, tier environment.Tier, id string) error
	takeKey        func(ctx context.Context, name string) error
}

func storesOf(b bootstrap.Engine) projectStores {
	return projectStores{
		databaseExists: func(ctx context.Context, id string) (bool, error) {
			service, err := b.Clients.Databases()
			if err != nil {
				return false, err
			}
			_, err = googleretry.Attempted(ctx, service.Projects.Databases.Get(bootstrap.DatabasePath(b.Clients, id)).Context(ctx).Do)
			if gcloud.Absent(err) {
				return false, nil
			}
			if err != nil {
				return false, fmt.Errorf("read the %q Firestore database: %w", id, err)
			}
			return true, nil
		},
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
		takeKey: func(ctx context.Context, name string) error {
			if on, err := bootstrap.Keys.HasAPIsOn(ctx, b); err != nil || !on {
				return err
			}
			_, err := b.TakeKey(ctx, name)
			return err
		},
	}
}

func PlanRemoveProject(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string) ([]provider.Change, error) {
	return planRemoveProject(ctx, b, storesOf(b), tier, project)
}

func RemoveProject(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string, log progress.Log) error {
	return removeProject(ctx, b, storesOf(b), tier, project, log)
}

func planRemoveProject(ctx context.Context, b bootstrap.Engine, stores projectStores, tier environment.Tier, project string) ([]provider.Change, error) {
	var changes []provider.Change
	recorded, err := records.ReadOrEmpty(ctx, b.Records, writableDatabaseKey(tier, project))
	if err != nil {
		return nil, err
	}
	if len(recorded.Value) > 0 {
		changes = append(changes, deleted(bootstrap.KindDatabase, b.Clients.ProjectWritableDatabase(tier, project), reasonRunsDatabaseRemoved))
	}
	if b.Clients.Emulated() {
		return changes, nil
	}
	values := b.Clients.ProjectDatabase(tier, project)
	if held, err := stores.databaseExists(ctx, values); err != nil {
		return nil, err
	} else if held {
		changes = append(changes, deleted(bootstrap.KindDatabase, values, reasonValuesDatabaseRemoved))
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
	if err := takeWritableDatabase(ctx, b, stores, tier, project, log); err != nil {
		return err
	}
	if b.Clients.Emulated() {
		return nil
	}
	values := b.Clients.ProjectDatabase(tier, project)
	gcloud.EnsureProgress(log).Say(fmt.Sprintf("Deleting Firestore database %s, which kept the values of %s in %s", values, project, tier))
	if err := stores.takeDatabase(ctx, tier, values); err != nil {
		return gcloud.ExplainMissingGrant(err, b.Clients.DeployCredential(tier).Tiered(credential.DatabasesRole).String())
	}
	key := ports.ProjectKey(tier, project)
	gcloud.EnsureProgress(log).Say(fmt.Sprintf("Scheduling every version of the %s key, which sealed the values of %s in %s, for destruction", key, project, tier))
	return stores.takeKey(ctx, key)
}

func deleted(kind bootstrap.Kind, name, reason string) provider.Change {
	return provider.Change{Kind: string(kind), Name: name, Action: provider.ActionDelete, Reason: reason}
}
