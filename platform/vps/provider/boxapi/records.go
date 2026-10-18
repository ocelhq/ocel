package boxapi

import (
	"encoding/json"
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

type EntryFile struct {
	Revision records.Revision `json:"revision"`
	Value    json.RawMessage  `json:"value"`
}

type ListedEntry struct {
	Path     string           `json:"path"`
	Revision records.Revision `json:"revision"`
	Value    json.RawMessage  `json:"value"`
}

type Change struct {
	Path     string           `json:"path"`
	Revision records.Revision `json:"revision"`
	Value    json.RawMessage  `json:"value,omitempty"`
	Remove   bool             `json:"remove,omitempty"`
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
