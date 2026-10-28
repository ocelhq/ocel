package boxstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/tracing/content"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const agentMissing = 127

type KeyValues struct{ box *ssh.Box }

func NewKeyValues(box *ssh.Box) *KeyValues { return &KeyValues{box: box} }

func refuseUnbootstrapped(tier environment.Tier) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"this host has no ocel bootstrap\nRun `%s`",
		provider.BootstrapCommand(tier))
}

func (s *KeyValues) run(ctx context.Context, tier environment.Tier, unbootstrapped error, stdin io.Reader, argv ...string) (string, error) {
	command := ssh.LiveCommand{
		Fixed: []string{boxapi.LiveBinary, boxapi.RecordsCommand},
		Rest:  slices.Concat([]string{string(tier)}, argv),
	}
	result, err := s.box.StreamLive(content.Omit(ctx), ssh.RootWhenAble, "records "+argv[0], command, stdin)
	if err != nil {
		return "", err
	}
	switch result.Code {
	case 0:
		return result.Stdout, nil
	case boxapi.ExitNotFound:
		return "", records.ErrNotFound
	case boxapi.ExitStale:
		return "", records.ErrStale
	case agentMissing:
		return "", unbootstrapped
	default:
		return "", s.box.Refuse("records "+argv[0], result)
	}
}

func (s *KeyValues) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	path, err := boxapi.PathOf(key)
	if err != nil {
		return records.Entry{}, err
	}
	rendered, err := s.run(ctx, key.Partition.Tier, records.ErrNotFound, nil, boxapi.RecordsVerbGet, path)
	if err != nil {
		return records.Entry{}, err
	}
	var file boxapi.EntryFile
	if err := json.Unmarshal([]byte(rendered), &file); err != nil {
		return records.Entry{}, unreadable(boxapi.RecordsVerbGet, rendered)
	}
	return entryOf(key, file.Revision, file.Value)
}

func (s *KeyValues) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	revisions, err := s.compareAndSet(ctx, entry.Key.Partition.Tier, entry)
	if err != nil {
		return "", err
	}
	return revisions[0], nil
}

func (s *KeyValues) WritePair(ctx context.Context, first, second records.Entry) error {
	if second.Key.Partition.Tier != first.Key.Partition.Tier {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s and %s belong to different tiers",
			first.Key, second.Key)
	}
	_, err := s.compareAndSet(ctx, first.Key.Partition.Tier, first, second)
	return err
}

func (s *KeyValues) compareAndSet(ctx context.Context, tier environment.Tier, entries ...records.Entry) ([]records.Revision, error) {
	changes := make([]boxapi.Change, 0, len(entries))
	for _, entry := range entries {
		if err := records.RefuseUnwritable(entry); err != nil {
			return nil, err
		}
		path, err := boxapi.PathOf(entry.Key)
		if err != nil {
			return nil, err
		}
		var value bytes.Buffer
		if err := json.Compact(&value, entry.Value); err != nil {
			return nil, err
		}
		changes = append(changes, boxapi.Change{Path: path, Revision: entry.Revision, Value: value.Bytes()})
	}
	return s.send(ctx, tier, refuseUnbootstrapped(tier), changes)
}

func (s *KeyValues) send(ctx context.Context, tier environment.Tier, unbootstrapped error, changes []boxapi.Change) ([]records.Revision, error) {
	fed, err := json.Marshal(changes)
	if err != nil {
		return nil, err
	}
	rendered, err := s.run(ctx, tier, unbootstrapped, bytes.NewReader(fed), boxapi.RecordsVerbCompareAndSet)
	if err != nil {
		return nil, err
	}
	var revisions []records.Revision
	if err := json.Unmarshal([]byte(rendered), &revisions); err != nil || len(revisions) != len(changes) {
		return nil, unreadable(boxapi.RecordsVerbCompareAndSet, rendered)
	}
	for at, change := range changes {
		if change.Remove {
			continue
		}
		if _, err := parseRevision(string(revisions[at])); err != nil {
			return nil, err
		}
	}
	return revisions, nil
}

func (s *KeyValues) Remove(ctx context.Context, key records.Key, expected records.Revision) error {
	path, err := boxapi.PathOf(key)
	if err != nil {
		return err
	}
	_, err = s.send(ctx, key.Partition.Tier, records.ErrNotFound, []boxapi.Change{{Path: path, Revision: expected, Remove: true}})
	return err
}

func (s *KeyValues) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	dir, err := boxapi.PartitionDir(in)
	if err != nil {
		return nil, err
	}
	argv := []string{boxapi.RecordsVerbList, dir}
	if len(under) > 0 {
		prefix, err := boxapi.PathOf(in.Key(under...))
		if err != nil {
			return nil, err
		}
		argv = append(argv, strings.TrimPrefix(prefix, dir+"/"))
	}
	rendered, err := s.run(ctx, in.Tier, errUnbootstrappedList, nil, argv...)
	if errors.Is(err, errUnbootstrappedList) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found []records.Entry
	for line := range strings.Lines(rendered) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var listed boxapi.ListedEntry
		if err := json.Unmarshal([]byte(line), &listed); err != nil {
			return nil, unreadable(boxapi.RecordsVerbList, line)
		}
		key, err := boxapi.KeyOf(in, listed.Path)
		if err != nil {
			return nil, err
		}
		entry, err := entryOf(key, listed.Revision, listed.Value)
		if err != nil {
			return nil, err
		}
		found = append(found, entry)
	}
	return found, nil
}

var errUnbootstrappedList = refusal.Refuse(refusal.CodeNotReady, "this host keeps no records")

func unreadable(verb, said string) error {
	return refusal.Refuse(refusal.CodeDenied,
		"the box answered a records %s with %q", verb, strings.TrimSpace(said))
}

const (
	revisionWidth = 32
	revisionHex   = "0123456789abcdef"
)

func entryOf(key records.Key, revision records.Revision, value json.RawMessage) (records.Entry, error) {
	if !json.Valid(value) {
		return records.Entry{}, refusal.Refuse(refusal.CodeDenied, "%s is not an entry ocel wrote", key)
	}
	parsed, err := parseRevision(string(revision))
	if err != nil {
		return records.Entry{}, err
	}
	return records.Entry{Key: key, Value: value, Revision: parsed}, nil
}

func parseRevision(rendered string) (records.Revision, error) {
	revision := strings.TrimSpace(rendered)
	if len(revision) != revisionWidth || strings.Trim(revision, revisionHex) != "" {
		return "", refusal.Refuse(refusal.CodeDenied,
			"the box answered %q, not a revision", rendered)
	}
	return records.Revision(revision), nil
}

var _ records.Store = (*KeyValues)(nil)
