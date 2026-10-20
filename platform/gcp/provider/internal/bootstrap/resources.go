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
	"secretmanager.googleapis.com",
	"artifactregistry.googleapis.com",
	"iam.googleapis.com",
	"cloudresourcemanager.googleapis.com",
	"logging.googleapis.com",
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
			Kind: KindTierTag, Name: tierTagOf().Name, Shared: true,
			Note: "the tag every resource of a tier carries its tier's value of, production or preview, which fences a tier's deploy credential to that tier's resources",
		},
		{
			Kind: KindDatabase, Name: names.RecordsDatabase(tier), Slow: true,
			Note: "every record this tier stores, carrying the tier's tag so no other tier's credential reaches it",
		},
		{
			Kind: KindBucket, Name: names.TierBucketStem(tier, naming.StorageState), Class: naming.StorageState,
			Note: fmt.Sprintf("the state every stack in this tier writes and the stamp saying what this bootstrap is, versioned so a bad write is recoverable for %d days", NoncurrentStateLifetimeDays),
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
			Kind: KindRepository, Name: names.Repository(tier),
			Note: "the images this tier runs, and an untagged image lives at least a week",
		},
		{
			Kind: KindLogView, Name: names.LogView(tier),
			Note: "the view of this tier's Cloud Run entries on the project's _Default log bucket, the only logs this tier's deploy credential may read",
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
