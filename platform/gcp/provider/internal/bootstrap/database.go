package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	reasonUnprotected = "its delete protection is off"
)

var firestorePatience = gcloud.Patience{Attempts: 180, Ceiling: 10 * time.Second}

const databaseNameFreed = 5 * time.Minute

const (
	NativeFirestore = "FIRESTORE_NATIVE"
	protectionOn    = "DELETE_PROTECTION_ENABLED"
	protectionOff   = "DELETE_PROTECTION_DISABLED"
)

var databaseKind = KindOps{
	noun:          "Firestore database",
	terraform:     cost.FirestoreDatabaseType,
	emulatorKeeps: reasonEmulated,
	survey: func(ctx context.Context, b Engine, _ Survey, target Resource) (Presence, error) {
		return b.databasePresence(ctx, target.Name)
	},
	Make: func(ctx context.Context, b Engine, read Survey, target Resource) error {
		if _, err := b.ensureDatabase(ctx, read, target.Name, b.Clients.InlineTierTags(read.Tier)); err != nil || b.Clients.Emulated() {
			return err
		}
		return ports.UntilDatabaseServes(ctx, b.Clients.Workload(), target.Name)
	},
	mend: func(ctx context.Context, b Engine, _ Survey, target Resource) error {
		return b.protectDatabase(ctx, target.Name)
	},
	removal: func(read Survey, target Resource) Removal {
		if read.Emulated {
			return kept(target, reasonEmulated)
		}
		return Removal{Resource: target, Action: provider.ActionDisableThenDelete}
	},
	tagged: func(_ context.Context, b Engine, _ Survey, target Resource) (gcloud.TaggedResource, bool, error) {
		return b.Clients.DatabaseTagged(target.Name), true, nil
	},
	leave: func(ctx context.Context, b Engine, read Survey, taking Removal) error {
		if taking.Action == provider.ActionKeep && taking.Reason != ReasonAbsent {
			if err := b.takeKeyValues(ctx, read.Tier); err != nil {
				return err
			}
		}
		return b.TakeRecords(ctx, b, read.Tier)
	},
	Take: func(ctx context.Context, b Engine, _ Survey, target Resource) error {
		return b.TakeDatabase(ctx, target.Name)
	},
}

func (b Engine) MakeTierDatabase(ctx context.Context, tier environment.Tier, id string) error {
	read := Survey{Project: b.Clients.Project(), Region: b.Clients.Region}
	found, err := b.ensureDatabase(ctx, read, id, b.Clients.InlineTierTags(tier))
	if !found || err != nil {
		return err
	}
	return b.Clients.EnsureTierTag(ctx, tier, b.Clients.DatabaseTagged(id))
}

func (b Engine) TakeTierDatabase(ctx context.Context, tier environment.Tier, id string) error {
	if err := b.Clients.RemoveTierTagIfFound(ctx, tier, func(ctx context.Context) (gcloud.TaggedResource, bool, error) {
		present, err := b.databasePresence(ctx, id)
		return b.Clients.DatabaseTagged(id), present.Present, err
	}); err != nil {
		return err
	}
	return b.TakeDatabase(ctx, id)
}

func (b Engine) ensureDatabase(ctx context.Context, read Survey, id string, tags map[string]string) (bool, error) {
	service, err := b.Clients.Databases()
	if err != nil {
		return false, err
	}
	operation, err := googleretry.Attempted(ctx, service.Projects.Databases.Create("projects/"+read.Project, &firestoreadmin.GoogleFirestoreAdminV1Database{
		LocationId:            read.Region,
		Type:                  NativeFirestore,
		DeleteProtectionState: protectionOn,
		Tags:                  tags,
	}).DatabaseId(id).Context(ctx).Do)
	if gcloud.Taken(err) || googleretry.AnsweredCode(err) >= http.StatusInternalServerError {
		return b.adoptDatabase(ctx, read, id, err)
	}
	if err != nil {
		return false, fmt.Errorf("create the %q Firestore database: %w", id, err)
	}
	return false, b.Awaited(ctx, fmt.Sprintf("creating the %q Firestore database", id), operation)
}

func (b Engine) adoptDatabase(ctx context.Context, read Survey, id string, refused error) (bool, error) {
	service, err := b.Clients.Databases()
	if err != nil {
		return false, err
	}
	database, err := googleretry.Attempted(ctx, service.Projects.Databases.Get(DatabasePath(b.Clients, id)).Context(ctx).Do)
	switch {
	case gcloud.Absent(err):
		return false, refuseAbsentDatabase(id, refused)
	case err != nil:
		return false, fmt.Errorf("create the %q Firestore database: %w, then read it: %w", id, refused, err)
	case database.DeleteTime != "":
		return false, refuseDeletedDatabase(id, database.DeleteTime)
	}
	return true, b.databaseServesThisRegion(ctx, read, id, database.LocationId)
}

func refuseAbsentDatabase(id string, refused error) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"Firestore answered %v to creating the %q database and holds none by that name, as it does for 5 minutes after one is deleted.\n"+
			"Run the same command again once 5 minutes have passed since the delete",
		refused, id)
}

func refuseDeletedDatabase(id, deleted string) error {
	freed := deleted + " plus 5 minutes"
	if at, err := time.Parse(time.RFC3339Nano, deleted); err == nil {
		freed = at.Add(databaseNameFreed).UTC().Format("15:04:05 UTC")
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"Firestore deleted the %q database at %s and keeps its name for 5 minutes.\n"+
			"Run the same command again after %s",
		id, deleted, freed)
}

func (b Engine) databaseServesThisRegion(ctx context.Context, read Survey, id, location string) error {
	if location == read.Region {
		return b.protectDatabase(ctx, id)
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"project %s already has the %q Firestore database in %s and Firestore never moves one, "+
			"so the records this bootstrap writes would sit a continent away from the buckets and keys it names %s for.\n"+
			"Bootstrap this project in %s, or deploy into a project with no %q database",
		read.Project, id, location, read.Region, location, id)
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
	finished, err := gcloud.Waiting(ctx, firestorePatience, doing, func() (*firestoreadmin.GoogleLongrunningOperation, error) {
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
	client, err := b.Clients.TierFirestore(tier)
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
