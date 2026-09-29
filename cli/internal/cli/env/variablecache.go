package env

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/encoding/protojson"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type variableCache struct {
	dir string
}

type variableCacheEntry struct {
	Fingerprint string            `json:"fingerprint"`
	Definitions []json.RawMessage `json:"definitions"`
	Groups      []json.RawMessage `json:"groups,omitempty"`
}

func openVariableCache() (*variableCache, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config directory: %w", err)
	}
	return openVariableCacheAt(filepath.Join(base, "ocel", "variable-cache"))
}

func openVariableCacheAt(dir string) (*variableCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create variable cache directory: %w", err)
	}
	return &variableCache{dir: dir}, nil
}

func (c *variableCache) LoadContaining(projectDir, fingerprint, key string) (definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition, ok bool) {
	data, err := os.ReadFile(c.path(projectDir))
	if err != nil {
		return nil, nil, false
	}
	var e variableCacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, nil, false
	}
	if e.Fingerprint != stampVariableCacheEntry(fingerprint, e) {
		return nil, nil, false
	}
	for _, raw := range e.Definitions {
		definition := &resourcesv1.VariableDefinition{}
		if err := protojson.Unmarshal(raw, definition); err != nil {
			return nil, nil, false
		}
		definitions = append(definitions, definition)
	}
	for _, raw := range e.Groups {
		group := &resourcesv1.GroupDefinition{}
		if err := protojson.Unmarshal(raw, group); err != nil {
			return nil, nil, false
		}
		groups = append(groups, group)
	}
	for _, definition := range definitions {
		if definition.GetKey() == key {
			return definitions, groups, true
		}
	}
	return nil, nil, false
}

func (c *variableCache) Save(projectDir, fingerprint string, definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition) error {
	var e variableCacheEntry
	for _, definition := range definitions {
		raw, err := protojson.Marshal(definition)
		if err != nil {
			return fmt.Errorf("encode variable cache entry: %w", err)
		}
		e.Definitions = append(e.Definitions, raw)
	}
	for _, group := range groups {
		raw, err := protojson.Marshal(group)
		if err != nil {
			return fmt.Errorf("encode variable cache entry: %w", err)
		}
		e.Groups = append(e.Groups, raw)
	}
	e.Fingerprint = stampVariableCacheEntry(fingerprint, e)
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode variable cache entry: %w", err)
	}
	if err := os.WriteFile(c.path(projectDir), data, 0o600); err != nil {
		return fmt.Errorf("write variable cache entry: %w", err)
	}
	return nil
}

func stampVariableCacheEntry(fingerprint string, e variableCacheEntry) string {
	e.Fingerprint = fingerprint
	data, err := json.Marshal(e)
	if err != nil {
		return ""
	}
	return hashString(string(data))
}

func (c *variableCache) path(projectDir string) string {
	return filepath.Join(c.dir, hashString(projectDir)+".json")
}

func hashBundledEntry(entryPath string) (string, error) {
	f, err := os.Open(entryPath)
	if err != nil {
		return "", fmt.Errorf("read the bundled discovery entrypoint: %w", err)
	}
	defer f.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", fmt.Errorf("read the bundled discovery entrypoint: %w", err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
