package executables

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
)

const pinnedVersion = "0.4.1"

func releaseServing(t *testing.T, names ...string) *httptest.Server {
	t.Helper()

	var checksums strings.Builder
	for _, name := range names {
		for i, platform := range Platforms {
			asset := AssetName(KindProvider, name, pinnedVersion, platform.GOOS, platform.GOARCH)
			fmt.Fprintf(&checksums, "%064x  %s\n", i+1, asset)
		}
	}
	fmt.Fprintf(&checksums, "%064x  ocel_%s_linux_amd64.tar.gz\n", 99, pinnedVersion)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case ChecksumsAsset:
			_, _ = w.Write([]byte(checksums.String()))
		case SignatureAsset:
			_, _ = w.Write(countersigned(checksums.String(), SignerIdentity(pinnedVersion)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func unsignedRelease(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) != ChecksumsAsset {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, "%064x  %s\n", 1, AssetName(KindProvider, "fake", pinnedVersion, "linux", "amd64"))
	}))
	t.Cleanup(server.Close)
	return server
}

var pinningModes = []struct {
	name string
	mode Pinning
}{
	{"to the lock", PinToLock},
	{"in memory", PinInMemory},
}

func storeOn(t *testing.T, server *httptest.Server, goos, goarch string) *Store {
	t.Helper()
	return &Store{
		Dir:      t.TempDir(),
		Version:  pinnedVersion,
		Platform: Platform{GOOS: goos, GOARCH: goarch},
		BaseURL:  server.URL,
		HTTP:     server.Client(),
		Sleep:    func(time.Duration) {},
		Verify:   countersigns,
	}
}

func TestTheLockIsWrittenBesideTheConfigOnTheFirstRunOfAVersion(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "fake", "remote")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	if _, err := ensurePinned(context.Background(), store, KindProvider, projectDir, "fake", store.Platform, PinToLock); err == nil {
		t.Fatal("ensurePinned() error = nil, want the fetch of an archive this release does not serve to fail")
	}

	lock, found, err := lockfile.Read(projectDir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !found {
		t.Fatalf("no %s was written beside the config", lockfile.Name)
	}
	if lock.CLI != pinnedVersion {
		t.Fatalf("lock.CLI = %q, want %q", lock.CLI, pinnedVersion)
	}
	for _, name := range []string{"fake", "remote"} {
		if len(lock.Providers[name]) != len(Platforms) {
			t.Fatalf("%s pins %d platforms, want all %d", name, len(lock.Providers[name]), len(Platforms))
		}
	}
}

func TestADryRunWithNoLockPinsInMemoryAndWritesNothing(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "fake")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	_, err := ensurePinned(context.Background(), store, KindProvider, projectDir, "fake", store.Platform, PinInMemory)
	if err == nil {
		t.Fatal("ensurePinned() error = nil, want the fetch of an archive this release does not serve to fail")
	}
	archive := AssetName(KindProvider, "fake", pinnedVersion, "linux", "amd64")
	if !strings.Contains(err.Error(), archive) {
		t.Fatalf("error %q does not name %s, want the dry run pinned and on to fetching it", err.Error(), archive)
	}
	if _, err := os.Stat(lockfile.Path(projectDir)); err == nil {
		t.Fatalf("%s was written by a dry run", lockfile.Name)
	}
}

func TestADryRunAgainstALockPinningAnotherVersionPinsInMemoryAndLeavesTheLockAlone(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "fake")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	pinned := lockfile.Lock{CLI: "0.3.0", Providers: map[string]map[string]string{"fake": {"linux-amd64": "abc"}}}
	if err := lockfile.Write(projectDir, pinned); err != nil {
		t.Fatalf("Write: %v", err)
	}
	before, err := os.ReadFile(lockfile.Path(projectDir))
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}

	_, err = ensurePinned(context.Background(), store, KindProvider, projectDir, "fake", store.Platform, PinInMemory)
	if err == nil {
		t.Fatal("ensurePinned() error = nil, want the fetch of an archive this release does not serve to fail")
	}
	archive := AssetName(KindProvider, "fake", pinnedVersion, "linux", "amd64")
	if !strings.Contains(err.Error(), archive) {
		t.Fatalf("error %q does not name %s, want the dry run pinned from the release and on to fetching it", err.Error(), archive)
	}

	after, err := os.ReadFile(lockfile.Path(projectDir))
	if err != nil {
		t.Fatalf("read the lock back: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("a dry run rewrote the lock:\n%s\nwant\n%s", after, before)
	}
}

func TestALockWrittenOnOnePlatformMatchesTheOneWrittenOnAnother(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "fake", "other", "remote")

	written := map[string][]byte{}
	for _, host := range []Platform{{GOOS: "darwin", GOARCH: "arm64"}, {GOOS: "linux", GOARCH: "amd64"}} {
		projectDir := t.TempDir()
		store := storeOn(t, server, host.GOOS, host.GOARCH)
		_, _ = ensurePinned(context.Background(), store, KindProvider, projectDir, "fake", store.Platform, PinToLock)

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

	server := releaseServing(t, "fake")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	pinned := lockfile.Lock{CLI: "0.3.0", Providers: map[string]map[string]string{"fake": {"linux-amd64": "abc"}}}
	if err := lockfile.Write(projectDir, pinned); err != nil {
		t.Fatalf("Write: %v", err)
	}
	before, err := os.ReadFile(lockfile.Path(projectDir))
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}

	_, err = ensurePinned(context.Background(), store, KindProvider, projectDir, "fake", store.Platform, PinToLock)
	if err == nil {
		t.Fatal("ensurePinned() error = nil, want a lock pinning another version refused")
	}
	for _, want := range []string{lockfile.Name, "0.3.0", pinnedVersion, "ocel lock"} {
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

	server := releaseServing(t, "fake", "remote")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	stale := lockfile.Lock{CLI: "0.3.0", Providers: map[string]map[string]string{"fake": {"linux-amd64": "abc"}}}
	if err := lockfile.Write(projectDir, stale); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := writeReleaseLock(context.Background(), store, projectDir); err != nil {
		t.Fatalf("writeReleaseLock: %v", err)
	}

	lock, found, err := lockfile.Read(projectDir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !found {
		t.Fatalf("no %s was left beside the config", lockfile.Name)
	}
	if lock.CLI != pinnedVersion {
		t.Fatalf("lock.CLI = %q, want %q", lock.CLI, pinnedVersion)
	}
	if len(lock.Providers["remote"]) != len(Platforms) {
		t.Fatalf("remote pins %d platforms, want all %d", len(lock.Providers["remote"]), len(Platforms))
	}
}

func TestAPinnedLockIsNotRewrittenOrRefetched(t *testing.T) {
	t.Parallel()

	server := releaseServing(t, "fake")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	_, _ = ensurePinned(context.Background(), store, KindProvider, projectDir, "fake", store.Platform, PinToLock)
	first, err := os.ReadFile(lockfile.Path(projectDir))
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}

	server.Close()

	if _, err := ensurePinned(context.Background(), store, KindProvider, projectDir, "fake", store.Platform, PinToLock); err == nil {
		t.Fatal("ensurePinned() error = nil, want the fetch to fail against a closed release")
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

	server := releaseServing(t, "fake")
	store := storeOn(t, server, "linux", "amd64")
	projectDir := t.TempDir()

	_, err := ensurePinned(context.Background(), store, KindProvider, projectDir, "nowhere", store.Platform, PinToLock)
	if err == nil {
		t.Fatal("ensurePinned() error = nil, want the unpinned provider refused")
	}
	for _, want := range []string{lockfile.Name, "nowhere", pinnedVersion} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

func TestAProvidersDirResolvesWithNothingOnPATH(t *testing.T) {
	server := releaseServing(t, "fake")
	store := storeOn(t, server, "linux", "amd64")
	store.Override = t.TempDir()
	t.Setenv("PATH", t.TempDir())

	dir := filepath.Join(store.Override, "provider", "fake", pinnedVersion, "linux-amd64")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "provider-fake")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	projectDir := t.TempDir()
	got, err := ensurePinned(context.Background(), store, KindProvider, projectDir, "fake", store.Platform, PinToLock)
	if err != nil {
		t.Fatalf("ensurePinned: %v", err)
	}
	if got != want {
		t.Fatalf("ensurePinned() = %q, want %q", got, want)
	}
	if _, err := os.Stat(lockfile.Path(projectDir)); err == nil {
		t.Fatalf("%s was written for a run that fetched nothing", lockfile.Name)
	}
}

func TestAReleaseThatSignsNoChecksumsPinsNothing(t *testing.T) {
	t.Parallel()

	for _, tt := range pinningModes {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := storeOn(t, unsignedRelease(t), "linux", "amd64")
			projectDir := t.TempDir()

			_, err := ensurePinned(context.Background(), store, KindProvider, projectDir, "fake", store.Platform, tt.mode)
			if err == nil {
				t.Fatal("ensurePinned() error = nil, want a release that signs no checksums refused")
			}
			if !strings.Contains(err.Error(), SignatureAsset) {
				t.Errorf("error %q does not name the signature it looked for", err.Error())
			}
			if _, err := os.Stat(lockfile.Path(projectDir)); err == nil {
				t.Fatalf("%s was written from checksums nothing signed", lockfile.Name)
			}
		})
	}
}

func TestChecksumsTheSignatureDoesNotCoverPinNothing(t *testing.T) {
	t.Parallel()

	for _, tt := range pinningModes {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := storeOn(t, releaseServing(t, "fake"), "linux", "amd64")
			store.Verify = func(checksums, signature []byte, identity string) error {
				return countersigns(append(checksums, '\n'), signature, identity)
			}
			projectDir := t.TempDir()

			if _, err := ensurePinned(context.Background(), store, KindProvider, projectDir, "fake", store.Platform, tt.mode); err == nil {
				t.Fatal("ensurePinned() error = nil, want checksums the signature does not cover refused")
			}
			if _, err := os.Stat(lockfile.Path(projectDir)); err == nil {
				t.Fatalf("%s was written from checksums the signature does not cover", lockfile.Name)
			}
		})
	}
}

const releaseChecksums = `d0d0 ocel_0.2.0_linux_amd64.tar.gz
0002 ocel-provider-fake_0.2.0_darwin_arm64.tar.gz
0001 ocel-provider-fake_0.2.0_darwin_amd64.tar.gz
0003 ocel-provider-fake_0.2.0_linux_amd64.tar.gz
0004 ocel-provider-fake_0.2.0_linux_arm64.tar.gz
0005 ocel-provider-fake_0.2.0_windows_amd64.zip
0006 ocel-provider-remote_0.2.0_linux_amd64.tar.gz
0007 ocel-connector-remote_0.2.0_linux_amd64.tar.gz
0008 ocel-connector-remote_0.2.0_linux_arm64.tar.gz
beef ocel-provider-fake_0.1.0_linux_amd64.tar.gz
`

func renderedLock(t *testing.T, lock lockfile.Lock) []byte {
	t.Helper()
	raw, err := lock.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	return raw
}

func TestChecksumsParseTheDigestOfEveryProviderArchive(t *testing.T) {
	t.Parallel()

	sums, err := ParseChecksums(strings.NewReader(releaseChecksums))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	if got := sums["ocel-provider-fake_0.2.0_linux_amd64.tar.gz"]; got != "0003" {
		t.Fatalf("digest = %q, want %q", got, "0003")
	}
	if _, ok := sums["ocel_0.2.0_linux_amd64.tar.gz"]; !ok {
		t.Fatal("the CLI's own archive was dropped; the parse reads the file, it does not filter it")
	}
}

func TestTheLockListsEveryProviderOfTheVersionItPins(t *testing.T) {
	t.Parallel()

	sums, err := ParseChecksums(strings.NewReader(releaseChecksums))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	lock := lockFromChecksums("0.2.0", sums)

	if lock.CLI != "0.2.0" {
		t.Fatalf("lock.CLI = %q, want %q", lock.CLI, "0.2.0")
	}
	if got, ok := pinnedDigest(lock, KindProvider, "fake", Platform{GOOS: "windows", GOARCH: "amd64"}); !ok || got != "0005" {
		t.Fatalf("Digest(fake, windows-amd64) = %q, %v, want %q, true", got, ok, "0005")
	}
	if len(lock.Providers["fake"]) != 5 {
		t.Fatalf("fake pins %d platforms, want the five the release ships", len(lock.Providers["fake"]))
	}
	if _, ok := pinnedDigest(lock, KindProvider, "fake", Platform{GOOS: "linux", GOARCH: "386"}); ok {
		t.Fatal("the lock pinned a platform the release does not ship")
	}
	if _, ok := lock.Providers["ocel"]; ok {
		t.Fatal("the CLI's own archive was pinned as a provider")
	}
	if got, ok := pinnedDigest(lock, KindConnector, "remote", Platform{GOOS: "linux", GOARCH: "arm64"}); !ok || got != "0008" {
		t.Fatalf("Digest(connector, remote, linux-arm64) = %q, %v, want %q, true", got, ok, "0008")
	}
	if _, ok := lock.Providers["remote"]; !ok {
		t.Fatal("the remote provider and the remote connector share a name, and pinning one dropped the other")
	}
	if _, ok := pinnedDigest(lock, KindProvider, "remote", Platform{GOOS: "linux", GOARCH: "arm64"}); ok {
		t.Fatal("a connector archive was pinned as a provider")
	}
}

func TestAnotherVersionIsNotPinned(t *testing.T) {
	t.Parallel()

	sums, err := ParseChecksums(strings.NewReader(releaseChecksums))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	lock := lockFromChecksums("0.2.0", sums)
	for _, digest := range lock.Providers["fake"] {
		if digest == "beef" {
			t.Fatal("an archive of another version was pinned")
		}
	}
}

func TestTheLockIsWrittenInOneOrderWhateverOrderItWasBuiltIn(t *testing.T) {
	t.Parallel()

	forward, err := ParseChecksums(strings.NewReader(releaseChecksums))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(releaseChecksums), "\n")
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	backward, err := ParseChecksums(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}

	if a, b := renderedLock(t, lockFromChecksums("0.2.0", forward)), renderedLock(t, lockFromChecksums("0.2.0", backward)); string(a) != string(b) {
		t.Fatalf("the lock is not byte-identical across build order:\n%s\nvs\n%s", a, b)
	}
}
