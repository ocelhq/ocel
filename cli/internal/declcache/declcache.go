// Package declcache persists the variable declarations a project's discovery
// run produced, so a command that only needs to know a key's folder scope can
// skip re-running the pass when nothing it reads has changed. It mirrors
// internal/resolvecache: one 0600 file per project under the user's config
// dir, an entry usable only while its fingerprint still matches.
//
// The fingerprint is over the bundled discovery program, which is every source
// file and dependency a declaration can come from, inlined by esbuild, so any
// change to the declaring code moves it and no time bound is needed. What it
// cannot see is the ambient state that program reads while it runs — a
// declaration made conditional on process.env can be missing from a cached set
// with the code unchanged. So a cached set answers for the keys it holds and
// for no others, which is why Load takes the key it is being asked about and
// has no way to report an absence as an answer.
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

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// Cache reads and writes declaration entries under a directory.
type Cache struct {
	dir string
}

type entry struct {
	Fingerprint string            `json:"fingerprint"`
	Definitions []json.RawMessage `json:"definitions"`
}

// Open returns a Cache rooted at the "declaration-cache" directory under the
// user's config dir, creating it (0700) if necessary.
func Open() (*Cache, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config directory: %w", err)
	}
	return OpenAt(filepath.Join(base, "ocel", "declaration-cache"))
}

// OpenAt returns a Cache rooted at dir, creating it (0700) if necessary.
func OpenAt(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create declaration cache directory: %w", err)
	}
	return &Cache{dir: dir}, nil
}

// Load returns the declarations cached for projectDir under fingerprint, but
// only when they declare key. ok is false for a miss, an entry from a different
// fingerprint, one that cannot be read or parsed, or a set that does not
// mention key — an absence the cache cannot distinguish from a declaration the
// last run's ambient state suppressed. Running discovery again is always the
// safe answer.
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

// Save persists definitions as what projectDir's code declares under
// fingerprint.
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

// Fingerprint identifies the declaration set a discovery run would produce:
// the bundled program at entryPath. The env class is not part of it — a
// declaration is what the code states, and the store the run's values come
// from cannot change that — so alternating preview and production writes share
// one entry rather than evicting each other.
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
