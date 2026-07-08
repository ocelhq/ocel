// Package resolvecache persists the CLI's most recent resolve response per
// project, so `ocel dev` can skip calling the resolve API when the declared
// resource definitions and signed-in account haven't changed since the last
// successful resolve. See internal/provision.CachedResolve, which is the
// only caller.
package resolvecache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is the cached outcome of a resolve call for one project.
type Entry struct {
	// DefsHash is HashDefs of the resource definitions that produced Env.
	DefsHash string `json:"defsHash"`
	// Account fingerprints the server + credentials the resolve was made
	// with (see Fingerprint), so switching accounts invalidates the cache
	// even when DefsHash still matches.
	Account string `json:"account"`
	// ExpiresAt is the server-provided expiry carried over from the resolve
	// response; once passed, the entry is no longer usable even if DefsHash
	// and Account still match.
	ExpiresAt time.Time `json:"expiresAt"`
	// Env is the flat OCEL_RESOURCE_<TYPE>_<name> -> JSON env map from the
	// cached resolve response.
	Env map[string]string `json:"env"`
}

// Def is one resource definition (name + canonical type name, see
// provision.ResourceTypeName) as hashed by HashDefs.
type Def struct {
	Name string
	Type string
}

// Cache reads and writes resolve cache entries under a directory, one 0600
// file per project.
type Cache struct {
	dir string
}

// Open returns a Cache rooted at the "resolve-cache" directory under the
// user's config dir, creating it (0700) if necessary.
func Open() (*Cache, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config directory: %w", err)
	}
	return OpenAt(filepath.Join(base, "ocel", "resolve-cache"))
}

// OpenAt returns a Cache rooted at dir, creating it (0700) if necessary.
// Exported so tests can point it at a temp directory instead of Open's real
// user config dir.
func OpenAt(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create resolve cache directory: %w", err)
	}
	return &Cache{dir: dir}, nil
}

// Load returns the cached entry for projectID. ok is false if no entry is
// stored, or if the stored entry can't be read or parsed - a corrupt or
// unreadable cache file is treated as a miss rather than an error, since
// resolving fresh is always a safe fallback.
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

// Save persists entry as the cached resolve response for projectID, mode
// 0600 since Env carries live connection strings.
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

// sanitize makes projectID safe to use as a filename.
func sanitize(projectID string) string {
	return strings.NewReplacer("/", "_", `\`, "_").Replace(projectID)
}

// HashDefs returns a stable hash of defs, order-independent, so declaring
// resources in a different order doesn't cause a spurious cache miss.
func HashDefs(defs []Def) string {
	sorted := make([]Def, len(defs))
	copy(sorted, defs)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Type < sorted[j].Type
	})
	return hash(sorted)
}

// Fingerprint returns a stable hash identifying the account a resolve was
// made against (the API/harness base URL plus the bearer token), so a
// re-login or account switch invalidates any cache entry from before it.
func Fingerprint(baseURL, token string) string {
	return hash([2]string{baseURL, token})
}

func hash(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
