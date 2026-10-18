package boxapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
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

func (s KeyValues) Read(_ context.Context, key records.Key) (records.Entry, error) {
	path, err := PathOf(key)
	if err != nil {
		return records.Entry{}, err
	}
	raw, err := os.ReadFile(filepath.Join(KeyValuesDir(s.Root, key.Partition.Tier), path+EntrySuffix))
	if errors.Is(err, fs.ErrNotExist) {
		return records.Entry{}, records.ErrNotFound
	}
	if err != nil {
		return records.Entry{}, err
	}
	return entryOf(key, raw)
}

func (s KeyValues) List(_ context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	dir, err := PartitionDir(in)
	if err != nil {
		return nil, err
	}
	partition := filepath.Join(KeyValuesDir(s.Root, in.Tier), dir)
	beneath := partition
	var found []records.Entry
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

func (s KeyValues) entryAt(in records.Partition, partition, path string) (records.Entry, error) {
	relative, err := filepath.Rel(partition, strings.TrimSuffix(path, EntrySuffix))
	if err != nil {
		return records.Entry{}, err
	}
	key, err := KeyOf(in, filepath.ToSlash(relative))
	if err != nil {
		return records.Entry{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return records.Entry{}, err
	}
	return entryOf(key, raw)
}

func (KeyValues) Write(context.Context, records.Entry) (records.Revision, error) {
	return "", errReadOnly
}

func (KeyValues) WritePair(context.Context, records.Entry, records.Entry) error {
	return errReadOnly
}

func (KeyValues) Remove(context.Context, records.Key, records.Revision) error {
	return errReadOnly
}

type entryFile struct {
	Revision records.Revision `json:"revision"`
	Value    json.RawMessage  `json:"value"`
}

func entryOf(key records.Key, raw []byte) (records.Entry, error) {
	var file entryFile
	if err := json.Unmarshal(raw, &file); err != nil || file.Revision == "" || len(file.Value) == 0 {
		return records.Entry{}, refusal.Refuse(refusal.CodeDenied, "%s is not an entry ocel wrote", key)
	}
	return records.Entry{Key: key, Value: file.Value, Revision: file.Revision}, nil
}

func PartitionDir(in records.Partition) (string, error) {
	if err := records.RefuseMalformedPartition(in); err != nil {
		return "", err
	}
	segments := in.Segments()
	for i, segment := range segments {
		segments[i] = encodeSegment(segment)
	}
	return strings.Join(segments, segmentSeparator), nil
}

func PathOf(key records.Key) (string, error) {
	if err := records.RefuseMalformedKey(key); err != nil {
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
	escaped := records.EscapeSegment(segment, isPlain)
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

func KeyOf(in records.Partition, encoded string) (records.Key, error) {
	var path []string
	for _, segment := range strings.Split(encoded, "/") {
		decoded, err := records.UnescapeSegment(segment)
		if err != nil {
			return records.Key{}, err
		}
		path = append(path, decoded)
	}
	return in.Key(path...), nil
}

var _ records.Store = KeyValues{}
