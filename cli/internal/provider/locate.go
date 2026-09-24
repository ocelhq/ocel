package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/ocelhq/ocel/cli/internal/lockfile"
	"github.com/ocelhq/ocel/cli/internal/providers"
	"github.com/ocelhq/ocel/cli/internal/version"
)

type pinning int

const (
	pinToLock pinning = iota
	pinInMemory
)

func locateProvider(ctx context.Context, projectDir, name string, pinning pinning) (string, error) {
	store, err := providers.New(version.Version)
	if err != nil {
		return "", err
	}
	return locate(ctx, store, providers.KindProvider, projectDir, name, store.Platform, pinning)
}

func Connector(ctx context.Context, projectDir, name string, platform providers.Platform) ([]byte, error) {
	store, err := providers.New(version.Version)
	if err != nil {
		return nil, err
	}
	path, err := locate(ctx, store, providers.KindConnector, projectDir, name, platform, pinToLock)
	if err != nil {
		return nil, err
	}
	read, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the %s connector built for %s: %w", name, platform.Dir(), err)
	}
	return read, nil
}

func locate(ctx context.Context, store *providers.Store, kind providers.Kind, projectDir, name string, platform providers.Platform, pinning pinning) (string, error) {
	if !store.Fetches() {
		return store.Binary(ctx, kind, name, platform, "")
	}

	lock, err := pins(ctx, store, projectDir, pinning)
	if err != nil {
		return "", err
	}

	digest, pinned := lock.Digest(kind, name, platform.Dir())
	if !pinned {
		return "", fmt.Errorf("%s pins no %s %s %s for %s — release %s ships no such archive", lockfile.Name, name, kind, store.Version, platform.Dir(), store.Version)
	}
	return store.Binary(ctx, kind, name, platform, digest)
}

func Pin(ctx context.Context, projectDir string) error {
	store, err := providers.New(version.Version)
	if err != nil {
		return err
	}
	_, err = pin(ctx, store, projectDir)
	return err
}

func pins(ctx context.Context, store *providers.Store, projectDir string, pinning pinning) (lockfile.Lock, error) {
	lock, held, err := lockfile.Read(projectDir)
	if err != nil {
		return lockfile.Lock{}, err
	}
	if !held {
		if pinning == pinInMemory {
			return lockFromRelease(ctx, store)
		}
		return pin(ctx, store, projectDir)
	}
	if lock.CLI != store.Version {
		return lockfile.Lock{}, fmt.Errorf("%s pins the providers of ocel %s and this is ocel %s — run `ocel lock` to pin the providers this version runs, and commit the change", lockfile.Name, lock.CLI, store.Version)
	}
	return lock, nil
}

func pin(ctx context.Context, store *providers.Store, projectDir string) (lockfile.Lock, error) {
	lock, err := lockFromRelease(ctx, store)
	if err != nil {
		return lockfile.Lock{}, err
	}
	if err := lockfile.Write(projectDir, lock); err != nil {
		return lockfile.Lock{}, err
	}
	return lock, nil
}

func lockFromRelease(ctx context.Context, store *providers.Store) (lockfile.Lock, error) {
	sums, err := store.Checksums(ctx)
	if err != nil {
		return lockfile.Lock{}, fmt.Errorf("read the checksums of release %s: %w", store.Version, err)
	}
	return lockfile.FromChecksums(store.Version, sums), nil
}
