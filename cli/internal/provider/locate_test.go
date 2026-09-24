package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/lockfile"
	"github.com/ocelhq/ocel/cli/internal/providers"
)

const locatedVersion = "0.4.1"

func releaseServing(t *testing.T, names ...string) *httptest.Server {
	t.Helper()

	var checksums strings.Builder
	for _, name := range names {
		for i, platform := range providers.Platforms {
			asset := providers.AssetName(providers.KindProvider, name, locatedVersion, platform.GOOS, platform.GOARCH)
			fmt.Fprintf(&checksums, "%064x  %s\n", i+1, asset)
		}
	}
	fmt.Fprintf(&checksums, "%064x  ocel_%s_linux_amd64.tar.gz\n", 99, locatedVersion)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case providers.ChecksumsAsset:
			_, _ = w.Write([]byte(checksums.String()))
		case providers.SignatureAsset:
			_, _ = w.Write(countersigned(checksums.String()))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func countersigned(checksums string) []byte {
	sum := sha256.Sum256([]byte(checksums))
	return fmt.Appendf(nil, "%s signs %x", providers.SignerIdentity(locatedVersion), sum)
}

func countersigns(checksums, signature []byte, identity string) error {
	if want := countersigned(string(checksums)); !bytes.Equal(signature, want) || identity != providers.SignerIdentity(locatedVersion) {
		return fmt.Errorf("%s carries %q, want %q signed as %s", providers.SignatureAsset, signature, want, identity)
	}
	return nil
}

func unsignedRelease(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) != providers.ChecksumsAsset {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, "%064x  %s\n", 1, providers.AssetName(providers.KindProvider, "aws", locatedVersion, "linux", "amd64"))
	}))
	t.Cleanup(server.Close)
	return server
}

func storeOn(t *testing.T, server *httptest.Server, goos, goarch string) *providers.Store {
	t.Helper()
	return &providers.Store{
		Dir:      t.TempDir(),
		Version:  locatedVersion,
		Platform: providers.Platform{GOOS: goos, GOARCH: goarch},
		BaseURL:  server.URL,
		HTTP:     server.Client(),
		Sleep:    func(time.Duration) {},
		Verify:   countersigns,
	}
}

func TestTheLockIsWrittenBesideTheConfigOnTheFirstRunOfAVersion(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "aws", "vps")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	if _, err := locate(context.Background(), store, providers.KindProvider, projectDir, "aws", store.Platform, pinToLock); err == nil {
		t.Fatal("locate() error = nil, want the fetch of an archive this release does not serve to fail")
	}

	lock, held, err := lockfile.Read(projectDir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !held {
		t.Fatalf("no %s was written beside the config", lockfile.Name)
	}
	if lock.CLI != locatedVersion {
		t.Fatalf("lock.CLI = %q, want %q", lock.CLI, locatedVersion)
	}
	for _, name := range []string{"aws", "vps"} {
		if len(lock.Providers[name]) != len(providers.Platforms) {
			t.Fatalf("%s pins %d platforms, want all %d", name, len(lock.Providers[name]), len(providers.Platforms))
		}
	}
}

func TestADryRunHoldingNoLockPinsInMemoryAndWritesNothing(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "aws")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	_, err := locate(context.Background(), store, providers.KindProvider, projectDir, "aws", store.Platform, pinInMemory)
	if err == nil {
		t.Fatal("locate() error = nil, want the fetch of an archive this release does not serve to fail")
	}
	archive := providers.AssetName(providers.KindProvider, "aws", locatedVersion, "linux", "amd64")
	if !strings.Contains(err.Error(), archive) {
		t.Fatalf("error %q does not name %s, want the dry run pinned and on to fetching it", err.Error(), archive)
	}
	if _, err := os.Stat(lockfile.Path(projectDir)); err == nil {
		t.Fatalf("%s was written by a dry run", lockfile.Name)
	}
}

func TestALockWrittenOnOnePlatformMatchesTheOneWrittenOnAnother(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "aws", "gcp", "vps")

	written := map[string][]byte{}
	for _, host := range []providers.Platform{{GOOS: "darwin", GOARCH: "arm64"}, {GOOS: "linux", GOARCH: "amd64"}} {
		projectDir := t.TempDir()
		store := storeOn(t, server, host.GOOS, host.GOARCH)
		_, _ = locate(context.Background(), store, providers.KindProvider, projectDir, "aws", store.Platform, pinToLock)

		raw, err := os.ReadFile(lockfile.Path(projectDir))
		if err != nil {
			t.Fatalf("read the lock written on %s: %v", host.Dir(), err)
		}
		written[host.Dir()] = raw
	}

	if a, b := written["darwin-arm64"], written["linux-amd64"]; string(a) != string(b) {
		t.Fatalf("the lock differs by host platform:\n%s\nvs\n%s", a, b)
	}
}

func TestALockPinningAnotherVersionIsRefusedRatherThanRewritten(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "aws")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	pinned := lockfile.Lock{CLI: "0.3.0", Providers: map[string]map[string]string{"aws": {"linux-amd64": "abc"}}}
	if err := lockfile.Write(projectDir, pinned); err != nil {
		t.Fatalf("Write: %v", err)
	}
	before, err := os.ReadFile(lockfile.Path(projectDir))
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}

	_, err = locate(context.Background(), store, providers.KindProvider, projectDir, "aws", store.Platform, pinToLock)
	if err == nil {
		t.Fatal("locate() error = nil, want a lock pinning another version refused")
	}
	for _, want := range []string{lockfile.Name, "0.3.0", locatedVersion, "ocel lock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}

	after, err := os.ReadFile(lockfile.Path(projectDir))
	if err != nil {
		t.Fatalf("read the lock back: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("the pin regenerated itself:\n%s\nwant\n%s", after, before)
	}
}

func TestPinRewritesALockThatPinsAnotherVersion(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "aws", "vps")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	stale := lockfile.Lock{CLI: "0.3.0", Providers: map[string]map[string]string{"aws": {"linux-amd64": "abc"}}}
	if err := lockfile.Write(projectDir, stale); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := pin(context.Background(), store, projectDir); err != nil {
		t.Fatalf("pin: %v", err)
	}

	lock, held, err := lockfile.Read(projectDir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !held {
		t.Fatalf("no %s was left beside the config", lockfile.Name)
	}
	if lock.CLI != locatedVersion {
		t.Fatalf("lock.CLI = %q, want %q", lock.CLI, locatedVersion)
	}
	if len(lock.Providers["vps"]) != len(providers.Platforms) {
		t.Fatalf("vps pins %d platforms, want all %d", len(lock.Providers["vps"]), len(providers.Platforms))
	}
}

func TestAPinnedLockIsNotRewrittenOrRefetched(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "aws")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	_, _ = locate(context.Background(), store, providers.KindProvider, projectDir, "aws", store.Platform, pinToLock)
	first, err := os.ReadFile(lockfile.Path(projectDir))
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}

	server.Close()

	if _, err := locate(context.Background(), store, providers.KindProvider, projectDir, "aws", store.Platform, pinToLock); err == nil {
		t.Fatal("locate() error = nil, want the fetch to fail against a closed release")
	}
	second, err := os.ReadFile(lockfile.Path(projectDir))
	if err != nil {
		t.Fatalf("read the lock back: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("the lock was rewritten on a run that changed no version")
	}
}

func TestAProviderTheLockDoesNotPinIsRefusedByName(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "aws")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	_, err := locate(context.Background(), store, providers.KindProvider, projectDir, "nowhere", store.Platform, pinToLock)
	if err == nil {
		t.Fatal("locate() error = nil, want the unpinned provider refused")
	}
	for _, want := range []string{lockfile.Name, "nowhere", locatedVersion} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

func TestAProvidersDirResolvesWithNothingOnPATH(t *testing.T) {
	server := releaseServing(t, "aws")
	store := storeOn(t, server, "linux", "amd64")
	store.Override = t.TempDir()
	t.Setenv("PATH", t.TempDir())

	dir := filepath.Join(store.Override, "provider", "aws", locatedVersion, "linux-amd64")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "provider-aws")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	projectDir := t.TempDir()
	got, err := locate(context.Background(), store, providers.KindProvider, projectDir, "aws", store.Platform, pinToLock)
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if got != want {
		t.Fatalf("locate() = %q, want %q", got, want)
	}
	if _, err := os.Stat(lockfile.Path(projectDir)); err == nil {
		t.Fatalf("%s was written for a run that fetched nothing", lockfile.Name)
	}
}

func TestAReleaseThatSignsNoChecksumsPinsNothing(t *testing.T) {
	t.Parallel()

	for _, pinning := range []pinning{pinToLock, pinInMemory} {
		store := storeOn(t, unsignedRelease(t), "linux", "amd64")
		projectDir := t.TempDir()

		_, err := locate(context.Background(), store, providers.KindProvider, projectDir, "aws", store.Platform, pinning)
		if err == nil {
			t.Fatal("locate() error = nil, want a release that signs no checksums refused")
		}
		if !strings.Contains(err.Error(), providers.SignatureAsset) {
			t.Errorf("error %q does not name the signature it looked for", err.Error())
		}
		if _, err := os.Stat(lockfile.Path(projectDir)); err == nil {
			t.Fatalf("%s was written from checksums nothing signed", lockfile.Name)
		}
	}
}

func TestChecksumsTheSignatureDoesNotCoverPinNothing(t *testing.T) {
	t.Parallel()

	store := storeOn(t, releaseServing(t, "aws"), "linux", "amd64")
	store.Verify = func(checksums, signature []byte, identity string) error {
		return countersigns(append(checksums, '\n'), signature, identity)
	}
	projectDir := t.TempDir()

	if _, err := locate(context.Background(), store, providers.KindProvider, projectDir, "aws", store.Platform, pinToLock); err == nil {
		t.Fatal("locate() error = nil, want checksums the signature does not cover refused")
	}
	if _, err := os.Stat(lockfile.Path(projectDir)); err == nil {
		t.Fatalf("%s was written from checksums the signature does not cover", lockfile.Name)
	}
}
