package gcp

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
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

type kind struct {
	noun          string
	terraform     string
	properties    func(region string, target item) map[string]any
	emulatorSkips bool
	emulatorKeeps string
	parameter     bool

	survey  func(ctx context.Context, b bootstrap, tier environment.Tier, target item) (presence, error)
	make    func(ctx context.Context, b bootstrap, read survey, target item) error
	mend    func(ctx context.Context, b bootstrap, read survey, target item) error
	removal func(read survey, target item) removal
	leave   func(ctx context.Context, b bootstrap, read survey, taking removal) error
	take    func(ctx context.Context, b bootstrap, read survey, target item) (time.Duration, error)
	taken   func(r removal) string
}

var kinds map[Kind]kind

func init() {
	kinds = map[Kind]kind{
		KindDatabase:       databaseKind,
		KindBucket:         bucketKind,
		KindKeyRing:        keyRingKind,
		KindKey:            keyKind,
		KindSecret:         secretKind,
		KindRepository:     repositoryKind,
		KindServiceAccount: accountKind,
		KindRole:           roleKind,
		KindGrant:          grantKind,
		KindQueue:          queueKind,
		KindService:        {noun: "Cloud Run service", terraform: tfCloudRunService},
		KindSchedule:       {noun: "Cloud Scheduler job", terraform: tfSchedulerJob},
	}
}

func (k kind) mendOrMake(ctx context.Context, b bootstrap, read survey, target item) error {
	if k.mend != nil {
		return k.mend(ctx, b, read, target)
	}
	return k.make(ctx, b, read, target)
}

func (i item) shape(region string) (pricing.Shaped, bool) {
	k := i.kind()
	if k.terraform == "" {
		return pricing.Shaped{}, false
	}
	properties := map[string]any{}
	if k.properties != nil {
		properties = k.properties(region, i)
	}
	return pricing.Shaped{Name: i.Name, Type: k.terraform, Properties: properties}, true
}

func kept(target item, reason string) removal {
	return removal{item: target, action: provider.ActionKeep, reason: reason}
}
