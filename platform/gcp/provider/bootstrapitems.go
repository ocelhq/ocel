package gcp

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
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

type item struct {
	Kind      Kind
	Name      string
	Note      string
	Slow      bool
	Shared    bool
	Versioned bool
}

func (i item) ID() string { return string(i.Kind) + "/" + i.Name }

func (i item) phrase() string { return i.kind().noun + " " + i.Name }

func (i item) kind() kind { return kinds[i.Kind] }

func provisioned(target item, emulated bool) bool {
	return !emulated || !target.kind().emulatorSkips
}

func stackItems(names names.Names, tier environment.Tier, emulated bool) []item {
	items := []item{
		{
			Kind: KindDatabase, Name: names.Database(), Shared: true, Slow: true,
			Note: "every record this project stores, and both tiers keep theirs in it",
		},
		{
			Kind: KindBucket, Name: names.Bucket(tier),
			Note: "the artifacts this tier deploys, and the stamp saying what this bootstrap is",
		},
		{
			Kind: KindBucket, Name: names.StateBucket(tier), Versioned: true,
			Note: "the state every stack in this tier writes, versioned so a bad write is recoverable",
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
			Kind: KindServiceAccount, Name: names.RealtimeAccount(tier),
			Note: "the identity this tier's realtime gateways run as, which may read this tier's realtime keys and no other secret",
		},
		{
			Kind: KindRepository, Name: names.Repository(tier),
			Note: "the images this tier runs, and an untagged image lives at least a week",
		},
		{
			Kind: KindGrant, Name: networkUserRole, Shared: true,
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
			Kind: KindRole, Name: names.PartDatabasesRole(), Shared: true,
			Note: "the role a deploy credential is granted, on the condition it names the tier databases a part holds, to make those databases and their indexes, and no more",
		},
		{
			Kind: KindRole, Name: names.ProjectRecordsRole(), Shared: true,
			Note: "the role a deploy credential is granted, on the condition it names them, to make the Firestore database and the key each project's values are kept and sealed in, and no more",
		},
		{
			Kind: KindServiceAccount, Name: names.EnvSourceSyncAccount(tier),
			Note: "the identity the env source sync runs as and Cloud Scheduler calls it as: it reads and writes this tier's records, seals and opens under the tier key and its projects' keys, and calls the sync service alone",
		},
	}
	return slices.DeleteFunc(items, func(each item) bool { return !provisioned(each, emulated) })
}

func parameterItems(names names.Names, tier environment.Tier) []item {
	return []item{{
		Kind: KindSecret, Name: names.PassphraseSecret(tier),
		Note: "the passphrase this tier's state is encrypted under, minted here and never written over",
	}}
}

func bootstrapItems(names names.Names, tier environment.Tier, emulated bool) []item {
	return slices.Concat(stackItems(names, tier, emulated), parameterItems(names, tier))
}

func digestOf(namespace provider.Namespace, items []item) string {
	sum := sha256.New()
	sum.Write([]byte(namespace.String() + "\n"))
	for _, item := range items {
		sum.Write([]byte(item.ID() + "\n"))
	}
	return hex.EncodeToString(sum.Sum(nil))
}
