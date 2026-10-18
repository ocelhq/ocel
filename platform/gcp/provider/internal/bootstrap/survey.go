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
}

func (s Survey) has(target Resource) bool { return s.Held[target.ID()] }

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

func (b Engine) survey(ctx context.Context, tier environment.Tier) (Survey, error) {
	read := Survey{
		Tier:      tier,
		Names:     b.Clients.Names,
		Project:   b.Clients.Project(),
		Region:    b.Clients.Region,
		Emulated:  b.Clients.Emulated(),
		Held:      map[string]bool{},
		Mending:   map[string]string{},
		Replacing: map[string]string{},
	}
	for _, item := range Resources(read.Names, tier, read.Emulated) {
		found, err := b.PresenceOf(ctx, tier, item)
		if err != nil {
			return Survey{}, err
		}
		read.Held[item.ID()] = found.Present
		if found.Mends != "" {
			read.Mending[item.ID()] = found.Mends
		}
		if found.Replaces != "" {
			read.Replacing[item.ID()] = found.Replaces
		}
	}

	sibling, err := b.stamped(ctx, read.Names.Bucket(tier.Sibling()))
	if err != nil {
		return Survey{}, err
	}
	read.Sibling = sibling.present

	own, err := b.stamped(ctx, read.Names.Bucket(tier))
	if err != nil {
		return Survey{}, err
	}
	read.Present, read.Stamp, read.Generation = own.present, own.stamp, own.generation

	if read.stateOf, err = b.stateIn(ctx, read.Names.StateBucket(tier)); err != nil {
		return Survey{}, err
	}
	return read, nil
}

type stamped struct {
	present    bool
	stamp      Stamp
	generation int64
}

func (b Engine) stamped(ctx context.Context, bucket string) (stamped, error) {
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
	client, err := b.Clients.Storage()
	if err != nil {
		return "", err
	}
	attrs, err := client.Bucket(bucket).Objects(ctx, nil).Next()
	if errors.Is(err, iterator.Done) || errors.Is(err, storage.ErrBucketNotExist) || gcloud.Absent(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("list what %s contains: %w", bucket, err)
	}
	return stackNamed(attrs.Name), nil
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

func (b Engine) PresenceOf(ctx context.Context, tier environment.Tier, target Resource) (Presence, error) {
	surveyed := target.KindOps().survey
	if surveyed == nil {
		return Presence{}, refusal.Refuse(refusal.CodeInvalid, "gcp: nothing surveys a %s", target.Kind)
	}
	return surveyed(ctx, b, tier, target)
}

func presenceFrom(present bool, err error) (Presence, error) { return Presence{Present: present}, err }
