package declcache

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

type Cache struct {
	dir string
}

type entry struct {
	Fingerprint string            `json:"fingerprint"`
	Definitions []json.RawMessage `json:"definitions"`
}

func Open() (*Cache, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config directory: %w", err)
	}
	return OpenAt(filepath.Join(base, "ocel", "declaration-cache"))
}

func OpenAt(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create declaration cache directory: %w", err)
	}
	return &Cache{dir: dir}, nil
}

func (c *Cache) Load(projectDir, fingerprint, key string) (definitions []*resourcesv1.VariableDefinition, ok bool) {
	data, err := os.ReadFile(c.path(projectDir))
	if err != nil {
		return nil, false
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, false
	}
	if e.Fingerprint != fingerprint {
		return nil, false
	}
	for _, raw := range e.Definitions {
		definition := &resourcesv1.VariableDefinition{}
		if err := protojson.Unmarshal(raw, definition); err != nil {
			return nil, false
		}
		definitions = append(definitions, definition)
	}
	for _, definition := range definitions {
		if definition.GetKey() == key {
			return definitions, true
		}
	}
	return nil, false
}

func (c *Cache) Save(projectDir, fingerprint string, definitions []*resourcesv1.VariableDefinition) error {
	e := entry{Fingerprint: fingerprint}
	for _, definition := range definitions {
		raw, err := protojson.Marshal(definition)
		if err != nil {
			return fmt.Errorf("encode declaration cache entry: %w", err)
		}
		e.Definitions = append(e.Definitions, raw)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode declaration cache entry: %w", err)
	}
	if err := os.WriteFile(c.path(projectDir), data, 0o600); err != nil {
		return fmt.Errorf("write declaration cache entry: %w", err)
	}
	return nil
}

func (c *Cache) path(projectDir string) string {
	return filepath.Join(c.dir, hashString(projectDir)+".json")
}

func Fingerprint(entryPath string) (string, error) {
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
