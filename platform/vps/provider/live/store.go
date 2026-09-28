package live

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
)

const (
	TierRoot  = "/etc/ocel"
	StateRoot = "/var/lib/ocel"
)

const (
	EntrySuffix  = ".json"
	sealKeyFile  = "seal.key"
	sealKeyBytes = 32
	sealNonce    = 12
	sealTag      = 16

	partitionJoiner = "+"
)

func RecordsDir(root string, tier environment.Tier) string {
	return filepath.Join(root, string(tier), "records")
}

func KeyPath(root string, tier environment.Tier) string {
	return filepath.Join(root, string(tier), sealKeyFile)
}

var errReadOnly = errors.New("the box's entries are read-only here")

type KeyValues struct{ Root string }

func (s KeyValues) Read(_ context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	path, err := PathOf(key)
	if err != nil {
		return keyvalue.Entry{}, err
	}
	raw, err := os.ReadFile(filepath.Join(RecordsDir(s.Root, key.Partition.Tier), path+EntrySuffix))
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
	partition := filepath.Join(RecordsDir(s.Root, in.Tier), dir)
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

type Cipher struct{ Root string }

func (Cipher) Seal(context.Context, environment.Tier, seal.AssociatedData, []byte) ([]byte, error) {
	return nil, errors.New("the box seals values only through its helper")
}

func (s Cipher) Open(_ context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	if tier == "" {
		return nil, fmt.Errorf("a value bound to %s names no tier", bound.Bytes())
	}
	key, err := os.ReadFile(KeyPath(s.Root, tier))
	if err != nil {
		return nil, fmt.Errorf("read the %s seal key: %w", tier, err)
	}
	return Open(key, bound, sealed)
}

func Open(key []byte, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	if len(key) != sealKeyBytes {
		return nil, fmt.Errorf("the seal key is %d bytes, want %d", len(key), sealKeyBytes)
	}
	if len(sealed) < sealNonce+sealTag {
		return nil, errors.New("the sealed value is too short")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, sealed[:sealNonce], sealed[sealNonce:], bound.Bytes())
	if err != nil {
		return nil, fmt.Errorf("the value was not sealed under this key bound to %s", bound.Bytes())
	}
	return plaintext, nil
}

func PartitionDir(in keyvalue.Partition) (string, error) {
	if err := keyvalue.RefuseMalformedPartition(in); err != nil {
		return "", err
	}
	segments := make([]string, 0, 1+len(in.Path))
	for _, segment := range append([]string{in.Root}, in.Path...) {
		segments = append(segments, encodeSegment(segment))
	}
	return strings.Join(segments, partitionJoiner), nil
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
	var written strings.Builder
	for i := 0; i < len(segment); i++ {
		if plain(segment[i]) && (i != 0 || segment[i] != '.') {
			written.WriteByte(segment[i])
			continue
		}
		fmt.Fprintf(&written, "%%%02X", segment[i])
	}
	return written.String()
}

func plain(c byte) bool {
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
		decoded, err := decodeSegment(segment)
		if err != nil {
			return keyvalue.Key{}, err
		}
		path = append(path, decoded)
	}
	return in.Key(path...), nil
}

func decodeSegment(segment string) (string, error) {
	var written strings.Builder
	for i := 0; i < len(segment); i++ {
		if segment[i] != '%' {
			written.WriteByte(segment[i])
			continue
		}
		if i+2 >= len(segment) {
			return "", refusal.Refuse(refusal.CodeDenied,
				"the records helper returned %q, which ocel did not write", segment)
		}
		value, err := strconv.ParseUint(segment[i+1:i+3], 16, 8)
		if err != nil {
			return "", refusal.Refuse(refusal.CodeDenied,
				"the records helper returned %q, which ocel did not write", segment)
		}
		written.WriteByte(byte(value))
		i += 2
	}
	return written.String(), nil
}

var (
	_ keyvalue.Store = KeyValues{}
	_ seal.Cipher    = Cipher{}
)
