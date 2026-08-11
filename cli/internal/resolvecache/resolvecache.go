package resolvecache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Entry struct {
	DefsHash  string            `json:"defsHash"`
	Account   string            `json:"account"`
	ExpiresAt time.Time         `json:"expiresAt"`
	Env       map[string]string `json:"env"`
}

type Def struct {
	Name string
	Type string
}

type Cache struct {
	dir string
}

func Open() (*Cache, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config directory: %w", err)
	}
	return OpenAt(filepath.Join(base, "ocel", "resolve-cache"))
}

func OpenAt(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create resolve cache directory: %w", err)
	}
	return &Cache{dir: dir}, nil
}

func (c *Cache) Load(projectID string) (entry Entry, ok bool) {
	data, err := os.ReadFile(c.path(projectID))
	if err != nil {
		return Entry{}, false
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		return Entry{}, false
	}
	return entry, true
}

func (c *Cache) Save(projectID string, entry Entry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode resolve cache entry: %w", err)
	}
	if err := os.WriteFile(c.path(projectID), data, 0o600); err != nil {
		return fmt.Errorf("write resolve cache entry: %w", err)
	}
	return nil
}

func (c *Cache) path(projectID string) string {
	return filepath.Join(c.dir, sanitize(projectID)+".json")
}

func sanitize(projectID string) string {
	return strings.NewReplacer("/", "_", `\`, "_").Replace(projectID)
}

func HashDefs(defs []Def) string {
	sorted := make([]Def, len(defs))
	copy(sorted, defs)
	slices.SortFunc(sorted, func(a, b Def) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Type, b.Type)
	})
	return hash(sorted)
}

func Fingerprint(baseURL, token string) string {
	return hash([2]string{baseURL, token})
}

func hash(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
