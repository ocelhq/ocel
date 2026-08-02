// Package declcache persists the variable declarations a project's discovery
// run produced, so a command that only needs to know a key's folder scope can
// skip re-running the pass when nothing it reads has changed. It mirrors
// internal/resolvecache: one 0600 file per project under the user's config
// dir, an entry usable only while its fingerprint still matches and its expiry
// has not passed.
//
// The fingerprint is over the bundled discovery program, which is every source
// file and dependency a declaration can come from, inlined by esbuild. What it
// cannot see is the ambient state that program reads while it runs — a
// declaration made conditional on process.env has no fingerprint here — so the
// expiry is what bounds that, rather than a freshness guarantee this side can
// make.
package declcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// TTL bounds how long a declaration set is reused. It is short because the
// inputs the fingerprint cannot cover are the ones a stale answer would be
// wrong about, and long enough that a scripted run of writes pays for
// discovery once.
const TTL = 5 * time.Minute

// Cache reads and writes declaration entries under a directory.
type Cache struct {
	dir string
}

type entry struct {
	Fingerprint string            `json:"fingerprint"`
	ExpiresAt   time.Time         `json:"expiresAt"`
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

// Load returns the declarations cached for projectDir under fingerprint. ok is
// false for a miss, an entry from a different fingerprint, an expired entry, or
// one that cannot be read or parsed — running discovery again is always the
// safe answer.
func (c *Cache) Load(projectDir, fingerprint string) (definitions []*resourcesv1.VariableDefinition, ok bool) {
	data, err := os.ReadFile(c.path(projectDir))
	if err != nil {
		return nil, false
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, false
	}
	if e.Fingerprint != fingerprint || !time.Now().Before(e.ExpiresAt) {
		return nil, false
	}
	for _, raw := range e.Definitions {
		definition := &resourcesv1.VariableDefinition{}
		if err := protojson.Unmarshal(raw, definition); err != nil {
			return nil, false
		}
		definitions = append(definitions, definition)
	}
	return definitions, true
}

// Save persists definitions as what projectDir's code declares under
// fingerprint, expiring TTL from now.
func (c *Cache) Save(projectDir, fingerprint string, definitions []*resourcesv1.VariableDefinition) error {
	e := entry{Fingerprint: fingerprint, ExpiresAt: time.Now().Add(TTL)}
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
// the bundled program at entryPath, plus the env class the run declares
// against, since a declaring process is answered from that class's store.
func Fingerprint(entryPath string, class string) (string, error) {
	f, err := os.Open(entryPath)
	if err != nil {
		return "", fmt.Errorf("read the bundled discovery entrypoint: %w", err)
	}
	defer f.Close()

	sum := sha256.New()
	fmt.Fprintf(sum, "%s\n", class)
	if _, err := io.Copy(sum, f); err != nil {
		return "", fmt.Errorf("read the bundled discovery entrypoint: %w", err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
