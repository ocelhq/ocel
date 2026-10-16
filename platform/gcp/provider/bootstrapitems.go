package gcp

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Kind string

const (
	KindDatabase       Kind = "firestore:database"
	KindBucket         Kind = "storage:bucket"
	KindKeyRing        Kind = "kms:keyring"
	KindKey            Kind = "kms:key"
	KindSecret         Kind = "secretmanager:secret"
	KindRepository     Kind = "artifactregistry:repository"
	KindServiceAccount Kind = "iam:serviceaccount"
	KindRole           Kind = "iam:role"
	KindService        Kind = "run:service"
	KindSchedule       Kind = "cloudscheduler:job"
	KindGrant          Kind = "resourcemanager:grant"
	KindQueue          Kind = "cloudtasks:queue"
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

var kindNouns = map[Kind]string{
	KindDatabase:       "Firestore database",
	KindBucket:         "bucket",
	KindKeyRing:        "KMS key ring",
	KindKey:            "KMS key",
	KindSecret:         "secret",
	KindRepository:     "Artifact Registry repository",
	KindServiceAccount: "service account",
	KindRole:           "custom role",
	KindGrant:          "project grant of",
	KindQueue:          "Cloud Tasks queue",
}

func (i item) phrase() string { return kindNouns[i.Kind] + " " + i.Name }

func provisioned(kind Kind, emulated bool) bool {
	return !emulated || (kind != KindRepository && kind != KindRole && kind != KindGrant)
}

func stackItems(names Names, tier environment.Tier, emulated bool) []item {
	items := []item{
		{
			Kind: KindDatabase, Name: names.Database(), Shared: true, Slow: true,
			Note: "every record this project stores, and both tiers keep theirs in it",
		},
		{
			Kind: KindDatabase, Name: names.TagDatabase(tier), Slow: true,
			Note: "the tag records of every Next app this tier serves: one document per revalidated tag, read by every instance of the app",
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
			Kind: KindServiceAccount, Name: names.EnvSourceSyncAccount(tier),
			Note: "the identity the env source sync runs as and Cloud Scheduler calls it as: it reads and writes this project's records, seals and opens under the tier key, and calls the sync service alone",
		},
	}
	return slices.DeleteFunc(items, func(each item) bool { return !provisioned(each.Kind, emulated) })
}

func parameterItems(names Names, tier environment.Tier) []item {
	return []item{{
		Kind: KindSecret, Name: names.PassphraseSecret(tier),
		Note: "the passphrase this tier's state is encrypted under, minted here and never written over",
	}}
}

func bootstrapItems(names Names, tier environment.Tier, emulated bool) []item {
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
