package provider

import (
	"context"
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
			asset := providers.AssetName(name, locatedVersion, platform.GOOS, platform.GOARCH)
			fmt.Fprintf(&checksums, "%064x  %s\n", i+1, asset)
		}
	}
	fmt.Fprintf(&checksums, "%064x  ocel_%s_linux_amd64.tar.gz\n", 99, locatedVersion)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) != providers.ChecksumsAsset {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(checksums.String()))
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
	}
}

func TestTheLockIsWrittenBesideTheConfigOnTheFirstRunOfAVersion(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "aws", "vps")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	if _, err := locate(context.Background(), store, projectDir, "aws"); err == nil {
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

func TestALockWrittenOnOnePlatformMatchesTheOneWrittenOnAnother(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "aws", "gcp", "vps")

	written := map[string][]byte{}
	for _, host := range []providers.Platform{{GOOS: "darwin", GOARCH: "arm64"}, {GOOS: "linux", GOARCH: "amd64"}} {
		projectDir := t.TempDir()
		store := storeOn(t, server, host.GOOS, host.GOARCH)
		_, _ = locate(context.Background(), store, projectDir, "aws")

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

func TestAPinnedLockIsNotRewrittenOrRefetched(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "aws")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	_, _ = locate(context.Background(), store, projectDir, "aws")
	first, err := os.ReadFile(lockfile.Path(projectDir))
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}

	server.Close()

	if _, err := locate(context.Background(), store, projectDir, "aws"); err == nil {
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

	_, err := locate(context.Background(), store, projectDir, "nowhere")
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

	dir := filepath.Join(store.Override, "aws", locatedVersion, "linux-amd64")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "provider-aws")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	projectDir := t.TempDir()
	got, err := locate(context.Background(), store, projectDir, "aws")
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
