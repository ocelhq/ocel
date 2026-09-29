package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	originMode   = 0o640
	originSuffix = ".pem"
)

func placeDir() (string, error) {
	dir := os.Getenv(switchboard.PlaceEnv)
	if dir == "" {
		return "", fmt.Errorf("no directory is mounted to place files in: %s is unset", switchboard.PlaceEnv)
	}
	return filepath.Clean(dir), nil
}

func originPlaceable(path string) error {
	if err := placeable(path); err != nil {
		return err
	}
	name := filepath.Base(path)
	if !strings.HasPrefix(name, switchboard.OriginPrefix) || !strings.HasSuffix(name, originSuffix) {
		return fmt.Errorf("%s is not named %s*%s, the only names an origin certificate is placed under", path, switchboard.OriginPrefix, originSuffix)
	}
	return nil
}

func placeOrigin(argv []string, in io.Reader, errs io.Writer) int {
	if len(argv) != 1 {
		return usage(errs)
	}
	path := argv[0]
	if err := originPlaceable(path); err != nil {
		return refuse(errs, err)
	}
	if err := stagedPrivate(path, in); err != nil {
		return refuse(errs, fmt.Errorf("place %s: %w", path, err))
	}
	return 0
}

func stagedPrivate(path string, in io.Reader) error {
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	owned, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("the group of %s cannot be read", dir)
	}
	file, err := os.CreateTemp(dir, stagedPattern)
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = errors.Join(file.Chmod(originMode), file.Chown(-1, int(owned.Gid)))
	if err == nil {
		_, err = io.Copy(file, in)
	}
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return synced(dir)
}

func unplaceOrigins(kept []string, errs io.Writer) int {
	dir, err := placeDir()
	if err != nil {
		return refuse(errs, err)
	}
	for _, path := range kept {
		if err := originPlaceable(path); err != nil {
			return refuse(errs, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return refuse(errs, fmt.Errorf("read %s: %w", dir, err))
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if !strings.HasPrefix(entry.Name(), switchboard.OriginPrefix) || slices.Contains(kept, path) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return refuse(errs, fmt.Errorf("unplace %s: %w", path, err))
		}
	}
	if err := synced(dir); err != nil {
		return refuse(errs, fmt.Errorf("unplace in %s: %w", dir, err))
	}
	return 0
}
