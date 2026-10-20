package gcp

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"testing"

	raw "google.golang.org/api/storage/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func bucketBootstrap(t *testing.T) (bootstrap.Engine, bootstrap.Survey, *storageServer) {
	t.Helper()
	p, server := servingStorage(t)
	clients := &gcloud.Clients{Names: p.resolved.Names, Region: "europe-west1", Endpoint: p.resolved.Endpoint, Records: fake.NewKeyValues()}
	read := bootstrap.Survey{Tier: environment.TierProduction, Names: clients.Names, Project: "acme-prod", Region: "europe-west1", Buckets: naming.Buckets{}}
	return bootstrap.Engine{Clients: clients}, read, server
}

func tierBucketOf(class naming.StorageClass) bootstrap.Resource {
	return bootstrap.Resource{Kind: bootstrap.KindBucket, Name: "ocel-prod-" + string(class), Class: class}
}

func TestABootstrapMakesADataClassesBucketUnderARandomTagAndRecordsItsName(t *testing.T) {
	t.Parallel()
	b, read, server := bucketBootstrap(t)
	target := tierBucketOf(naming.StorageCache)

	if err := target.KindOps().Make(context.Background(), b, read, target); err != nil {
		t.Fatalf("Make() = %v", err)
	}
	if len(server.created) != 1 || !regexp.MustCompile(`^ocel-prod-cache-[0-9a-z]{6}$`).MatchString(server.created[0].Name) {
		t.Fatalf("the bootstrap created %v, want one bucket named ocel-prod-cache-<random tag>", server.created)
	}
	recorded, err := b.Clients.TierBuckets(context.Background(), read.Tier, naming.StorageCache)
	if err != nil || recorded[naming.StorageCache] != server.created[0].Name {
		t.Fatalf("TierBuckets(cache) after the bootstrap made the bucket = %v, %v, want %s read back from its record", recorded, err, server.created[0].Name)
	}
	read.Buckets = recorded
	found, err := b.PresenceOf(context.Background(), read, target)
	if err != nil || !found.Present {
		t.Errorf("PresenceOf() of the bucket just made = %+v, %v, want it found by its recorded name", found, err)
	}
	if listed := server.listings(); listed != 0 {
		t.Errorf("the bootstrap listed the project's buckets %d times, want none: it reads the name it recorded", listed)
	}
}

func TestABootstrapRacingAnotherMakesTheBucketUnderTheNameTheOtherClaimed(t *testing.T) {
	t.Parallel()
	b, read, server := bucketBootstrap(t)
	other, err := b.Clients.ClaimTierBucket(context.Background(), read.Tier, naming.StorageEdge, func() string { return "ocel-prod-edge-0th3r0" })
	if err != nil {
		t.Fatal(err)
	}
	server.holding(&raw.Bucket{Name: other})
	target := tierBucketOf(naming.StorageEdge)

	if err := target.KindOps().Make(context.Background(), b, read, target); err != nil {
		t.Fatalf("Make() beside another run's edge bucket = %v, want the other run's bucket adopted", err)
	}
	if len(server.created) != 0 || len(server.deleted) != 0 {
		t.Errorf("the bootstrap created %v and deleted %v, want neither: the tier keeps its edge objects in the one bucket claimed first", server.created, server.deleted)
	}
}

func TestATierBucketNameAnotherProjectHoldsIsForgottenSoTheNextBootstrapMintsAnother(t *testing.T) {
	t.Parallel()
	b, read, server := bucketBootstrap(t)
	server.takenElsewhere = true
	target := tierBucketOf(naming.StorageEdge)

	err := target.KindOps().Make(context.Background(), b, read, target)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Make() over a name another project holds = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
	recorded, err := b.Clients.RecordedTierBuckets(context.Background(), read.Tier)
	if err != nil || recorded[naming.StorageEdge] != "" {
		t.Fatalf("the edge bucket recorded after the refusal = %v, %v, want none: a name another project holds would be read and refused by every later bootstrap", recorded, err)
	}

	server.takenElsewhere = false
	if err := target.KindOps().Make(context.Background(), b, read, target); err != nil || len(server.created) != 1 {
		t.Errorf("the next bootstrap's Make() = %v with %d buckets created, want one bucket made under a new name", err, len(server.created))
	}
}

func TestATierBucketAnEarlierBootstrapMadeKeepsItsRecordedNameWhenTheBootstrapCannotReadIt(t *testing.T) {
	t.Parallel()
	b, read, server := bucketBootstrap(t)
	earlier, err := b.Clients.ClaimTierBucket(context.Background(), read.Tier, naming.StorageState, func() string { return "ocel-prod-state-a1b2c3" })
	if err != nil {
		t.Fatal(err)
	}
	server.holding(&raw.Bucket{Name: earlier}, "ocel/bootstrap.json")
	server.forbiddingReads = true
	target := tierBucketOf(naming.StorageState)

	err = target.KindOps().Make(context.Background(), b, read, target)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
		t.Fatalf("Make() over the state bucket an earlier bootstrap made = %v, want a %s refusal", err, refusal.CodeDenied)
	}
	b.Clients.DropTierBucketsRead(read.Tier)
	recorded, err := b.Clients.RecordedTierBuckets(context.Background(), read.Tier)
	if err != nil || recorded[naming.StorageState] != earlier {
		t.Errorf("the state bucket recorded after the refusal = %v, %v, want %s: forgetting it would leave the tier's Pulumi state behind", recorded, err, earlier)
	}
}

func TestTakingATiersBucketEmptiesAndDeletesTheRecordedBucketOfItsClassAndNoOther(t *testing.T) {
	t.Parallel()
	b, read, server := bucketBootstrap(t)
	server.holding(&raw.Bucket{Name: "ocel-prod-state-a1b2c3"}, "ocel/bootstrap.json")
	server.holding(&raw.Bucket{Name: "ocel-prod-edge-a1b2c3"})
	server.holding(&raw.Bucket{Name: "ocel-preview-state-a1b2c3"})
	read.Buckets = naming.Buckets{naming.StorageState: "ocel-prod-state-a1b2c3", naming.StorageEdge: "ocel-prod-edge-a1b2c3"}
	target := tierBucketOf(naming.StorageState)

	if _, err := target.KindOps().Take(context.Background(), b, read, target); err != nil {
		t.Fatalf("Take() = %v", err)
	}
	if want := []string{"ocel-prod-state-a1b2c3"}; !slices.Equal(server.deleted, want) {
		t.Errorf("Take() deleted %v, want exactly %v", server.deleted, want)
	}

	read.Buckets = naming.Buckets{}
	if _, err := tierBucketOf(naming.StorageCache).KindOps().Take(context.Background(), b, read, tierBucketOf(naming.StorageCache)); err != nil {
		t.Errorf("Take() of a class with no bucket recorded = %v, want nothing to do", err)
	}
}

func TestTakingATiersBucketForgetsItsRecordedName(t *testing.T) {
	t.Parallel()
	b, read, server := bucketBootstrap(t)
	recorded, err := b.Clients.ClaimTierBucket(context.Background(), read.Tier, naming.StorageCache, func() string { return "ocel-prod-cache-a1b2c3" })
	if err != nil {
		t.Fatal(err)
	}
	server.holding(&raw.Bucket{Name: "ocel-prod-cache-a1b2c3"})
	read.Buckets = naming.Buckets{naming.StorageCache: recorded}
	target := tierBucketOf(naming.StorageCache)

	if _, err := target.KindOps().Take(context.Background(), b, read, target); err != nil {
		t.Fatalf("Take() = %v", err)
	}
	b.Clients.DropTierBucketsRead(read.Tier)
	left, err := b.Clients.RecordedTierBuckets(context.Background(), read.Tier)
	if err != nil || left[naming.StorageCache] != "" {
		t.Errorf("the cache bucket recorded after Take() deleted it = %v, %v, want none: a sibling tier keeps the database the record is in, and the record would name a deleted bucket", left, err)
	}
}

func TestATiersBucketsAreRemovedBeforeTheDatabaseTheirNamesAreRecordedIn(t *testing.T) {
	t.Parallel()
	_, read, _ := bucketBootstrap(t)
	read.Held = map[string]bool{}
	for _, each := range bootstrap.Resources(read.Names, read.Tier, false) {
		read.Held[each.ID()] = true
	}

	var order []bootstrap.Kind
	for _, taking := range bootstrap.Removals(read) {
		order = append(order, taking.Resource.Kind)
	}
	if last := order[len(order)-1]; last != bootstrap.KindDatabase || slices.Index(order, bootstrap.KindBucket) > slices.Index(order, bootstrap.KindDatabase) {
		t.Errorf("a removal takes %v in that order, want every bucket before the database: a removal stopped after the database would leave buckets nothing names", order)
	}
}
