package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

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
	stateOf   string

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

func (b Engine) surveyPresence(ctx context.Context, read Survey, items []Resource) error {
	for _, item := range items {
		found, err := b.PresenceOf(ctx, read, item)
		if err != nil {
			return err
		}
		read.Held[item.ID()] = found.Present
		if found.Mends != "" {
			read.Mending[item.ID()] = found.Mends
		}
		if found.Replaces != "" {
			read.Replacing[item.ID()] = found.Replaces
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
	sibling, err := b.recordedBuckets(ctx, tier.Sibling())
	if err != nil {
		return Survey{}, err
	}
	if err := b.surveyPresence(ctx, read, Resources(read.Names, tier, read.Emulated)); err != nil {
		return Survey{}, err
	}

	stampedSibling, err := b.stamped(ctx, sibling[naming.StorageState])
	if err != nil {
		return Survey{}, err
	}
	read.Sibling = stampedSibling.present

	own, err := b.stamped(ctx, read.Buckets[naming.StorageState])
	if err != nil {
		return Survey{}, err
	}
	read.Present, read.Stamp, read.Generation = own.present, own.stamp, own.generation

	if read.stateOf, err = b.stateIn(ctx, read.Buckets[naming.StorageState]); err != nil {
		return Survey{}, err
	}
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

const stateRoot = ".pulumi/stacks/"

func (b Engine) stateIn(ctx context.Context, bucket string) (string, error) {
	if bucket == "" {
		return "", nil
	}
	client, err := b.Clients.Storage()
	if err != nil {
		return "", err
	}
	objects := client.Bucket(bucket).Objects(ctx, nil)
	for {
		attrs, err := objects.Next()
		if errors.Is(err, iterator.Done) || errors.Is(err, storage.ErrBucketNotExist) || gcloud.Absent(err) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("list what %s contains: %w", bucket, err)
		}
		if attrs.Name != StampObject {
			return stackNamed(attrs.Name), nil
		}
	}
}

func stackNamed(object string) string {
	name := strings.TrimPrefix(object, stateRoot)
	name = strings.TrimSuffix(name, ".json")
	if name == "" {
		return object
	}
	return name
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
