package variables

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	firestoreadmin "google.golang.org/api/firestore/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func recordingEngine() bootstrap.Engine {
	return bootstrap.Engine{
		ServiceStates: &bootstrap.ServiceStates{},
		Clients:       &gcloud.Clients{Names: names.New(provider.Namespace("ocel"), "acme-prod"), Region: "europe-west1", Endpoint: "http://127.0.0.1:1"},
		Records:       fake.NewKeyValues(),
	}
}

func recordedDatabase(t *testing.T, b bootstrap.Engine, tier environment.Tier, project string) string {
	t.Helper()
	database, found, err := ports.ReadProjectDatabaseRecord(context.Background(), b.Records, tier, project)
	if err != nil || !found {
		t.Fatalf("the database %s keeps its records in = %q, %t, %v, want it on record", project, database, found, err)
	}
	return database
}

func TestAProjectsFirstUseRecordsTheDatabaseItsOwnDatabaseOptionNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := recordingEngine()
	tier := environment.TierProduction

	if err := EnsureProjectDatabase(ctx, b, tier, "shop", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := EnsureProjectDatabase(ctx, b, tier, "blog", true, nil); err != nil {
		t.Fatal(err)
	}

	if got, want := recordedDatabase(t, b, tier, "shop"), b.Clients.SharedDatabase(tier); got != want {
		t.Errorf("shop keeps its records in %q, want the shared %q", got, want)
	}
	if got, want := recordedDatabase(t, b, tier, "blog"), b.Clients.OwnDatabase(tier, "blog"); got != want {
		t.Errorf("blog keeps its records in %q, want its own %q", got, want)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, SharedDatabasePartName); err != nil || !present {
		t.Errorf("the %s part is on record: %t, %v, want it added by shop's first use", SharedDatabasePartName, present, err)
	}
}

func TestADeployThatChangesOwnDatabaseAfterTheProjectsFirstUseIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := recordingEngine()
	tier := environment.TierPreview
	for project, own := range map[string]bool{"shop": false, "blog": true} {
		if err := EnsureProjectDatabase(ctx, b, tier, project, own, nil); err != nil {
			t.Fatal(err)
		}
	}

	for project, own := range map[string]bool{"shop": true, "blog": false} {
		err := EnsureProjectDatabase(ctx, b, tier, project, own, nil)
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "ocel projects destroy "+project) {
			t.Errorf("deploying %s with ownDatabase %t after its first use = %v, want it refused naming `ocel projects destroy %s`: its records stay where they are", project, own, err, project)
		}
		if _, err := PlanProjectDatabase(ctx, b, tier, project, own); err == nil {
			t.Errorf("planning %s with ownDatabase %t after its first use passed, want the same refusal before any change", project, own)
		}
	}
}

func TestARecordedProjectIsNotAskedAgainWhichDatabaseItWants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := recordingEngine()
	tier := environment.TierProduction
	if err := EnsureProjectDatabase(ctx, b, tier, "shop", true, nil); err != nil {
		t.Fatal(err)
	}

	if err := EnsureProjectDatabaseRecorded(ctx, b, tier, "shop", false, nil); err != nil {
		t.Errorf("a value write for shop, recorded with its own database, = %v, want it to keep what is recorded", err)
	}
	if got, want := recordedDatabase(t, b, tier, "shop"), b.Clients.OwnDatabase(tier, "shop"); got != want {
		t.Errorf("shop keeps its records in %q, want its own %q still", got, want)
	}
}

func TestTheSharedDatabaseIsNotTakenDownWhileAProjectKeepsItsRecordsThere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := recordingEngine()
	tier := environment.TierProduction
	if err := EnsureProjectDatabase(ctx, b, tier, "shop", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := EnsureProjectDatabase(ctx, b, tier, "blog", true, nil); err != nil {
		t.Fatal(err)
	}
	part := SharedDatabasePart().Deployed(b, tier)

	err := part.RefuseInUse(ctx, bootstrap.PartRemoval{Retry: "ocel bootstrap --remove " + SharedDatabasePartName})
	if err == nil || !strings.Contains(err.Error(), "shop") || strings.Contains(err.Error(), "blog") {
		t.Errorf("removing the shared database while shop keeps its records there = %v, want it refused naming shop alone", err)
	}
	users, err := part.Users(ctx)
	if err != nil || len(users) != 1 || users[0] != "shop" {
		t.Errorf("the shared database is used by %v, %v, want shop alone", users, err)
	}
}

func refusingDatabases(t *testing.T) *gcloud.Clients {
	t.Helper()
	tags := googletest.NewTieredTags(t)
	databases := googletest.NewDatabases(t, tags)
	databases.CreateDenied = true
	return tags.Tagged(googletest.Clients(databases.URL))
}

func TestADatabaseTheDeployCredentialMayNotCreateIsRefusedNamingTheTieredDatabasesRole(t *testing.T) {
	t.Parallel()
	c := refusingDatabases(t)
	b := bootstrap.Engine{ServiceStates: &bootstrap.ServiceStates{}, Clients: c}

	err := makeAppDatabase(context.Background(), b, environment.TierProduction, c.OwnDatabase(environment.TierProduction, "shop"), nil, nil)

	want := "the deploy credential lacks " + c.DeployCredential(environment.TierProduction).Tiered(credential.DatabasesRole).String()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("makeAppDatabase() = %v, want it to say %q: Firestore checks creating a database against the untagged project, which the tiered databases role reaches", err, want)
	}
}

func TestADatabaseGoogleDoesNotServeYetIsLaidOutOnceItDoes(t *testing.T) {
	t.Parallel()
	tags := googletest.NewTieredTags(t)
	databases := googletest.NewDatabases(t, tags)
	databases.LayoutUnserved = 2
	b := bootstrap.Engine{ServiceStates: &bootstrap.ServiceStates{}, Clients: tags.Tagged(googletest.Clients(databases.URL))}
	layout := Layout{
		Expiring: []LayoutField{{Group: "runs", Path: "purgeAt"}},
		Indexes:  []LayoutIndex{{Group: "runs", Index: &firestoreadmin.GoogleFirestoreAdminV1Index{QueryScope: "COLLECTION"}}},
	}

	if err := makeAppDatabase(context.Background(), b, environment.TierProduction, b.Clients.SharedDatabase(environment.TierProduction), []Layout{layout}, nil); err != nil {
		t.Fatalf("makeAppDatabase() = %v, want its layout made once Firestore serves the new database", err)
	}
	if want := []string{"PATCH runs/fields/purgeAt", "POST runs/indexes"}; !slices.Equal(databases.Layouts, want) {
		t.Errorf("laid out %q, want %q", databases.Layouts, want)
	}
}

func TestEveryTimeToLiveAndIndexOfADatabaseIsStartedBeforeAnyIsAwaited(t *testing.T) {
	t.Parallel()
	tags := googletest.NewTieredTags(t)
	databases := googletest.NewDatabases(t, tags)
	databases.OperationPolls = 2
	b := bootstrap.Engine{ServiceStates: &bootstrap.ServiceStates{}, Clients: tags.Tagged(googletest.Clients(databases.URL))}
	database := b.Clients.SharedDatabase(environment.TierProduction)
	layout := Layout{
		Expiring:  []LayoutField{{Group: "runs", Path: "purgeAt"}, {Group: "tags", Path: "purgeAt"}},
		Unindexed: []LayoutField{{Group: "runs", Path: "payload"}},
		Indexes: []LayoutIndex{
			{Group: "runs", Index: &firestoreadmin.GoogleFirestoreAdminV1Index{QueryScope: "COLLECTION"}},
			{Group: "tags", Index: &firestoreadmin.GoogleFirestoreAdminV1Index{QueryScope: "COLLECTION"}},
		},
	}
	log := &fake.Log{}

	if err := makeAppDatabase(context.Background(), b, environment.TierProduction, database, []Layout{layout}, log); err != nil {
		t.Fatalf("makeAppDatabase() = %v, want its layout made", err)
	}
	if len(databases.Layouts) != 5 || len(databases.LaidOutAfterAnOperationFinished) > 0 {
		t.Errorf("laid out %q, of which %q only after an earlier change had finished, want all five started together: each takes minutes on a new database",
			databases.Layouts, databases.LaidOutAfterAnOperationFinished)
	}
	if lines := log.Lines(); len(lines) != 1 || !strings.Contains(lines[0], database) {
		t.Errorf("progress heard %q, want one line naming %s while its layout is awaited", lines, database)
	}
}

func TestAnIndexFirestoreRefusesAsContendedIsCreatedAgainRatherThanTakenAsPresent(t *testing.T) {
	t.Parallel()
	tags := googletest.NewTieredTags(t)
	databases := googletest.NewDatabases(t, tags)
	databases.LayoutContended = 1
	b := bootstrap.Engine{ServiceStates: &bootstrap.ServiceStates{}, Clients: tags.Tagged(googletest.Clients(databases.URL))}
	layout := Layout{Indexes: []LayoutIndex{{Group: "runs", Index: &firestoreadmin.GoogleFirestoreAdminV1Index{QueryScope: "COLLECTION"}}}}

	if err := makeAppDatabase(context.Background(), b, environment.TierProduction, b.Clients.SharedDatabase(environment.TierProduction), []Layout{layout}, nil); err != nil {
		t.Fatalf("makeAppDatabase() = %v, want the index made", err)
	}
	if want := []string{"POST runs/indexes"}; !slices.Equal(databases.Layouts, want) {
		t.Errorf("laid out %q, want %q: Firestore answers a change it makes alongside another with 409, which is not an index already there", databases.Layouts, want)
	}
}
