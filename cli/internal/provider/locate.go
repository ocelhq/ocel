package provider

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/lockfile"
	"github.com/ocelhq/ocel/cli/internal/providers"
	"github.com/ocelhq/ocel/cli/internal/version"
)

func Locate(ctx context.Context, projectDir, name string) (string, error) {
	store, err := providers.New(version.Version)
	if err != nil {
		return "", err
	}
	return locate(ctx, store, projectDir, name)
}

func locate(ctx context.Context, store *providers.Store, projectDir, name string) (string, error) {
	if !store.Fetches() {
		return store.Binary(ctx, name, "")
	}

	lock, err := pins(ctx, store, projectDir)
	if err != nil {
		return "", err
	}

	platform := store.Platform.Dir()
	digest, pinned := lock.Digest(name, platform)
	if !pinned {
		return "", fmt.Errorf("%s pins no %s provider %s for %s — release %s ships no such archive", lockfile.Name, name, store.Version, platform, store.Version)
	}
	return store.Binary(ctx, name, digest)
}

func Pin(ctx context.Context, projectDir string) error {
	store, err := providers.New(version.Version)
	if err != nil {
		return err
	}
	return pin(ctx, store, projectDir)
}

func pins(ctx context.Context, store *providers.Store, projectDir string) (lockfile.Lock, error) {
	lock, held, err := lockfile.Read(projectDir)
	if err != nil {
		return lockfile.Lock{}, err
	}
	if !held {
		if err := pin(ctx, store, projectDir); err != nil {
			return lockfile.Lock{}, err
		}
		lock, _, err = lockfile.Read(projectDir)
		return lock, err
	}
	if lock.CLI != store.Version {
		return lockfile.Lock{}, fmt.Errorf("%s pins the providers of ocel %s and this is ocel %s — run `ocel lock` to pin the providers this version runs, and commit the change", lockfile.Name, lock.CLI, store.Version)
	}
	return lock, nil
}

func pin(ctx context.Context, store *providers.Store, projectDir string) error {
	sums, err := store.Checksums(ctx)
	if err != nil {
		return fmt.Errorf("read the checksums of release %s: %w", store.Version, err)
	}
	return lockfile.Write(projectDir, lockfile.FromChecksums(store.Version, sums))
}
