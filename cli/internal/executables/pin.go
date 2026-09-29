package executables

import (
	"context"
	"fmt"
	"os"

	"github.com/ocelhq/ocel/cli/internal/lockfile"
	"github.com/ocelhq/ocel/cli/internal/version"
)

type Pinning int

const (
	PinToLock Pinning = iota
	PinInMemory
)

func ChoosePinning(dry bool) Pinning {
	if dry {
		return PinInMemory
	}
	return PinToLock
}

func EnsureProvider(ctx context.Context, projectDir, name string, pinning Pinning) (string, error) {
	store, err := New(version.Version)
	if err != nil {
		return "", err
	}
	return ensurePinned(ctx, store, KindProvider, projectDir, name, store.Platform, pinning)
}

func EnsureConnector(ctx context.Context, projectDir, name string, platform Platform) ([]byte, error) {
	store, err := New(version.Version)
	if err != nil {
		return nil, err
	}
	path, err := ensurePinned(ctx, store, KindConnector, projectDir, name, platform, PinToLock)
	if err != nil {
		return nil, err
	}
	read, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the %s connector built for %s: %w", name, platform.Dir(), err)
	}
	return read, nil
}

func Pin(ctx context.Context, projectDir string) error {
	store, err := New(version.Version)
	if err != nil {
		return err
	}
	_, err = writeReleaseLock(ctx, store, projectDir)
	return err
}

func ensurePinned(ctx context.Context, store *Store, kind Kind, projectDir, name string, platform Platform, pinning Pinning) (string, error) {
	if store.HasOverride() {
		return store.EnsureBinary(ctx, kind, name, platform, "")
	}

	lock, err := ensureLock(ctx, store, projectDir, pinning)
	if err != nil {
		return "", err
	}

	digest, pinned := pinnedDigest(lock, kind, name, platform)
	if !pinned {
		return "", fmt.Errorf("%s pins no %s %s %s for %s — release %s ships no such archive", lockfile.Name, name, kind, store.Version, platform.Dir(), store.Version)
	}
	return store.EnsureBinary(ctx, kind, name, platform, digest)
}

func ensureLock(ctx context.Context, store *Store, projectDir string, pinning Pinning) (lockfile.Lock, error) {
	lock, found, err := lockfile.Read(projectDir)
	if err != nil {
		return lockfile.Lock{}, err
	}
	if pinning == PinInMemory && (!found || lock.CLI != store.Version) {
		return readReleaseLock(ctx, store)
	}
	if !found {
		return writeReleaseLock(ctx, store, projectDir)
	}
	if lock.CLI != store.Version {
		return lockfile.Lock{}, fmt.Errorf("%s pins the providers of ocel %s and this is ocel %s — run `ocel lock` to pin the providers this version runs, and commit the change", lockfile.Name, lock.CLI, store.Version)
	}
	return lock, nil
}

func writeReleaseLock(ctx context.Context, store *Store, projectDir string) (lockfile.Lock, error) {
	lock, err := readReleaseLock(ctx, store)
	if err != nil {
		return lockfile.Lock{}, err
	}
	if err := lockfile.Write(projectDir, lock); err != nil {
		return lockfile.Lock{}, err
	}
	return lock, nil
}

func readReleaseLock(ctx context.Context, store *Store) (lockfile.Lock, error) {
	sums, err := store.Checksums(ctx)
	if err != nil {
		return lockfile.Lock{}, fmt.Errorf("read the checksums of release %s: %w", store.Version, err)
	}
	return lockFromChecksums(store.Version, sums), nil
}

func lockFromChecksums(version string, sums map[string]string) lockfile.Lock {
	lock := lockfile.Lock{
		CLI:        version,
		Providers:  map[string]map[string]string{},
		Connectors: map[string]map[string]string{},
	}
	for asset, digest := range sums {
		parsed, ok := ParseAssetName(asset)
		if !ok || parsed.Version != version {
			continue
		}
		platform := Platform{GOOS: parsed.GOOS, GOARCH: parsed.GOARCH}
		kindPins := pinsOfKind(lock, parsed.Kind)
		pinned, known := kindPins[parsed.Name]
		if !known {
			pinned = map[string]string{}
			kindPins[parsed.Name] = pinned
		}
		pinned[platform.Dir()] = digest
	}
	return lock
}

func pinsOfKind(lock lockfile.Lock, kind Kind) map[string]map[string]string {
	if kind == KindConnector {
		return lock.Connectors
	}
	return lock.Providers
}

func pinnedDigest(lock lockfile.Lock, kind Kind, name string, platform Platform) (string, bool) {
	digest, ok := pinsOfKind(lock, kind)[name][platform.Dir()]
	return digest, ok
}
