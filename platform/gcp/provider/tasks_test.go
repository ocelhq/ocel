package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/task"
)

func TestTheBaseGivesATierThePushAccountAndDelayQueueItsTopicsAndTasksRunThrough(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	push := bootstrap.Resource{Kind: bootstrap.KindServiceAccount, Name: names.PushAccount(environment.TierProduction)}
	if !slices.Contains(googletest.IdsOf(bootstrap.Resources(names, environment.TierProduction, false)), push.ID()) {
		t.Errorf("the base provisions %v, want %s: Pub/Sub may sign a push as it only once a bootstrap grants it", googletest.IdsOf(bootstrap.Resources(names, environment.TierProduction, false)), push.ID())
	}
	queue := bootstrap.Resource{Kind: bootstrap.KindQueue, Name: names.DelayQueue(environment.TierProduction)}
	if !slices.Contains(googletest.IdsOf(bootstrap.Resources(names, environment.TierProduction, true)), queue.ID()) {
		t.Errorf("the base provisions %v, want %s: a deploy grants apps the queue and may grant only on a queue that already exists", googletest.IdsOf(bootstrap.Resources(names, environment.TierProduction, true)), queue.ID())
	}
}

func TestTopicsAndTasksCheckTheServicesAndPermissionsTheyAreAddedWith(t *testing.T) {
	t.Parallel()
	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}})

	for _, api := range []string{"pubsub.googleapis.com", "cloudtasks.googleapis.com"} {
		if !slices.Contains(bootstrap.APIsFor(environment.TierProduction, nil), api) {
			t.Errorf("a bootstrap checks %v, want %s: the base grants Pub/Sub's agent the push account and makes the delay queue", bootstrap.APIsFor(environment.TierProduction, nil), api)
		}
	}
	if parts := bootstrap.NewPartsRole(b.Clients.Names).Permissions; slices.ContainsFunc(parts, func(permission string) bool { return strings.HasPrefix(permission, "cloudtasks.") }) {
		t.Errorf("the parts role holds %v, want no Cloud Tasks permission: granted on the whole project, it would let a deploy set who may enqueue on any queue", parts)
	}
	for _, permission := range []string{"cloudtasks.queues.create", "cloudtasks.queues.purge", "cloudtasks.queues.getIamPolicy", "cloudtasks.queues.setIamPolicy"} {
		if !slices.Contains(bootstrap.BootstrapPermissions, permission) {
			t.Errorf("a bootstrap checks %v, want %s among them: the base makes, purges and grants on the delay queue", bootstrap.BootstrapPermissions, permission)
		}
	}
	deploy := deployOf(environment.TierProduction)
	for _, permission := range []string{"datastore.databases.create", "datastore.databases.list"} {
		if !slices.Contains(deploy.ProjectPermissions().Permissions, permission) {
			t.Errorf("a deploy is granted %s, want %s among them: a deploy makes a project's database of runs, and Firestore checks that against the project", deploy.ProjectPermissions(), permission)
		}
	}
	lines := credential.Lines(deploy.Grants())
	if want := deploy.Tiered(credential.DatabasesRole).String(); !slices.Contains(lines, want) {
		t.Errorf("a deploy credential is granted\n%s\nwant %s: a deploy indexes a project's database of runs and sets its time to live, and removing the project deletes it", strings.Join(lines, "\n"), want)
	}
	if !slices.Contains(bootstrap.BootstrapRoles(), "roles/cloudtasks.admin") {
		t.Errorf("a bootstrap credential is granted %v, want roles/cloudtasks.admin: removing a bootstrap purges its delay queue", bootstrap.BootstrapRoles())
	}
	for _, want := range []string{deploy.Tiered(credential.TopicsRole).String(), credential.SchedulesRole} {
		if !slices.Contains(lines, want) {
			t.Errorf("a deploy credential is granted\n%s\nwant %s: a deploy makes and removes topics, subscriptions and schedules", strings.Join(lines, "\n"), want)
		}
	}
}

func TestEveryListingOfATiersRunsByTopicConsumerStatusAndTagHasItsIndex(t *testing.T) {
	t.Parallel()

	shapes := map[string]bool{}
	for _, index := range task.RunIndexes() {
		key := ""
		for _, field := range index.Fields {
			key += field.FieldPath + ":" + field.Order + field.ArrayConfig + " "
		}
		shapes[key] = true
		if index.QueryScope != "COLLECTION" {
			t.Errorf("an index on runs is scoped to %s, want each environment's own runs collection", index.QueryScope)
		}
	}
	for _, consumer := range []string{"", "consumer:ASCENDING "} {
		for _, status := range []string{"", "status:ASCENDING "} {
			for _, tags := range []string{"", "tags:CONTAINS "} {
				if want := "topic:ASCENDING " + consumer + status + tags + "createdAt:DESCENDING "; !shapes[want] {
					t.Errorf("the runs indexes are %v, want one on %s", shapes, want)
				}
			}
		}
	}
}

func TestTheDelayQueueIsMadeGrantingNoOne(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := composed(bootstrap.Engine{Clients: server.Open(t)})

	if err := b.EnsureDelayQueue(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("ensureDelayQueue() = %v", err)
	}
	if server.QueueWrites != 0 {
		t.Errorf("%d queue policies were written, want none: apps are granted the queue as they deploy", server.QueueWrites)
	}
}

func TestABootstrapChecksEveryPermissionItsFirestoreAdminCallsNeed(t *testing.T) {
	t.Parallel()

	needs := map[string]string{
		"datastore.databases.create":      "creating a Firestore database",
		"datastore.databases.getMetadata": "reading a Firestore database",
		"datastore.databases.update":      "protecting a Firestore database",
		"datastore.databases.delete":      "deleting a Firestore database",
		"datastore.locations.list":        "asking which locations Firestore serves the project from",
		"datastore.operations.get":        "polling a Firestore operation",
		"datastore.schemas.create":        "creating a Firestore index",
		"datastore.schemas.list":          "listing Firestore indexes",
		"datastore.schemas.update":        "exempting a Firestore field from indexing",
	}
	for permission, call := range needs {
		if !slices.Contains(bootstrap.PermissionsFor(nil), permission) {
			t.Errorf("a bootstrap checks %v, want %s among them: %s would fail mid-apply", bootstrap.PermissionsFor(nil), permission, call)
		}
	}
}

func TestABootstrapChecksFirestoreIndexPermissionsByTheirCurrentNames(t *testing.T) {
	t.Parallel()

	for _, permission := range slices.Concat(bootstrap.PermissionsFor(nil), deployOf(environment.TierProduction).ProjectPermissions().Permissions, deployOf(environment.TierProduction).Untagged().Permissions) {
		if strings.HasPrefix(permission, "datastore.indexes.") {
			t.Errorf("a bootstrap or a deploy is granted %s, want datastore.schemas.*: Google's role reference lists only the schemas names", permission)
		}
	}
}

func plannedBy(t *testing.T, p *Provider, pre provider.DeployPreflight) []provider.ChangeGroup {
	t.Helper()
	plan, err := p.PlanPreflight(context.Background(), pre)
	if err != nil {
		t.Fatalf("PlanPreflight() = %v", err)
	}
	return plan.Groups
}

func plansWritableDatabase(planned []provider.ChangeGroup, database string) bool {
	return len(planned) == 1 && planned[0].Action == provider.ActionCreate && len(planned[0].Changes) == 1 &&
		planned[0].Changes[0].Name == database && planned[0].Changes[0].Action == provider.ActionCreate
}

func declaringATaskOf(project string) provider.DeployPreflight {
	pre := declaringATask()
	pre.Deploy.Slug = project
	return pre
}

func TestADryDeployDeclaringATaskPlansItsProjectsWritableDatabaseAndMakesNothing(t *testing.T) {
	database := ports.ProjectWritableDatabase("ocel", environment.TierProduction, "shop")
	if planned := plannedBy(t, failingOnAnyCall(t), declaringATaskOf("shop")); !plansWritableDatabase(planned, database) {
		t.Errorf("a dry deploy declaring a task planned %+v, want %s created", planned, database)
	}
}

func TestTheFirstDeployDeclaringATaskMakesItsProjectsWritableDatabaseAndAnotherProjectsIsStillPlanned(t *testing.T) {
	p, _ := deployingNetworks(t)

	if err := p.ApplyPreflight(context.Background(), declaringATaskOf("shop")); err != nil {
		t.Fatalf("ApplyPreflight() = %v", err)
	}
	if planned := plannedBy(t, p, declaringATaskOf("shop")); len(planned) != 0 {
		t.Errorf("a dry deploy of shop after its first planned %+v, want nothing", planned)
	}
	if planned := plannedBy(t, p, declaringATaskOf("blog")); !plansWritableDatabase(planned, ports.ProjectWritableDatabase("ocel", environment.TierProduction, "blog")) {
		t.Errorf("a dry deploy of blog planned %+v, want blog's own database created", planned)
	}
}

func TestRemovingABootstrapPurgesItsDelayQueueAndKeepsIt(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := composed(bootstrap.Engine{Clients: server.Open(t)})
	ctx := context.Background()
	tier := environment.TierProduction
	read := bootstrap.Survey{Tier: tier, Names: b.Clients.Names, Emulated: true}
	queue := bootstrap.Resource{Kind: bootstrap.KindQueue, Name: b.Clients.DelayQueue(tier)}
	read.Held = map[string]bool{queue.ID(): true}

	if taking := bootstrap.Removing(read, queue); taking.Action != provider.ActionUpdate || taking.Reason != bootstrap.ReasonQueuePurged {
		t.Errorf("removing the delay queue plans %s (%s), want it purged and kept", taking.Action, taking.Reason)
	}
	if _, err := queue.KindOps().Take(ctx, b, read, queue); err != nil {
		t.Fatalf("take(queue) = %v", err)
	}
	if !slices.Equal(server.QueuesPurged, []string{b.DelayQueuePath(tier)}) {
		t.Errorf("removing the bootstrap purged %v, want its delay queue: Cloud Tasks holds a deleted queue's name for days", server.QueuesPurged)
	}
}
