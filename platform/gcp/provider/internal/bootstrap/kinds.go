package bootstrap

import (
	"context"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
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
	KindQueue          Kind = "cloudtasks:queue"
	KindTierTag        Kind = "resourcemanager:tagkey"
	KindLogView        Kind = "logging:view"

	KindSQLInstance          Kind = "sqladmin:instance"
	KindMemorystoreInstance  Kind = "memorystore:instance"
	KindTopic                Kind = "pubsub:topic"
	KindSubscription         Kind = "pubsub:subscription"
	KindBackendService       Kind = "compute:backendservice"
	KindNetworkEndpointGroup Kind = "compute:networkendpointgroup"
	KindCertificate          Kind = "certificatemanager:certificate"
	KindCertificateMapEntry  Kind = "certificatemanager:certificatemapentry"
)

const LocationGlobal = "global"

type KindOps struct {
	noun          string
	terraform     string
	properties    func(region string, target Resource) map[string]any
	emulatorSkips bool
	emulatorKeeps string
	parameter     bool
	replaceable   bool

	survey  func(ctx context.Context, b Engine, read Survey, target Resource) (Presence, error)
	Make    func(ctx context.Context, b Engine, read Survey, target Resource) error
	mend    func(ctx context.Context, b Engine, read Survey, target Resource) error
	removal func(read Survey, target Resource) Removal
	leave   func(ctx context.Context, b Engine, read Survey, taking Removal) error
	Take    func(ctx context.Context, b Engine, read Survey, target Resource) error
	taken   func(r Removal) string
	tagged  func(ctx context.Context, b Engine, read Survey, target Resource) (gcloud.TaggedResource, bool, error)

	taggedAtCreation bool
}

var kinds map[Kind]KindOps

func init() {
	kinds = map[Kind]KindOps{
		KindDatabase:       databaseKind,
		KindBucket:         bucketKind,
		KindKeyRing:        keyRingKind,
		KindKey:            keyKind,
		KindSecret:         secretKind,
		KindRepository:     repositoryKind,
		KindServiceAccount: accountKind,
		KindRole:           roleKind,
		KindQueue:          queueKind,
		KindTierTag:        tierTagKind,
		KindLogView:        logViewKind,
		KindService:        {noun: "Cloud Run service", terraform: cost.CloudRunServiceType},
		KindSchedule:       {noun: "Cloud Scheduler job", terraform: cost.SchedulerJobType},
	}
}

var kindAPIs = map[Kind]string{
	KindDatabase:       FirestoreAPI,
	KindBucket:         storageAPI,
	KindKeyRing:        kmsAPI,
	KindKey:            kmsAPI,
	KindSecret:         SecretManagerAPI,
	KindRepository:     repositoryAPI,
	KindServiceAccount: IAMAPI,
	KindRole:           IAMAPI,
	KindQueue:          "cloudtasks.googleapis.com",
	KindTierTag:        resourceManagerAPI,
	KindLogView:        loggingAPI,
	KindService:        "run.googleapis.com",
	KindSchedule:       "cloudscheduler.googleapis.com",
}

func (i Resource) api() string { return kindAPIs[i.Kind] }

func (k KindOps) mendOrMake(ctx context.Context, b Engine, read Survey, target Resource) error {
	if k.mend != nil {
		return k.mend(ctx, b, read, target)
	}
	return k.Make(ctx, b, read, target)
}

func (i Resource) Shape(region string) (pricing.Shaped, bool) {
	k := i.KindOps()
	if k.terraform == "" {
		return pricing.Shaped{}, false
	}
	properties := map[string]any{}
	if k.properties != nil {
		properties = k.properties(region, i)
	}
	return pricing.Shaped{Name: i.Name, Type: k.terraform, Properties: properties}, true
}

func kept(target Resource, reason string) Removal {
	return Removal{Resource: target, Action: provider.ActionKeep, Reason: reason}
}
