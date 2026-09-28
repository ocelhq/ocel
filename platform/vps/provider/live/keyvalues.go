package live

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	TierRoot  = "/etc/ocel"
	StateRoot = "/var/lib/ocel"
)

const (
	EntrySuffix      = ".json"
	segmentSeparator = "+"
)

func KeyValuesDir(root string, tier environment.Tier) string {
	return filepath.Join(root, string(tier), "keyvalues")
}

var errReadOnly = errors.New("the box's entries are read-only here")

type KeyValues struct{ Root string }

func (s KeyValues) Read(_ context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	path, err := PathOf(key)
	if err != nil {
		return keyvalue.Entry{}, err
	}
	raw, err := os.ReadFile(filepath.Join(KeyValuesDir(s.Root, key.Partition.Tier), path+EntrySuffix))
	if errors.Is(err, fs.ErrNotExist) {
		return keyvalue.Entry{}, keyvalue.ErrNotFound
	}
	if err != nil {
		return keyvalue.Entry{}, err
	}
	return entryOf(key, raw)
}

func (s KeyValues) List(_ context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	dir, err := PartitionDir(in)
	if err != nil {
		return nil, err
	}
	partition := filepath.Join(KeyValuesDir(s.Root, in.Tier), dir)
	beneath := partition
	var found []keyvalue.Entry
	if len(under) > 0 {
		prefix, err := encodePath(under)
		if err != nil {
			return nil, err
		}
		beneath = filepath.Join(partition, prefix)
		entry, err := s.entryAt(in, partition, beneath+EntrySuffix)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if err == nil {
			found = append(found, entry)
		}
	}
	err = filepath.WalkDir(beneath, func(path string, visited fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if visited.IsDir() || !strings.HasSuffix(visited.Name(), EntrySuffix) {
			return nil
		}
		entry, err := s.entryAt(in, partition, path)
		if err != nil {
			return err
		}
		found = append(found, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func (s KeyValues) entryAt(in keyvalue.Partition, partition, path string) (keyvalue.Entry, error) {
	relative, err := filepath.Rel(partition, strings.TrimSuffix(path, EntrySuffix))
	if err != nil {
		return keyvalue.Entry{}, err
	}
	key, err := KeyOf(in, filepath.ToSlash(relative))
	if err != nil {
		return keyvalue.Entry{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return keyvalue.Entry{}, err
	}
	return entryOf(key, raw)
}

func (KeyValues) Write(context.Context, keyvalue.Entry) (keyvalue.Revision, error) {
	return "", errReadOnly
}

func (KeyValues) WritePair(context.Context, keyvalue.Entry, keyvalue.Entry) error {
	return errReadOnly
}

func (KeyValues) Remove(context.Context, keyvalue.Key, keyvalue.Revision) error {
	return errReadOnly
}

type entryFile struct {
	Revision keyvalue.Revision `json:"revision"`
	Value    json.RawMessage   `json:"value"`
}

func entryOf(key keyvalue.Key, raw []byte) (keyvalue.Entry, error) {
	var file entryFile
	if err := json.Unmarshal(raw, &file); err != nil || file.Revision == "" || len(file.Value) == 0 {
		return keyvalue.Entry{}, refusal.Refuse(refusal.CodeDenied, "%s is not an entry ocel wrote", key)
	}
	return keyvalue.Entry{Key: key, Value: file.Value, Revision: file.Revision}, nil
}

func PartitionDir(in keyvalue.Partition) (string, error) {
	if err := keyvalue.RefuseMalformedPartition(in); err != nil {
		return "", err
	}
	segments := in.Segments()
	for i, segment := range segments {
		segments[i] = encodeSegment(segment)
	}
	return strings.Join(segments, segmentSeparator), nil
}

func PathOf(key keyvalue.Key) (string, error) {
	if err := keyvalue.RefuseMalformedKey(key); err != nil {
		return "", err
	}
	dir, err := PartitionDir(key.Partition)
	if err != nil {
		return "", err
	}
	path, err := encodePath(key.Path)
	if err != nil {
		return "", err
	}
	return dir + "/" + path, nil
}

func encodePath(path []string) (string, error) {
	segments := make([]string, 0, len(path))
	for _, segment := range path {
		if segment == "" {
			return "", refusal.Refuse(refusal.CodeInvalid, "%q has an empty segment", path)
		}
		segments = append(segments, encodeSegment(segment))
	}
	return strings.Join(segments, "/"), nil
}

func encodeSegment(segment string) string {
	escaped := keyvalue.EscapeSegment(segment, isPlain)
	if strings.HasPrefix(escaped, ".") {
		return "%2E" + escaped[1:]
	}
	return escaped
}

func isPlain(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	default:
		return c == '-' || c == '_' || c == '.'
	}
}

func KeyOf(in keyvalue.Partition, encoded string) (keyvalue.Key, error) {
	var path []string
	for _, segment := range strings.Split(encoded, "/") {
		decoded, err := keyvalue.UnescapeSegment(segment)
		if err != nil {
			return keyvalue.Key{}, err
		}
		path = append(path, decoded)
	}
	return in.Key(path...), nil
}

var _ keyvalue.Store = KeyValues{}
