// Package lockfile records, per project root, the TCP address of the `ocel
// dev` leader process for that working tree, in a per-user temp directory. It
// holds no opinion on liveness — callers decide whether a recorded address is
// still reachable (see internal/election).
//
// The key is the project root rather than any project identity: one dev server
// per working tree. Two clones of the same repo may sit at different commits
// with different resource declarations, so they must never share a leader.
package lockfile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// dirName is the per-user directory (under os.TempDir) holding one lockfile
// per project root.
const dirName = "ocel-dev-locks"

// Path returns the lockfile path for the project rooted at root, creating its
// parent directory (0700) if necessary.
func Path(root string) (string, error) {
	name, err := key(root)
	if err != nil {
		return "", err
	}
	dir, err := lockDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".lock"), nil
}

// Read returns the leader address recorded for root. It returns
// os.ErrNotExist (check with os.IsNotExist) if no lockfile exists.
func Read(root string) (string, error) {
	path, err := Path(root)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Create atomically records addr as the leader address for root. If a
// lockfile already exists — another process won the election concurrently —
// it fails with an error satisfying errors.Is(err, os.ErrExist) and leaves
// the existing file untouched.
func Create(root, addr string) error {
	path, err := Path(root)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create lockfile: %w", err)
	}
	if _, err := f.WriteString(addr); err != nil {
		f.Close()
		return fmt.Errorf("write lockfile: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write lockfile: %w", err)
	}
	return nil
}

// Remove deletes root's lockfile. It does not error if none exists.
func Remove(root string) error {
	path, err := Path(root)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove lockfile: %w", err)
	}
	return nil
}

// lockDir returns the per-user directory holding lockfiles, creating it
// (0700) if necessary. Scoping by user (rather than a single shared
// directory under the system-wide os.TempDir) avoids permission conflicts
// and cross-user collisions on multi-user machines where /tmp is shared.
func lockDir() (string, error) {
	uid := "shared"
	if u, err := user.Current(); err == nil && u.Uid != "" {
		uid = u.Uid
	}

	dir := filepath.Join(os.TempDir(), fmt.Sprintf("%s-%s", dirName, uid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create lockfile directory: %w", err)
	}
	return dir, nil
}

// key is root's filename for the lock directory: the hash of its absolute
// path. Absolute because two spellings of one root must agree; hashed because
// a path is neither a legal filename nor bounded in length.
func key(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve project root %q: %w", root, err)
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:]), nil
}
