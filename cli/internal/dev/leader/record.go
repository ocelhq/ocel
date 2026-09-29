package leader

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

const recordDirName = "dev-leaders"

var ErrMalformed = errors.New("the dev leader record has no address and token pair")

func Read(root string) (Leader, error) {
	path, err := recordPath(root)
	if err != nil {
		return Leader{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Leader{}, err
	}
	address, token, found := strings.Cut(strings.TrimSpace(string(data)), "\n")
	if !found || address == "" || token == "" {
		return Leader{}, ErrMalformed
	}
	return Leader{Address: address, Token: token}, nil
}

func writeRecord(root string, leader Leader) error {
	path, err := recordPath(root)
	if err != nil {
		return err
	}
	writing, err := os.CreateTemp(filepath.Dir(path), ".writing-*")
	if err != nil {
		return fmt.Errorf("create the dev leader record: %w", err)
	}
	defer func() { _ = os.Remove(writing.Name()) }()
	if _, err := writing.WriteString(leader.Address + "\n" + leader.Token + "\n"); err != nil {
		_ = writing.Close()
		return fmt.Errorf("write the dev leader record: %w", err)
	}
	if err := writing.Close(); err != nil {
		return fmt.Errorf("write the dev leader record: %w", err)
	}
	if err := os.Link(writing.Name(), path); err != nil {
		return fmt.Errorf("create the dev leader record: %w", err)
	}
	return nil
}

func lockRecord(root string) (*flock.Flock, error) {
	path, err := recordPath(root)
	if err != nil {
		return nil, err
	}
	lock := flock.New(path + ".flock")
	if err := lock.Lock(); err != nil {
		return nil, fmt.Errorf("wait for another ocel dev to finish claiming this project: %w", err)
	}
	return lock, nil
}

func removeRecord(root string) error {
	path, err := recordPath(root)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove the dev leader record: %w", err)
	}
	return nil
}

func recordPath(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve project root %q: %w", root, err)
	}
	dir, err := recordDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(dir, hex.EncodeToString(sum[:])), nil
}

func recordDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	dir := filepath.Join(base, "ocel", recordDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the dev leader directory: %w", err)
	}
	return dir, nil
}
