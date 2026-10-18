package gcp

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
	stateComplete state = "complete"
)

type stamp struct {
	State  state    `json:"state"`
	Writer string   `json:"writer"`
	Digest string   `json:"digest"`
	Parts  []string `json:"parts,omitempty"`
}

type survey struct {
	Tier       environment.Tier
	Names      names.Names
	Project    string
	Region     string
	Present    bool
	Stamp      stamp
	Generation int64
	Emulated   bool

	present   map[string]bool
	mending   map[string]string
	replacing map[string]string
	sibling   bool
	stateOf   string
}

func (s survey) has(target item) bool { return s.present[target.ID()] }

func (s survey) mends(target item) string { return s.mending[target.ID()] }

func (s survey) replaces(target item) string { return s.replacing[target.ID()] }

func (s survey) current(items []item) bool {
	for _, item := range items {
		if !s.has(item) || s.mends(item) != "" {
			return false
		}
	}
	return true
}

func (b bootstrap) survey(ctx context.Context, tier environment.Tier) (survey, error) {
	read := survey{
		Tier:      tier,
		Names:     b.clients.Names,
		Project:   b.clients.Project(),
		Region:    b.clients.Region,
		Emulated:  b.clients.Emulated(),
		present:   map[string]bool{},
		mending:   map[string]string{},
		replacing: map[string]string{},
	}
	for _, item := range bootstrapItems(read.Names, tier, read.Emulated) {
		found, err := b.presenceOf(ctx, tier, item)
		if err != nil {
			return survey{}, err
		}
		read.present[item.ID()] = found.present
		if found.mends != "" {
			read.mending[item.ID()] = found.mends
		}
		if found.replaces != "" {
			read.replacing[item.ID()] = found.replaces
		}
	}

	sibling, err := b.stamped(ctx, read.Names.Bucket(tier.Sibling()))
	if err != nil {
		return survey{}, err
	}
	read.sibling = sibling.present

	own, err := b.stamped(ctx, read.Names.Bucket(tier))
	if err != nil {
		return survey{}, err
	}
	read.Present, read.Stamp, read.Generation = own.present, own.stamp, own.generation

	if read.stateOf, err = b.stateIn(ctx, read.Names.StateBucket(tier)); err != nil {
		return survey{}, err
	}
	return read, nil
}

type stamped struct {
	present    bool
	stamp      stamp
	generation int64
}

func (b bootstrap) stamped(ctx context.Context, bucket string) (stamped, error) {
	client, err := b.clients.Storage()
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
	var read stamp
	if err := json.Unmarshal(written, &read); err != nil {
		return stamped{}, fmt.Errorf("read %s in %s: %w", StampObject, bucket, err)
	}
	return stamped{present: true, stamp: read, generation: reader.Attrs.Generation}, nil
}

const stateRoot = ".pulumi/stacks/"

func (b bootstrap) stateIn(ctx context.Context, bucket string) (string, error) {
	client, err := b.clients.Storage()
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

type presence struct {
	present  bool
	mends    string
	replaces string
}

func (b bootstrap) presenceOf(ctx context.Context, tier environment.Tier, target item) (presence, error) {
	surveyed := target.kind().survey
	if surveyed == nil {
		return presence{}, refusal.Refuse(refusal.CodeInvalid, "gcp: nothing surveys a %s", target.Kind)
	}
	return surveyed(ctx, b, tier, target)
}

func presenceFrom(present bool, err error) (presence, error) { return presence{present: present}, err }
