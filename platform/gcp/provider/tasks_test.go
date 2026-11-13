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
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/task"
)

func TestTheFirstTopicOrTaskAddsThePushAccountAndDelayQueueAndNoBootstrapMakesThem(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	push := bootstrap.Resource{Kind: bootstrap.KindServiceAccount, Name: names.PushAccount(environment.TierProduction)}
	queue := bootstrap.Resource{Kind: bootstrap.KindQueue, Name: names.DelayQueue(environment.TierProduction)}
	for _, emulated := range []bool{false, true} {
		if base := googletest.IdsOf(bootstrap.Resources(names, environment.TierProduction, emulated)); slices.Contains(base, push.ID()) || slices.Contains(base, queue.ID()) {
			t.Errorf("the base provisions %v, want neither %s nor %s: the first deploy declaring a topic or task adds them", base, push.ID(), queue.ID())
		}
	}
	if got := googletest.IdsOf(bootstrap.Topics.Items(names, environment.TierProduction)); !slices.Equal(got, []string{push.ID(), queue.ID()}) {
		t.Errorf("the topics part holds %v, want %s and %s", got, push.ID(), queue.ID())
	}
}

func TestTopicsAndTasksCheckTheServicesAndPermissionsTheyAreAddedWith(t *testing.T) {
	t.Parallel()
	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}})

	for _, api := range []string{"pubsub.googleapis.com", "cloudtasks.googleapis.com"} {
		if !slices.Contains(bootstrap.Topics.APIs, api) {
			t.Errorf("the topics part checks %v, want %s: it grants Pub/Sub's agent the push account and makes the delay queue", bootstrap.Topics.APIs, api)
		}
	}
	if parts := bootstrap.NewPartsRole(b.Clients.Names).Permissions; slices.ContainsFunc(parts, func(permission string) bool { return strings.HasPrefix(permission, "cloudtasks.") }) {
		t.Errorf("the parts role holds %v, want no Cloud Tasks permission: granted on the whole project, it would let a deploy set who may enqueue on any queue", parts)
	}
	for _, permission := range []string{"cloudtasks.queues.create", "cloudtasks.queues.purge", "cloudtasks.queues.getIamPolicy", "cloudtasks.queues.setIamPolicy"} {
		if !slices.Contains(bootstrap.BootstrapGrants.Permissions(), permission) {
			t.Errorf("a bootstrap checks %v, want %s among them: a bootstrap mends and purges the delay queue", bootstrap.BootstrapGrants.Permissions(), permission)
		}
	}
	deploy := deployOf(environment.TierProduction)
	held := grantedOver(t, deploy, checkedResource{name: "projects/acme-prod"})
	for _, permission := range []string{"datastore.databases.create", "datastore.databases.list", "cloudtasks.queues.create", "cloudtasks.queues.setIamPolicy", "cloudtasks.queues.purge"} {
		if _, holds := held[permission]; !holds {
			t.Errorf("a deploy lacks %s on the untagged project: it makes a project's database of runs and its tier's delay queue, and Google checks both against the project", permission)
		}
	}
	lines := credential.Lines(deploy.Grants())
	if want := deploy.Tiered(credential.DatabasesRole).String(); !slices.Contains(lines, want) {
		t.Errorf("a deploy credential is granted\n%s\nwant %s: a deploy indexes a project's database of runs and sets its time to live, and removing the project deletes it", strings.Join(lines, "\n"), want)
	}
	if !slices.Contains(bootstrap.BootstrapRoles(), "roles/cloudtasks.admin") {
		t.Errorf("a bootstrap credential is granted %v, want roles/cloudtasks.admin: removing a bootstrap purges its delay queue", bootstrap.BootstrapRoles())
	}
	for _, want := range []string{deploy.Tiered(credential.TopicsRole).String(), deploy.Schedules().String()} {
		if !slices.Contains(lines, want) {
			t.Errorf("a deploy credential is granted\n%s\nwant %s: a deploy makes and removes topics, subscriptions and schedules", strings.Join(lines, "\n"), want)
		}
	}
}

func TestEveryListingOfATiersRunsByTopicConsumerStatusAndTagHasItsIndex(t *testing.T) {
	t.Parallel()

	inventories := map[string]bool{}
	for _, index := range task.RunIndexes() {
		key := ""
		for _, field := range index.Fields {
			key += field.FieldPath + ":" + field.Order + field.ArrayConfig + " "
		}
		inventories[key] = true
		if index.QueryScope != "COLLECTION" {
			t.Errorf("an index on runs is scoped to %s, want each environment's own runs collection", index.QueryScope)
		}
	}
	for _, consumer := range []string{"", "consumer:ASCENDING "} {
		for _, status := range []string{"", "status:ASCENDING "} {
			for _, tags := range []string{"", "tags:CONTAINS "} {
				if want := "topic:ASCENDING " + consumer + status + tags + "createdAt:DESCENDING "; !inventories[want] {
					t.Errorf("the runs indexes are %v, want one on %s", inventories, want)
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

	for _, permission := range slices.Concat(bootstrap.PermissionsFor(nil), deployOf(environment.TierProduction).ProjectPermissions().Permissions) {
		if strings.HasPrefix(permission, "datastore.indexes.") {
			t.Errorf("a bootstrap or a deploy is granted %s, want datastore.schemas.*: Google's role reference lists only the schemas names", permission)
		}
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

	want := "its waiting messages are deleted, since Cloud Tasks reserves a deleted queue's name for 7 days"
	if taking := bootstrap.Removing(read, queue); taking.Action != provider.ActionUpdate || taking.Reason != want {
		t.Errorf("removing the delay queue plans %s (%s), want %s (%s)", taking.Action, taking.Reason, provider.ActionUpdate, want)
	}
	if err := queue.KindOps().Take(ctx, b, read, queue); err != nil {
		t.Fatalf("take(queue) = %v", err)
	}
	if !slices.Equal(server.QueuesPurged, []string{b.DelayQueuePath(tier)}) {
		t.Errorf("removing the bootstrap purged %v, want its delay queue: Cloud Tasks holds a deleted queue's name for days", server.QueuesPurged)
	}
}
