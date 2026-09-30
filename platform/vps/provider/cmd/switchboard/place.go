package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	stagedPattern = ".ocel.*.tmp"
	placedMode    = 0o644
	secretMode    = 0o400
	secretDirMode = 0o700
)

func placeable(path string) error {
	dir, err := placeDir()
	if err != nil {
		return err
	}
	name := filepath.Base(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) != dir || strings.HasPrefix(name, ".") {
		return fmt.Errorf("%s is not a file directly in %s, the one directory mounted to place files in", path, dir)
	}
	return nil
}

func confined(argv []string, errs io.Writer, act func(path string) error) int {
	if len(argv) != 1 {
		return usage(errs)
	}
	path := argv[0]
	if err := placeable(path); err != nil {
		return refuse(errs, err)
	}
	if err := act(path); err != nil {
		return refuse(errs, err)
	}
	return 0
}

func place(argv []string, in io.Reader, errs io.Writer) int {
	return confined(argv, errs, func(path string) error {
		if err := staged(path, in); err != nil {
			return fmt.Errorf("place %s: %w", path, err)
		}
		return nil
	})
}

func placeSecret(argv []string, in io.Reader, errs io.Writer) int {
	return confined(argv, errs, func(path string) error {
		if err := os.Chmod(filepath.Dir(path), secretDirMode); err != nil {
			return fmt.Errorf("place %s: %w", path, err)
		}
		if err := stagedAt(path, in, secretMode); err != nil {
			return fmt.Errorf("place %s: %w", path, err)
		}
		return nil
	})
}

func staged(path string, in io.Reader) error { return stagedAt(path, in, placedMode) }

func stagedAt(path string, in io.Reader, mode os.FileMode) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, stagedPattern)
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = file.Chmod(mode)
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

func synced(dir string) error {
	opened, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(opened.Sync(), opened.Close())
}

func unplace(argv []string, errs io.Writer) int {
	return confined(argv, errs, func(path string) error {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("unplace %s: %w", path, err)
		}
		if err := synced(filepath.Dir(path)); err != nil {
			return fmt.Errorf("unplace %s: %w", path, err)
		}
		return nil
	})
}

func digest(argv []string, out, errs io.Writer) int {
	return confined(argv, errs, func(path string) error {
		sum, err := regularSum(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if sum != "" {
			fmt.Fprintln(out, sum)
		}
		return nil
	})
}

var readByTraefik = []string{".yml", ".yaml", ".toml"}

func beside(argv []string, out, errs io.Writer) int {
	return confined(argv, errs, func(path string) error {
		dir := filepath.Dir(path)
		found := []switchboard.SiblingFile{}
		err := filepath.WalkDir(dir, func(at string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.Type().IsRegular() || at == path || !slices.Contains(readByTraefik, strings.ToLower(filepath.Ext(at))) {
				return nil
			}
			content, err := regularContent(at)
			if err != nil {
				return err
			}
			name, err := filepath.Rel(dir, at)
			if err != nil {
				return err
			}
			found = append(found, switchboard.SiblingFile{Name: name, Content: content})
			return nil
		})
		if err != nil {
			return fmt.Errorf("read what is beside %s: %w", path, err)
		}
		return json.NewEncoder(out).Encode(found)
	})
}

func openUnfollowed(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

func regularContent(path string) ([]byte, error) {
	file, err := openUnfollowed(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func regularSum(path string) (string, error) {
	file, err := openUnfollowed(path)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", nil
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
