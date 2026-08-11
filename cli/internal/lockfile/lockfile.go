package lockfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const dirName = "dev-locks"

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

func Remove(root string) error {
	path, err := Path(root)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove lockfile: %w", err)
	}
	return nil
}

func lockDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}

	dir := filepath.Join(base, "ocel", dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create lockfile directory: %w", err)
	}
	return dir, nil
}

func key(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve project root %q: %w", root, err)
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:]), nil
}
