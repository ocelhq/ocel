package bootstrap

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

type state string

const (
	stateApplying state = "applying"
	stateRemoving state = "removing"
	StateComplete state = "complete"
)

type Stamp struct {
	State  state    `json:"state"`
	Writer string   `json:"writer"`
	Digest string   `json:"digest"`
	Parts  []string `json:"parts,omitempty"`
}

type Survey struct {
	Tier       environment.Tier
	Names      names.Names
	Project    string
	Region     string
	Present    bool
	Stamp      Stamp
	Generation int64
	Emulated   bool

	Held      map[string]bool
	Mending   map[string]string
	Replacing map[string]string
	Sibling   bool

	Buckets naming.Buckets
}

func (s Survey) has(target Resource) bool { return s.Held[target.ID()] }

func (s Survey) named(target Resource) string {
	if bucket := s.Buckets[target.Class]; target.Kind == KindBucket && bucket != "" {
		return bucket
	}
	return target.Name
}

func (s Survey) mends(target Resource) string { return s.Mending[target.ID()] }

func (s Survey) replaces(target Resource) string { return s.Replacing[target.ID()] }

func (s Survey) current(items []Resource) bool {
	for _, item := range items {
		if !s.has(item) || s.mends(item) != "" {
			return false
		}
	}
	return true
}

func (b Engine) newSurvey(tier environment.Tier) Survey {
	return Survey{
		Tier:      tier,
		Names:     b.Clients.Names,
		Project:   b.Clients.Project(),
		Region:    b.Clients.Region,
		Emulated:  b.Clients.Emulated(),
		Held:      map[string]bool{},
		Mending:   map[string]string{},
		Replacing: map[string]string{},
	}
}

const surveyReadsAtOnce = 8

func runReadsAtOnce(ctx context.Context, reads ...func(context.Context) error) error {
	failed := make([]error, len(reads))
	var group errgroup.Group
	group.SetLimit(surveyReadsAtOnce)
	for i, read := range reads {
		group.Go(func() error {
			failed[i] = read(ctx)
			return nil
		})
	}
	_ = group.Wait()
	return cmp.Or(failed...)
}

func (b Engine) surveyPresence(ctx context.Context, read Survey, items []Resource) error {
	found := make([]Presence, len(items))
	reads := make([]func(context.Context) error, len(items))
	for i, item := range items {
		reads[i] = func(ctx context.Context) (err error) {
			found[i], err = b.PresenceOf(ctx, read, item)
			return err
		}
	}
	if err := runReadsAtOnce(ctx, reads...); err != nil {
		return err
	}
	for i, item := range items {
		read.Held[item.ID()] = found[i].Present
		if found[i].Mends != "" {
			read.Mending[item.ID()] = found[i].Mends
		}
		if found[i].Replaces != "" {
			read.Replacing[item.ID()] = found[i].Replaces
		}
	}
	return nil
}

func (b Engine) survey(ctx context.Context, tier environment.Tier) (Survey, error) {
	read := b.newSurvey(tier)
	var err error
	if read.Buckets, err = b.recordedBuckets(ctx, tier); err != nil {
		return Survey{}, err
	}
	stateBucket := read.Buckets[naming.StorageState]
	var sibling, own stamped
	err = runReadsAtOnce(ctx,
		func(ctx context.Context) error {
			buckets, err := b.recordedBuckets(ctx, tier.Sibling())
			if err != nil {
				return err
			}
			sibling, err = b.stamped(ctx, buckets[naming.StorageState])
			return err
		},
		func(ctx context.Context) error {
			return b.surveyPresence(ctx, read, Resources(read.Names, tier, read.Emulated))
		},
		func(ctx context.Context) (err error) {
			own, err = b.stamped(ctx, stateBucket)
			return err
		},
	)
	if err != nil {
		return Survey{}, err
	}
	read.Sibling = sibling.present
	read.Present, read.Stamp, read.Generation = own.present, own.stamp, own.generation
	return read, nil
}

func (b Engine) recordedBuckets(ctx context.Context, tier environment.Tier) (naming.Buckets, error) {
	database, err := b.databasePresence(ctx, b.Clients.RecordsDatabase(tier))
	if err != nil || !database.Present {
		return naming.Buckets{}, err
	}
	return b.Clients.RecordedTierBuckets(ctx, tier)
}

type stamped struct {
	present    bool
	stamp      Stamp
	generation int64
}

func (b Engine) stamped(ctx context.Context, bucket string) (stamped, error) {
	if bucket == "" {
		return stamped{}, nil
	}
	client, err := b.Clients.Storage()
	if err != nil {
		return stamped{}, err
	}
	reader, err := client.Bucket(bucket).Object(StampObject).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) || errors.Is(err, storage.ErrBucketNotExist) || gcloud.Absent(err) {
		return stamped{}, nil
	}
	if err != nil {
		return stamped{}, fmt.Errorf("read %s in %s: %w", StampObject, bucket, err)
	}
	defer reader.Close()
	written, err := io.ReadAll(reader)
	if err != nil {
		return stamped{}, fmt.Errorf("read %s in %s: %w", StampObject, bucket, err)
	}
	var read Stamp
	if err := json.Unmarshal(written, &read); err != nil {
		return stamped{}, fmt.Errorf("read %s in %s: %w", StampObject, bucket, err)
	}
	return stamped{present: true, stamp: read, generation: reader.Attrs.Generation}, nil
}

type Presence struct {
	Present  bool
	Mends    string
	Replaces string
}

func (b Engine) PresenceOf(ctx context.Context, read Survey, target Resource) (Presence, error) {
	surveyed := target.KindOps().survey
	if surveyed == nil {
		return Presence{}, refusal.Refuse(refusal.CodeInvalid, "gcp: nothing surveys a %s", target.Kind)
	}
	found, err := surveyed(ctx, b, read, target)
	if err != nil {
		return Presence{}, err
	}
	return b.untaggedPresence(ctx, read, target, found)
}

func presenceFrom(present bool, err error) (Presence, error) { return Presence{Present: present}, err }
