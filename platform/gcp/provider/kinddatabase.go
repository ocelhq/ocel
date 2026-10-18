package gcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	firestoreadmin "google.golang.org/api/firestore/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
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
	nativeFirestore = "FIRESTORE_NATIVE"
	protectionOn    = "DELETE_PROTECTION_ENABLED"
	protectionOff   = "DELETE_PROTECTION_DISABLED"
)

var databaseKind = kind{
	noun:          "Firestore database",
	terraform:     tfFirestoreDatabase,
	emulatorKeeps: reasonEmulated,
	survey: func(ctx context.Context, b bootstrap, _ environment.Tier, target item) (presence, error) {
		return b.databasePresence(ctx, target.Name)
	},
	make: func(ctx context.Context, b bootstrap, read survey, target item) error {
		return b.makeDatabase(ctx, read, target.Name)
	},
	mend: func(ctx context.Context, b bootstrap, _ survey, target item) error {
		return b.protectDatabase(ctx, target.Name)
	},
	removal: func(read survey, target item) removal {
		switch {
		case target.Shared && read.sibling:
			return kept(target, fmt.Sprintf(reasonSharedDB, read.Tier.Sibling()))
		case read.Emulated:
			return kept(target, reasonEmulated)
		}
		return removal{item: target, action: provider.ActionDisableThenDelete}
	},
	leave: func(ctx context.Context, b bootstrap, read survey, taking removal) error {
		if !taking.item.Shared {
			return nil
		}
		if taking.action == provider.ActionKeep {
			if err := b.takeKeyValues(ctx, read.Tier); err != nil {
				return err
			}
		}
		return b.takeProjectRecords(ctx, read.Tier)
	},
	take: func(ctx context.Context, b bootstrap, _ survey, target item) (time.Duration, error) {
		return 0, b.takeDatabase(ctx, target.Name)
	},
}

func (b bootstrap) makeDatabase(ctx context.Context, read survey, id string) error {
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	operation, err := googleretry.Attempted(ctx, service.Projects.Databases.Create("projects/"+read.Project, &firestoreadmin.GoogleFirestoreAdminV1Database{
		LocationId:            read.Region,
		Type:                  nativeFirestore,
		DeleteProtectionState: protectionOn,
	}).DatabaseId(id).Context(ctx).Do)
	if gcloud.Taken(err) {
		return b.databaseServesThisRegion(ctx, read, id)
	}
	if err != nil {
		return fmt.Errorf("create the %q Firestore database: %w", id, err)
	}
	return b.awaited(ctx, fmt.Sprintf("creating the %q Firestore database", id), operation)
}

func (b bootstrap) databaseServesThisRegion(ctx context.Context, read survey, id string) error {
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	database, err := googleretry.Attempted(ctx, service.Projects.Databases.Get(databasePath(b.clients, id)).Context(ctx).Do)
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

func (b bootstrap) protectDatabase(ctx context.Context, id string) error {
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	operation, err := googleretry.Attempted(ctx, service.Projects.Databases.Patch(databasePath(b.clients, id),
		&firestoreadmin.GoogleFirestoreAdminV1Database{DeleteProtectionState: protectionOn}).
		UpdateMask("deleteProtectionState").Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("turn on delete protection for the %q Firestore database: %w", id, err)
	}
	return b.awaited(ctx, fmt.Sprintf("turning on delete protection for the %q Firestore database", id), operation)
}

func (b bootstrap) awaited(ctx context.Context, doing string, operation *firestoreadmin.GoogleLongrunningOperation) error {
	if operation == nil || operation.Done || operation.Name == "" {
		return operationFailed(doing, operation)
	}
	service, err := b.clients.Databases()
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

func (b bootstrap) takeDatabase(ctx context.Context, id string) error {
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	path := databasePath(b.clients, id)
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
	if err := b.awaited(ctx, lifted, lifting); err != nil {
		return err
	}

	deleting, err := googleretry.Attempted(ctx, service.Projects.Databases.Delete(path).Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete the %q Firestore database: %w", id, err)
	}
	return b.awaited(ctx, fmt.Sprintf("deleting the %q Firestore database", id), deleting)
}

func (b bootstrap) takeKeyValues(ctx context.Context, tier environment.Tier) error {
	client, err := b.clients.Firestore()
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

func (b bootstrap) databasePresence(ctx context.Context, id string) (presence, error) {
	if b.clients.Emulated() {
		return presence{present: true}, nil
	}
	service, err := b.clients.Databases()
	if err != nil {
		return presence{}, err
	}
	database, err := googleretry.Attempted(ctx, service.Projects.Databases.Get(databasePath(b.clients, id)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return presence{}, nil
	}
	if err != nil {
		return presence{}, fmt.Errorf("read the %q Firestore database: %w", id, err)
	}
	if database.DeleteProtectionState != protectionOn {
		return presence{present: true, mends: reasonUnprotected}, nil
	}
	return presence{present: true}, nil
}

func databasePath(c *gcloud.Clients, id string) string {
	return "projects/" + c.Project() + "/databases/" + id
}
