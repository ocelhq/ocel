package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const StampObject = "ocel/bootstrap.json"

const (
	FirestoreAPI       = "firestore.googleapis.com"
	StorageAPI         = "storage.googleapis.com"
	kmsAPI             = "cloudkms.googleapis.com"
	IAMAPI             = "iam.googleapis.com"
	SecretManagerAPI   = "secretmanager.googleapis.com"
	resourceManagerAPI = "cloudresourcemanager.googleapis.com"
	repositoryAPI      = "artifactregistry.googleapis.com"
	loggingAPI         = "logging.googleapis.com"
	RunAPI             = "run.googleapis.com"
	schedulerAPI       = "cloudscheduler.googleapis.com"
	queueAPI           = "cloudtasks.googleapis.com"
	pubsubAPI          = "pubsub.googleapis.com"
)

var BootstrapAPIs = []string{
	FirestoreAPI,
	StorageAPI,
	SecretManagerAPI,
	repositoryAPI,
	IAMAPI,
	resourceManagerAPI,
	loggingAPI,
}

type Resource struct {
	Kind   Kind
	Name   string
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
		{Kind: KindTierTag, Name: tierTagOf().Name, Shared: true},
		{Kind: KindDatabase, Name: names.RecordsDatabase(tier), Slow: true},
		{Kind: KindBucket, Name: names.TierBucketStem(tier, naming.StorageState), Class: naming.StorageState},
		{Kind: KindRole, Name: names.CDNPurgeRole(), Shared: true},
		{Kind: KindRole, Name: names.PartsRole(), Shared: true},
		{Kind: KindRepository, Name: names.Repository(tier)},
		{Kind: KindLogView, Name: names.LogView(tier)},
	}
	return slices.DeleteFunc(items, func(each Resource) bool { return !provisioned(each, emulated) })
}

func ParameterResources(names names.Names, tier environment.Tier) []Resource {
	return []Resource{{Kind: KindSecret, Name: names.PassphraseSecret(tier)}}
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
