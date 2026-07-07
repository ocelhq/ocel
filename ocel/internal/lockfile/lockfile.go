// Package lockfile records, per projectID, the TCP address of the `ocel dev`
// leader process for that project, in a per-user temp directory. It holds no
// opinion on liveness — callers decide whether a recorded address is still
// reachable (see internal/election).
package lockfile

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// dirName is the per-user directory (under os.TempDir) holding one lockfile
// per project.
const dirName = "ocel-dev-locks"

// Path returns the lockfile path for projectID, creating its parent
// directory (0700) if necessary.
func Path(projectID string) (string, error) {
	dir, err := lockDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sanitize(projectID)+".lock"), nil
}

// Read returns the leader address recorded for projectID. It returns
// os.ErrNotExist (check with os.IsNotExist) if no lockfile exists.
func Read(projectID string) (string, error) {
	path, err := Path(projectID)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Create atomically records addr as the leader address for projectID. If a
// lockfile already exists — another process won the election concurrently —
// it fails with an error satisfying errors.Is(err, os.ErrExist) and leaves
// the existing file untouched.
func Create(projectID, addr string) error {
	path, err := Path(projectID)
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

// Remove deletes projectID's lockfile. It does not error if none exists.
func Remove(projectID string) error {
	path, err := Path(projectID)
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

// sanitize makes projectID safe to use as a filename.
func sanitize(projectID string) string {
	return strings.NewReplacer("/", "_", `\`, "_").Replace(projectID)
}
