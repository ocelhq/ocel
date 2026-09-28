package boxstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	Dir             = "/usr/local/lib/ocel"
	KeyValuesHelper = Dir + "/keyvalues"
	SealHelper      = Dir + "/seal"
)

const (
	ExitNotFound = 3
	ExitStale    = 4
)

const (
	revisionWidth = 32
	revisionHex   = "0123456789abcdef"
	acknowledged  = "removed"
)

type KeyValueTransport interface {
	HasStore(ctx context.Context, tier environment.Tier) (bool, error)

	Run(ctx context.Context, tier environment.Tier, stdin io.Reader, argv ...string) (string, error)
}

type KeyValues struct{ over KeyValueTransport }

func NewKeyValues(over KeyValueTransport) *KeyValues { return &KeyValues{over: over} }

var errUnprovisioned = errors.New("this host keeps no store for the tier")

func refuseUnbootstrapped(tier environment.Tier) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"this host has no ocel bootstrap\nRun `%s`",
		provider.BootstrapCommand(tier))
}

func (s *KeyValues) run(ctx context.Context, tier environment.Tier, unprovisioned error, stdin io.Reader, argv ...string) (string, error) {
	provisioned, err := s.over.HasStore(ctx, tier)
	if err != nil {
		return "", err
	}
	if !provisioned {
		return "", unprovisioned
	}
	return s.over.Run(ctx, tier, stdin, argv...)
}

func (s *KeyValues) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	path, err := live.PathOf(key)
	if err != nil {
		return keyvalue.Entry{}, err
	}
	rendered, err := s.run(ctx, key.Partition.Tier, keyvalue.ErrNotFound, nil, "read", path)
	if err != nil {
		return keyvalue.Entry{}, err
	}
	revision, value, split := strings.Cut(strings.TrimRight(rendered, "\n"), "\t")
	if !split {
		return keyvalue.Entry{}, refusal.Refuse(refusal.CodeDenied,
			"the key-value helper answered a read with %q", rendered)
	}
	return entryOf(key, revision, value)
}

func (s *KeyValues) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	path, line, err := encodeEntry(entry)
	if err != nil {
		return "", err
	}
	tier := entry.Key.Partition.Tier
	rendered, err := s.run(ctx, tier, refuseUnbootstrapped(tier), bytes.NewReader(line), "write", path, string(entry.Revision))
	if err != nil {
		return "", err
	}
	return parseRevision(rendered)
}

func (s *KeyValues) WritePair(ctx context.Context, first, second keyvalue.Entry) error {
	one, firstLine, err := encodeEntry(first)
	if err != nil {
		return err
	}
	two, secondLine, err := encodeEntry(second)
	if err != nil {
		return err
	}
	tier := first.Key.Partition.Tier
	if second.Key.Partition.Tier != tier {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s and %s belong to different tiers",
			first.Key, second.Key)
	}
	fed := io.MultiReader(bytes.NewReader(firstLine), bytes.NewReader(secondLine))
	rendered, err := s.run(ctx, tier, refuseUnbootstrapped(tier), fed, "pair", one, string(first.Revision), two, string(second.Revision))
	if err != nil {
		return err
	}
	left, right, split := strings.Cut(strings.TrimSpace(rendered), "\t")
	if !split {
		return refusal.Refuse(refusal.CodeDenied,
			"the key-value helper answered a pair with %q", rendered)
	}
	if _, err := parseRevision(left); err != nil {
		return err
	}
	_, err = parseRevision(right)
	return err
}

func (s *KeyValues) Remove(ctx context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
	path, err := live.PathOf(key)
	if err != nil {
		return err
	}
	rendered, err := s.run(ctx, key.Partition.Tier, keyvalue.ErrNotFound, nil, "remove", path, string(expected))
	if err != nil {
		return err
	}
	if strings.TrimSpace(rendered) != acknowledged {
		return refusal.Refuse(refusal.CodeDenied,
			"the key-value helper did not confirm removing %s", key)
	}
	return nil
}

func (s *KeyValues) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	dir, err := live.PartitionDir(in)
	if err != nil {
		return nil, err
	}
	argv := []string{"list", dir}
	if len(under) > 0 {
		prefix, err := live.PathOf(in.Key(under...))
		if err != nil {
			return nil, err
		}
		argv = append(argv, strings.TrimPrefix(prefix, dir+"/"))
	}
	rendered, err := s.run(ctx, in.Tier, errUnprovisioned, nil, argv...)
	if errors.Is(err, errUnprovisioned) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found []keyvalue.Entry
	for _, line := range strings.Split(strings.TrimSpace(rendered), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		columns := strings.SplitN(line, "\t", 3)
		if len(columns) != 3 {
			return nil, refusal.Refuse(refusal.CodeDenied,
				"the key-value helper listed a row ocel cannot read: %q", line)
		}
		key, err := live.KeyOf(in, columns[0])
		if err != nil {
			return nil, err
		}
		entry, err := entryOf(key, columns[1], columns[2])
		if err != nil {
			return nil, err
		}
		found = append(found, entry)
	}
	return found, nil
}

func encodeEntry(entry keyvalue.Entry) (string, []byte, error) {
	path, err := live.PathOf(entry.Key)
	if err != nil {
		return "", nil, err
	}
	if err := keyvalue.RefuseNonJSON(entry); err != nil {
		return "", nil, err
	}
	var line bytes.Buffer
	if err := json.Compact(&line, entry.Value); err != nil {
		return "", nil, err
	}
	return path, append(line.Bytes(), '\n'), nil
}

func entryOf(key keyvalue.Key, revision, value string) (keyvalue.Entry, error) {
	if !json.Valid([]byte(value)) {
		return keyvalue.Entry{}, refusal.Refuse(refusal.CodeDenied, "%s is not an entry ocel wrote", key)
	}
	parsed, err := parseRevision(revision)
	if err != nil {
		return keyvalue.Entry{}, err
	}
	return keyvalue.Entry{Key: key, Value: []byte(value), Revision: parsed}, nil
}

func parseRevision(rendered string) (keyvalue.Revision, error) {
	revision := strings.TrimSpace(rendered)
	if len(revision) != revisionWidth || strings.Trim(revision, revisionHex) != "" {
		return "", refusal.Refuse(refusal.CodeDenied,
			"the key-value helper answered %q, not a revision", rendered)
	}
	return keyvalue.Revision(revision), nil
}

var _ keyvalue.Store = (*KeyValues)(nil)
