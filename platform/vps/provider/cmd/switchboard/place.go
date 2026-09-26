package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	stagedPattern = ".ocel.*.tmp"
	placedMode    = 0o644
)

func placeable(path string) error {
	dir := os.Getenv(switchboard.PlaceEnv)
	if dir == "" {
		return fmt.Errorf("no directory is mounted to place files in: %s is unset", switchboard.PlaceEnv)
	}
	name := filepath.Base(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) != filepath.Clean(dir) || strings.HasPrefix(name, ".") {
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

func staged(path string, in io.Reader) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, stagedPattern)
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = io.Copy(file, in)
	err = errors.Join(err, file.Chmod(placedMode), file.Sync(), file.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return synced(dir)
}

func synced(dir string) error {
	held, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(held.Sync(), held.Close())
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

func regularSum(path string) (string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	held, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !held.Mode().IsRegular() {
		return "", nil
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
