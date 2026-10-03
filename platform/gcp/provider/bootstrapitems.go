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
	KindService        Kind = "run:service"
	KindSchedule       Kind = "cloudscheduler:job"
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
}

func (i item) phrase() string { return kindNouns[i.Kind] + " " + i.Name }

func provisioned(kind Kind, emulated bool) bool { return !emulated || kind != KindRepository }

func stackItems(names Names, tier environment.Tier, emulated bool) []item {
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
			Kind: KindServiceAccount, Name: names.WorkloadAccount(tier),
			Note: "the identity every app in this tier runs as, and the one the deploy hands Cloud Run",
		},
		{
			Kind: KindServiceAccount, Name: names.RealtimeAccount(tier),
			Note: "the identity this tier's realtime gateways run as: it holds no role in the project, and each deploy lets it read its environment's realtime keys alone",
		},
		{
			Kind: KindRepository, Name: names.Repository(tier),
			Note: "the images this tier runs, and an untagged image lives at least a week",
		},
		{
			Kind: KindServiceAccount, Name: names.EnvSourceSyncAccount(tier),
			Note: "the identity the env source sync runs as and Cloud Scheduler calls it as: it reads and writes this project's records, seals and opens under the tier key, and calls the sync service alone",
		},
		{
			Kind: KindService, Name: names.EnvSourceSync(tier),
			Note: "the env source sync: each call syncs every scheduled env source this tier registers once, and it bills only while a sync runs",
		},
		{
			Kind: KindSchedule, Name: names.EnvSourceSync(tier),
			Note: "calls the env source sync once a minute, with no retry because the next minute is the retry",
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
