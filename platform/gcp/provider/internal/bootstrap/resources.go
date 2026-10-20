package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const StampObject = "ocel/bootstrap.json"

var BootstrapAPIs = []string{
	"firestore.googleapis.com",
	"storage.googleapis.com",
	"cloudkms.googleapis.com",
	"secretmanager.googleapis.com",
	"artifactregistry.googleapis.com",
	"iam.googleapis.com",
	"run.googleapis.com",
	"cloudscheduler.googleapis.com",
	"iamcredentials.googleapis.com",
	"pubsub.googleapis.com",
	"cloudtasks.googleapis.com",
}

type Resource struct {
	Kind   Kind
	Name   string
	Note   string
	Slow   bool
	Shared bool
	Class  naming.StorageClass
}

func (i Resource) ID() string { return string(i.Kind) + "/" + i.Name }

func (i Resource) phrase() string { return i.KindOps().noun + " " + i.Name }

func (i Resource) KindOps() KindOps { return kinds[i.Kind] }

func provisioned(target Resource, emulated bool) bool {
	return !emulated || !target.KindOps().emulatorSkips
}

func StackResources(names names.Names, tier environment.Tier, emulated bool) []Resource {
	items := []Resource{
		{
			Kind: KindDatabase, Name: names.Database(), Shared: true, Slow: true,
			Note: "every record this project stores, and both tiers keep theirs in it",
		},
		{
			Kind: KindBucket, Name: names.TierBucketStem(tier, naming.StorageState), Class: naming.StorageState,
			Note: fmt.Sprintf("the state every stack in this tier writes and the stamp saying what this bootstrap is, versioned so a bad write is recoverable for %d days", NoncurrentStateLifetimeDays),
		},
		{
			Kind: KindBucket, Name: names.TierBucketStem(tier, naming.StorageEdge), Class: naming.StorageEdge,
			Note: "what this tier's edges and its load balancer may serve: static files, prerendered pages, route tables, edge bundles and image settings",
		},
		{
			Kind: KindBucket, Name: names.TierBucketStem(tier, naming.StorageCache), Class: naming.StorageCache,
			Note: fmt.Sprintf("what only an app's own code reads back: its fetch and use-cache entries, each deleted %d days after its last write, and its tag clock, which is kept", naming.CacheObjectLifetimeDays),
		},
		{
			Kind: KindKeyRing, Name: names.KeyRing(), Shared: true,
			Note: "the ring both tiers' keys hang on",
		},
		{
			Kind: KindKey, Name: string(tier),
			Note: "the key every value this tier stores is sealed under",
		},
		{
			Kind: KindRole, Name: names.AppAccountsRole(), Shared: true,
			Note: "the role a deploy credential is granted to create the service accounts its apps run as and to set who may act as them, and no more",
		},
		{
			Kind: KindRole, Name: names.CDNPurgeRole(), Shared: true,
			Note: "the role a deploy grants a Next app behind the alb to clear its revalidated pages from Cloud CDN, and no more",
		},
		{
			Kind: KindRole, Name: names.PartsRole(), Shared: true,
			Note: "the role a deploy credential is granted to add the parts of a bootstrap Google names no condition for, such as a tier's private network, and no more",
		},
		{
			Kind: KindRole, Name: names.RealtimeSubscriptionsRole(), Shared: true,
			Note: "the role each tier's realtime account is granted on the project to make, pull and delete the Pub/Sub subscription each gateway instance hears its environment's topic through, and no more",
		},
		{
			Kind: KindServiceAccount, Name: names.RealtimeAccount(tier),
			Note: "the identity this tier's realtime gateways run as, which may read this tier's realtime keys and no other secret",
		},
		{
			Kind: KindRepository, Name: names.Repository(tier),
			Note: "the images this tier runs, and an untagged image lives at least a week",
		},
		{
			Kind: KindGrant, Name: NetworkUserRole, Shared: true,
			Note: "lets the Cloud Run service agent attach services to the subnetwork of a tier's private network, which a deploy adds with its first database or kv store and may not grant on",
		},
		{
			Kind: KindServiceAccount, Name: names.PushAccount(tier),
			Note: "the identity Pub/Sub signs each push to a worker of this tier as, which only a bootstrap may let Pub/Sub's agent sign as",
		},
		{
			Kind: KindQueue, Name: names.DelayQueue(tier),
			Note: "the queue a delayed message of this tier waits in, which a deploy grants each app that publishes the right to enqueue on, and may only grant on a queue that already exists",
		},
		{
			Kind: KindRole, Name: names.ProjectRecordsRole(), Shared: true,
			Note: "the role a deploy credential is granted, on the condition it names them, to make the Firestore databases each project's values, runs and tag records are kept in and the key its values are sealed under, and no more",
		},
		{
			Kind: KindServiceAccount, Name: names.EnvSourceSyncAccount(tier),
			Note: "the identity the env source sync runs as and Cloud Scheduler calls it as: it reads and writes this tier's records, seals and opens under the tier key and its projects' keys, and calls the sync service alone",
		},
	}
	return slices.DeleteFunc(items, func(each Resource) bool { return !provisioned(each, emulated) })
}

func ParameterResources(names names.Names, tier environment.Tier) []Resource {
	return []Resource{{
		Kind: KindSecret, Name: names.PassphraseSecret(tier),
		Note: "the passphrase this tier's state is encrypted under, minted here and never written over",
	}}
}

func Resources(names names.Names, tier environment.Tier, emulated bool) []Resource {
	return slices.Concat(StackResources(names, tier, emulated), ParameterResources(names, tier))
}

func DigestOf(namespace provider.Namespace, items []Resource) string {
	sum := sha256.New()
	sum.Write([]byte(namespace.String() + "\n"))
	for _, item := range items {
		sum.Write([]byte(item.ID() + "\n"))
	}
	return hex.EncodeToString(sum.Sum(nil))
}
