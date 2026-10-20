package gcp

import (
	"context"
	"regexp"
	"slices"
	"testing"

	raw "google.golang.org/api/storage/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
)

func bucketBootstrap(t *testing.T) (bootstrap.Engine, bootstrap.Survey, *storageServer) {
	t.Helper()
	p, server := servingStorage(t)
	read := bootstrap.Survey{Tier: environment.TierProduction, Names: p.resolved.Names, Project: "acme-prod", Region: "europe-west1"}
	return bootstrap.Engine{Clients: p.resolved}, read, server
}

func tierBucketOf(class naming.StorageClass) bootstrap.Resource {
	return bootstrap.Resource{Kind: bootstrap.KindBucket, Name: "ocel-prod-" + string(class), Class: class}
}

func TestABootstrapMakesADataClassesBucketUnderARandomTagWithTheLifecycleOfItsClass(t *testing.T) {
	t.Parallel()
	b, read, server := bucketBootstrap(t)
	target := tierBucketOf(naming.StorageCache)

	if err := target.KindOps().Make(context.Background(), b, read, target); err != nil {
		t.Fatalf("Make() = %v", err)
	}
	if len(server.created) != 1 || !regexp.MustCompile(`^ocel-prod-cache-[0-9a-z]{6}$`).MatchString(server.created[0].Name) {
		t.Fatalf("the bootstrap created %v, want one bucket named ocel-prod-cache-<random tag>", server.created)
	}
	rules := server.created[0].Lifecycle.Rule
	if len(rules) != 1 || rules[0].Action.Type != "Delete" || rules[0].Condition.Age == nil || *rules[0].Condition.Age != 30 {
		t.Errorf("the cache bucket is created with lifecycle %+v, want its objects deleted 30 days after their last write", server.created[0].Lifecycle)
	}
	found, err := b.PresenceOf(context.Background(), read.Tier, target)
	if err != nil || !found.Present {
		t.Errorf("PresenceOf() of the bucket just made = %+v, %v, want it found by its untagged name", found, err)
	}
}

func TestABootstrapThatMadeATiersBucketBesideAnotherRunsTakesItsOwnAndRefusesAsBusy(t *testing.T) {
	t.Parallel()
	b, read, server := bucketBootstrap(t)
	server.holding(&raw.Bucket{Name: "ocel-prod-edge-0th3r0"})
	target := tierBucketOf(naming.StorageEdge)

	err := target.KindOps().Make(context.Background(), b, read, target)
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeBusy {
		t.Fatalf("Make() beside another run's edge bucket = %v, want a busy refusal", err)
	}
	if len(server.created) != 1 || !slices.Equal(server.deleted, []string{server.created[0].Name}) {
		t.Errorf("the bootstrap created %v and deleted %v, want the bucket it made deleted and the other run's kept", server.created, server.deleted)
	}
}

func TestTakingATiersBucketEmptiesAndDeletesEveryBucketOfItsClassAndNoOther(t *testing.T) {
	t.Parallel()
	b, read, server := bucketBootstrap(t)
	server.holding(&raw.Bucket{Name: "ocel-prod-state-a1b2c3"}, "ocel/bootstrap.json")
	server.holding(&raw.Bucket{Name: "ocel-prod-state-d4e5f6"})
	server.holding(&raw.Bucket{Name: "ocel-prod-edge-a1b2c3"})
	server.holding(&raw.Bucket{Name: "ocel-preview-state-a1b2c3"})
	target := tierBucketOf(naming.StorageState)

	if _, err := target.KindOps().Take(context.Background(), b, read, target); err != nil {
		t.Fatalf("Take() = %v", err)
	}
	slices.Sort(server.deleted)
	if want := []string{"ocel-prod-state-a1b2c3", "ocel-prod-state-d4e5f6"}; !slices.Equal(server.deleted, want) {
		t.Errorf("Take() deleted %v, want exactly %v", server.deleted, want)
	}
}
