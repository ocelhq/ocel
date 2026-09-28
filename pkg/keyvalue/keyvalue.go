package keyvalue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type Store interface {
	Read(ctx context.Context, key Key) (Entry, error)

	Write(ctx context.Context, entry Entry) (Revision, error)

	WritePair(ctx context.Context, first, second Entry) error

	Remove(ctx context.Context, key Key, expected Revision) error

	List(ctx context.Context, in Partition, under ...string) ([]Entry, error)
}

var ErrStale = errors.New("the entry moved since it was read")

var ErrNotFound = errors.New("no such entry")

const (
	RootSchema          = "schema"
	RootProjects        = "projects"
	RootStacks          = "stacks"
	RootEnvironments    = "environments"
	RootBootstrap       = "bootstrap"
	RootEdgeStacks      = "edgestacks"
	RootWildcard        = "wildcard"
	RootLedger          = "ledger"
	RootValues          = "values"
	RootValueRefs       = "valuerefs"
	RootConformance     = "conformance"
	RootEnvSources      = "envsources"
	RootEnvSourceStatus = "envsourcestatus"

	RootEnvSourceDigestKey = "envsourcedigestkey"
)

var variableRoots = []string{RootValues, RootValueRefs, RootEnvSources, RootEnvSourceStatus, RootEnvSourceDigestKey}

type Partition struct {
	Tier environment.Tier
	Root string
	Path []string
}

func (p Partition) Key(path ...string) Key {
	return Key{Partition: p, Path: slices.Clone(path)}
}

func (p Partition) HoldsVariables() bool { return slices.Contains(variableRoots, p.Root) }

func (p Partition) String() string {
	return string(p.Tier) + ":" + joined(append([]string{p.Root}, p.Path...))
}

type Key struct {
	Partition Partition
	Path      []string
}

func (k Key) String() string {
	return k.Partition.String() + "|" + joined(k.Path)
}

var segmentEscapes = strings.NewReplacer("%", "%25", "/", "%2F", "|", "%7C")

func joined(segments []string) string {
	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		escaped = append(escaped, segmentEscapes.Replace(segment))
	}
	return strings.Join(escaped, "/")
}

func (k Key) Under(prefix ...string) ([]string, bool) {
	if len(k.Path) <= len(prefix) || !slices.Equal(k.Path[:len(prefix)], prefix) {
		return nil, false
	}
	return k.Path[len(prefix):], true
}

type Revision string

type Entry struct {
	Key      Key
	Value    json.RawMessage
	Revision Revision
}

func RefuseMalformedPartition(p Partition) error {
	switch p.Tier {
	case environment.TierProduction, environment.TierPreview:
	default:
		return refusal.Refuse(refusal.CodeInvalid, "%s names no tier ocel keeps entries for", p)
	}
	if p.Root == "" || slices.Contains(p.Path, "") {
		return refusal.Refuse(refusal.CodeInvalid, "%s has an empty segment", p)
	}
	return nil
}

func RefuseMalformedKey(k Key) error {
	if err := RefuseMalformedPartition(k.Partition); err != nil {
		return err
	}
	if len(k.Path) == 0 || slices.Contains(k.Path, "") {
		return refusal.Refuse(refusal.CodeInvalid, "%s names no entry in its partition, or has an empty segment", k)
	}
	return nil
}

func RefuseNonJSON(entry Entry) error {
	if !json.Valid(entry.Value) {
		return refusal.Refuse(refusal.CodeInvalid, "%s is not written as JSON, and every entry is kept as JSON text", entry.Key)
	}
	return nil
}

func ReadOrEmpty(ctx context.Context, store Store, key Key) (Entry, error) {
	recorded, err := store.Read(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return Entry{Key: key}, nil
	}
	if err != nil {
		return Entry{}, err
	}
	recorded.Key = key
	return recorded, nil
}

func Forget(ctx context.Context, store Store, key Key) error {
	for range forgetAttempts {
		recorded, err := store.Read(ctx, key)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		err = store.Remove(ctx, key, recorded.Revision)
		if err == nil || errors.Is(err, ErrNotFound) {
			return nil
		}
		if !errors.Is(err, ErrStale) {
			return err
		}
	}
	return fmt.Errorf(
		"%s was rewritten between every read of it and the removal that followed, %d times over. "+
			"Something is still writing that entry; removing it now would drop a write nobody has seen",
		key, forgetAttempts)
}

const forgetAttempts = 5
