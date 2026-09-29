package keyvalue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
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

type Root string

const (
	RootSchema             Root = "schema"
	RootProjects           Root = "projects"
	RootStacks             Root = "stacks"
	RootEnvironments       Root = "environments"
	RootBootstrap          Root = "bootstrap"
	RootEdgeStacks         Root = "edgestacks"
	RootWildcard           Root = "wildcard"
	RootLedger             Root = "ledger"
	RootRouters            Root = "routers"
	RootValues             Root = "values"
	RootValueRefs          Root = "valuerefs"
	RootConformance        Root = "conformance"
	RootEnvSources         Root = "envsources"
	RootEnvSourceStatus    Root = "envsourcestatus"
	RootEnvSourceDigestKey Root = "envsourcedigestkey"
	RootSharedPhysicals    Root = "sharedphysicals"
)

var variableRoots = []Root{RootValues, RootValueRefs, RootEnvSources, RootEnvSourceStatus, RootEnvSourceDigestKey}

type Partition struct {
	Tier environment.Tier
	Root Root
	Path []string
}

func (p Partition) Key(path ...string) Key {
	return Key{Partition: p, Path: slices.Clone(path)}
}

func (p Partition) Segments() []string {
	return append([]string{string(p.Root)}, p.Path...)
}

func (p Partition) HoldsVariables() bool { return slices.Contains(variableRoots, p.Root) }

func (p Partition) String() string {
	return string(p.Tier) + ":" + JoinSegments(p.Segments(), "/", "|")
}

type Key struct {
	Partition Partition
	Path      []string
}

func (k Key) String() string {
	return k.Partition.String() + "|" + JoinSegments(k.Path, "/", "|")
}

func JoinSegments(segments []string, separator, reserved string) string {
	isPlain := func(c byte) bool { return strings.IndexByte(separator+reserved, c) < 0 }
	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		escaped = append(escaped, EscapeSegment(segment, isPlain))
	}
	return strings.Join(escaped, separator)
}

func SplitSegments(joined, separator string) ([]string, error) {
	segments := strings.Split(joined, separator)
	for i, segment := range segments {
		unescaped, err := UnescapeSegment(segment)
		if err != nil {
			return nil, err
		}
		segments[i] = unescaped
	}
	return segments, nil
}

func EscapeSegment(segment string, isPlain func(c byte) bool) string {
	var escaped strings.Builder
	for i := 0; i < len(segment); i++ {
		if segment[i] == '%' || !isPlain(segment[i]) {
			fmt.Fprintf(&escaped, "%%%02X", segment[i])
			continue
		}
		escaped.WriteByte(segment[i])
	}
	return escaped.String()
}

func UnescapeSegment(segment string) (string, error) {
	var unescaped strings.Builder
	for i := 0; i < len(segment); i++ {
		if segment[i] != '%' {
			unescaped.WriteByte(segment[i])
			continue
		}
		if i+2 >= len(segment) {
			return "", refuseUnescaped(segment)
		}
		value, err := strconv.ParseUint(segment[i+1:i+3], 16, 8)
		if err != nil {
			return "", refuseUnescaped(segment)
		}
		unescaped.WriteByte(byte(value))
		i += 2
	}
	return unescaped.String(), nil
}

func refuseUnescaped(segment string) error {
	return refusal.Refuse(refusal.CodeDenied, "%q holds a %% that escapes no byte, so ocel did not write it", segment)
}

const revisionBytes = 16

func NewRevision() (Revision, error) {
	token := make([]byte, revisionBytes)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("mint a revision token: %w", err)
	}
	return Revision(hex.EncodeToString(token)), nil
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

func RefuseUnwritable(entry Entry) error {
	if err := RefuseMalformedKey(entry.Key); err != nil {
		return err
	}
	return refuseNonJSON(entry)
}

func refuseNonJSON(entry Entry) error {
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
