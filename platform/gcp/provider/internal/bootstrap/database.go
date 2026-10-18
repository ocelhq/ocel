package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	firestoreadmin "google.golang.org/api/firestore/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	reasonEmulated    = "emulator serves one implicit database"
	reasonSharedDB    = "the %s bootstrap is still installed and shares it, so only the records this tier keeps in it are deleted"
	reasonUnprotected = "it exists with delete protection off, and one call would take every record it stores with it"
)

const (
	NativeFirestore = "FIRESTORE_NATIVE"
	protectionOn    = "DELETE_PROTECTION_ENABLED"
	protectionOff   = "DELETE_PROTECTION_DISABLED"
)

var databaseKind = KindOps{
	noun:          "Firestore database",
	terraform:     cost.FirestoreDatabaseType,
	emulatorKeeps: reasonEmulated,
	survey: func(ctx context.Context, b Engine, _ environment.Tier, target Resource) (Presence, error) {
		return b.databasePresence(ctx, target.Name)
	},
	Make: func(ctx context.Context, b Engine, read Survey, target Resource) error {
		return b.MakeDatabase(ctx, read, target.Name)
	},
	mend: func(ctx context.Context, b Engine, _ Survey, target Resource) error {
		return b.protectDatabase(ctx, target.Name)
	},
	removal: func(read Survey, target Resource) Removal {
		switch {
		case target.Shared && read.Sibling:
			return kept(target, fmt.Sprintf(reasonSharedDB, read.Tier.Sibling()))
		case read.Emulated:
			return kept(target, reasonEmulated)
		}
		return Removal{Resource: target, Action: provider.ActionDisableThenDelete}
	},
	leave: func(ctx context.Context, b Engine, read Survey, taking Removal) error {
		if !taking.Resource.Shared {
			return nil
		}
		if taking.Action == provider.ActionKeep {
			if err := b.takeKeyValues(ctx, read.Tier); err != nil {
				return err
			}
		}
		return b.TakeRecords(ctx, b, read.Tier)
	},
	Take: func(ctx context.Context, b Engine, _ Survey, target Resource) (time.Duration, error) {
		return 0, b.TakeDatabase(ctx, target.Name)
	},
}

func (b Engine) MakeDatabase(ctx context.Context, read Survey, id string) error {
	service, err := b.Clients.Databases()
	if err != nil {
		return err
	}
	operation, err := googleretry.Attempted(ctx, service.Projects.Databases.Create("projects/"+read.Project, &firestoreadmin.GoogleFirestoreAdminV1Database{
		LocationId:            read.Region,
		Type:                  NativeFirestore,
		DeleteProtectionState: protectionOn,
	}).DatabaseId(id).Context(ctx).Do)
	if gcloud.Taken(err) {
		return b.databaseServesThisRegion(ctx, read, id)
	}
	if err != nil {
		return fmt.Errorf("create the %q Firestore database: %w", id, err)
	}
	return b.Awaited(ctx, fmt.Sprintf("creating the %q Firestore database", id), operation)
}

func (b Engine) databaseServesThisRegion(ctx context.Context, read Survey, id string) error {
	service, err := b.Clients.Databases()
	if err != nil {
		return err
	}
	database, err := googleretry.Attempted(ctx, service.Projects.Databases.Get(DatabasePath(b.Clients, id)).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("read the %q Firestore database that already exists: %w", id, err)
	}
	if database.LocationId == read.Region {
		return b.protectDatabase(ctx, id)
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"project %s already has the %q Firestore database in %s and Firestore never moves one, "+
			"so the records this bootstrap writes would sit a continent away from the buckets and keys it names %s for.\n"+
			"Bootstrap this project in %s, or deploy into a project with no %q database",
		read.Project, id, database.LocationId, read.Region, database.LocationId, id)
}

func (b Engine) protectDatabase(ctx context.Context, id string) error {
	service, err := b.Clients.Databases()
	if err != nil {
		return err
	}
	operation, err := googleretry.Attempted(ctx, service.Projects.Databases.Patch(DatabasePath(b.Clients, id),
		&firestoreadmin.GoogleFirestoreAdminV1Database{DeleteProtectionState: protectionOn}).
		UpdateMask("deleteProtectionState").Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("turn on delete protection for the %q Firestore database: %w", id, err)
	}
	return b.Awaited(ctx, fmt.Sprintf("turning on delete protection for the %q Firestore database", id), operation)
}

func (b Engine) Awaited(ctx context.Context, doing string, operation *firestoreadmin.GoogleLongrunningOperation) error {
	if operation == nil || operation.Done || operation.Name == "" {
		return operationFailed(doing, operation)
	}
	service, err := b.Clients.Databases()
	if err != nil {
		return err
	}
	finished, err := gcloud.Until(ctx, doing, func() (*firestoreadmin.GoogleLongrunningOperation, error) {
		return googleretry.Attempted(ctx, service.Projects.Databases.Operations.Get(operation.Name).Context(ctx).Do)
	}, func(polled *firestoreadmin.GoogleLongrunningOperation) bool { return polled.Done })
	if err != nil {
		return fmt.Errorf("wait for %s: %w", doing, err)
	}
	return operationFailed(doing, finished)
}

func operationFailed(doing string, operation *firestoreadmin.GoogleLongrunningOperation) error {
	if operation == nil || operation.Error == nil {
		return nil
	}
	return fmt.Errorf("%s: %s", doing, operation.Error.Message)
}

func (b Engine) TakeDatabase(ctx context.Context, id string) error {
	service, err := b.Clients.Databases()
	if err != nil {
		return err
	}
	path := DatabasePath(b.Clients, id)
	lifting, err := googleretry.Attempted(ctx, service.Projects.Databases.Patch(path, &firestoreadmin.GoogleFirestoreAdminV1Database{
		DeleteProtectionState: protectionOff,
	}).UpdateMask("deleteProtectionState").Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lift delete protection from the %q Firestore database: %w", id, err)
	}
	lifted := fmt.Sprintf("lifting delete protection from the %q Firestore database", id)
	if err := b.Awaited(ctx, lifted, lifting); err != nil {
		return err
	}

	deleting, err := googleretry.Attempted(ctx, service.Projects.Databases.Delete(path).Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete the %q Firestore database: %w", id, err)
	}
	return b.Awaited(ctx, fmt.Sprintf("deleting the %q Firestore database", id), deleting)
}

func (b Engine) takeKeyValues(ctx context.Context, tier environment.Tier) error {
	client, err := b.Clients.Firestore()
	if err != nil {
		return err
	}
	documents := ports.OpenTierCollection(client, tier).Select().Documents(ctx)
	defer documents.Stop()
	for {
		kept, err := documents.Next()
		if errors.Is(err, iterator.Done) || status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return fmt.Errorf("list the entries the %s tier keeps: %w", tier, err)
		}
		if err := googleretry.Done(ctx, func() error {
			_, err := kept.Ref.Delete(ctx)
			return err
		}); err != nil {
			return fmt.Errorf("delete the %s entry %s: %w", tier, kept.Ref.ID, err)
		}
	}
}

func (b Engine) databasePresence(ctx context.Context, id string) (Presence, error) {
	if b.Clients.Emulated() {
		return Presence{Present: true}, nil
	}
	service, err := b.Clients.Databases()
	if err != nil {
		return Presence{}, err
	}
	database, err := googleretry.Attempted(ctx, service.Projects.Databases.Get(DatabasePath(b.Clients, id)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return Presence{}, nil
	}
	if err != nil {
		return Presence{}, fmt.Errorf("read the %q Firestore database: %w", id, err)
	}
	if database.DeleteProtectionState != protectionOn {
		return Presence{Present: true, Mends: reasonUnprotected}, nil
	}
	return Presence{Present: true}, nil
}

func DatabasePath(c *gcloud.Clients, id string) string {
	return "projects/" + c.Project() + "/databases/" + id
}
